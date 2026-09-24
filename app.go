package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFiles embed.FS

var errLocalMode = errors.New("目前是純本機模式；請先在基本設定切換並儲存 Slack 自動更新模式")

type app struct {
	mu           sync.Mutex
	updateMu     sync.Mutex
	dir          string
	state        State
	slack        *slackClient
	now          func() time.Time
	scheduleWake chan struct{}
}

func newApp(dir string, slack *slackClient) (*app, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	a := &app{dir: dir, slack: slack, now: time.Now, scheduleWake: make(chan struct{}, 1)}
	data, e := os.ReadFile(filepath.Join(dir, "state.json"))
	if e == nil {
		if e = json.Unmarshal(data, &a.state); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	} else {
		a.state.Settings = defaultSettings()
	}
	return a, nil
}
func (a *app) save() error {
	data, e := json.MarshalIndent(a.state, "", "  ")
	if e != nil {
		return e
	}
	return writePrivate(filepath.Join(a.dir, "state.json"), data)
}
func writePrivate(path string, data []byte) error {
	tmp := path + ".tmp"
	if e := os.WriteFile(tmp, data, 0600); e != nil {
		return e
	}
	if e := os.Rename(tmp, path); e != nil {
		return e
	}
	return nil
}
func (a *app) image() ([]byte, error) {
	data, e := os.ReadFile(filepath.Join(a.dir, "source.img"))
	if os.IsNotExist(e) {
		return os.ReadFile(filepath.Join(a.dir, "original.png"))
	}
	return data, e
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, e error) {
	jsonOut(w, status, map[string]any{"ok": false, "error": e.Error()})
}
func (a *app) handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /", a.home)
	m.HandleFunc("GET /api/status", a.status)
	m.HandleFunc("POST /api/settings", a.settings)
	m.HandleFunc("POST /api/preview", a.preview)
	m.HandleFunc("GET /api/download", a.download)
	m.HandleFunc("GET /oauth/start", a.oauthStart)
	m.HandleFunc("GET /oauth/callback", a.oauthCallback)
	m.HandleFunc("POST /api/slack/test", a.testSlack)
	m.HandleFunc("POST /api/slack/update", a.manualUpdate)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.Split(r.Host, ":")[0]
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" {
			http.Error(w, "Localhost only", http.StatusForbidden)
			return
		}
		if r.Method == "POST" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != "http://"+r.Host {
				http.Error(w, "Invalid origin", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		m.ServeHTTP(w, r)
	})
}
func (a *app) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, _ := webFiles.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}
func (a *app) status(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var lastSuccess, lastError *Attempt
	for i := len(a.state.Attempts) - 1; i >= 0; i-- {
		at := a.state.Attempts[i]
		if at.Success && lastSuccess == nil {
			lastSuccess = &at
		}
		if !at.Success && lastError == nil {
			lastError = &at
		}
	}
	jsonOut(w, 200, map[string]any{"settings": a.state.Settings, "hasImage": a.state.HasImage, "connected": a.state.Token.Access != "", "userId": a.state.Token.UserID, "team": a.state.Token.Team, "oauthConfigured": a.slack.clientID != "" && a.slack.clientSecret != "" && a.slack.redirect != "", "lastSuccess": lastSuccess, "lastError": lastError, "today": func() string { d, _ := today(a.state.Settings, a.now()); return d }()})
}
func (a *app) settings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if e := r.ParseMultipartForm(11 << 20); e != nil {
		fail(w, 400, e)
		return
	}
	var s Settings
	if e := json.Unmarshal([]byte(r.FormValue("settings")), &s); e != nil {
		fail(w, 400, errors.New("設定格式錯誤"))
		return
	}
	if e := s.validate(); e != nil {
		fail(w, 400, e)
		return
	}
	var uploaded []byte
	f, _, e := r.FormFile("image")
	if e == nil {
		defer f.Close()
		raw, e := io.ReadAll(io.LimitReader(f, (10<<20)+1))
		if e != nil {
			fail(w, 400, e)
			return
		}
		if _, e = decodeSource(raw); e != nil {
			fail(w, 400, e)
			return
		}
		uploaded = raw
	} else if !errors.Is(e, http.ErrMissingFile) {
		fail(w, 400, e)
		return
	}
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.state.HasImage && uploaded == nil {
		fail(w, 400, errors.New("請先上傳原圖"))
		return
	}
	if uploaded != nil {
		if e = writePrivate(filepath.Join(a.dir, "source.img"), uploaded); e != nil {
			fail(w, 500, e)
			return
		}
		a.state.HasImage = true
	}
	a.state.Settings = s
	if e = a.save(); e != nil {
		fail(w, 500, e)
		return
	}
	a.wakeScheduler()
	jsonOut(w, 200, map[string]any{"ok": true})
}

type previewRequest struct {
	Date     string   `json:"date"`
	Settings Settings `json:"settings"`
}

func (a *app) preview(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	var source []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
		if e := r.ParseMultipartForm(11 << 20); e != nil {
			fail(w, 400, e)
			return
		}
		if e := json.Unmarshal([]byte(r.FormValue("settings")), &req.Settings); e != nil {
			fail(w, 400, e)
			return
		}
		req.Date = r.FormValue("date")
		f, _, e := r.FormFile("image")
		if e != nil {
			fail(w, 400, e)
			return
		}
		defer f.Close()
		raw, e := io.ReadAll(io.LimitReader(f, (10<<20)+1))
		if e != nil {
			fail(w, 400, e)
			return
		}
		source = raw
	} else if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); e != nil {
		fail(w, 400, e)
		return
	}
	if e := req.Settings.validate(); e != nil {
		fail(w, 400, e)
		return
	}
	if source == nil {
		a.mu.Lock()
		var e error
		source, e = a.image()
		a.mu.Unlock()
		if e != nil {
			fail(w, 400, errors.New("請先上傳原圖"))
			return
		}
	}
	pngData, e := renderAvatar(source, req.Settings, req.Date)
	if e != nil {
		fail(w, 400, e)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(pngData)
}
func (a *app) download(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	s := a.state.Settings
	source, e := a.image()
	a.mu.Unlock()
	if e != nil {
		fail(w, 400, errors.New("請先上傳原圖"))
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		date, _ = today(s, a.now())
	}
	pngData, e := renderAvatar(source, s, date)
	if e != nil {
		fail(w, 400, e)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=avatar-%s.png", date))
	_, _ = w.Write(pngData)
}
func randomState() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func (a *app) oauthStart(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	local := a.state.Settings.mode() == "local"
	a.mu.Unlock()
	if local {
		http.Error(w, errLocalMode.Error(), http.StatusConflict)
		return
	}
	if a.slack.clientID == "" || a.slack.clientSecret == "" || a.slack.redirect == "" {
		http.Error(w, "請先設定 Slack OAuth 環境變數", 400)
		return
	}
	state, e := randomState()
	if e != nil {
		http.Error(w, "無法產生 state", 500)
		return
	}
	a.mu.Lock()
	a.state.OAuthState = state
	a.state.OAuthExpiry = a.now().Add(10 * time.Minute)
	e = a.save()
	a.mu.Unlock()
	if e != nil {
		http.Error(w, "無法儲存 OAuth state", 500)
		return
	}
	http.Redirect(w, r, a.slack.authorizeURL(state), http.StatusFound)
}
func (a *app) oauthCallback(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	local := a.state.Settings.mode() == "local"
	saved := a.state.OAuthState
	valid := saved != "" && subtle.ConstantTimeCompare([]byte(saved), []byte(r.URL.Query().Get("state"))) == 1 && a.now().Before(a.state.OAuthExpiry)
	a.state.OAuthState = ""
	a.state.OAuthExpiry = time.Time{}
	_ = a.save()
	a.mu.Unlock()
	if local {
		http.Error(w, errLocalMode.Error(), http.StatusConflict)
		return
	}
	if !valid {
		http.Error(w, "OAuth state 無效或已過期，請重新連接", 400)
		return
	}
	if reason := r.URL.Query().Get("error"); reason != "" {
		http.Error(w, "Slack 授權失敗: "+reason, 400)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Slack 未提供授權碼", 400)
		return
	}
	t, e := a.slack.oauth(r.Context(), code)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	a.mu.Lock()
	a.state.Token = t
	e = a.save()
	a.mu.Unlock()
	if e != nil {
		http.Error(w, "授權已取得，但無法保存 token", 500)
		return
	}
	a.wakeScheduler()
	http.Redirect(w, r, "/?connected=1", http.StatusSeeOther)
}
func (a *app) validToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.state.Token
	if t.Access == "" {
		return "", errors.New("尚未連接 Slack")
	}
	if !t.ExpiresAt.IsZero() && a.now().Add(5*time.Minute).After(t.ExpiresAt) {
		if t.Refresh == "" {
			return "", errors.New("Slack token 已過期，請重新授權")
		}
		updated, e := a.slack.refresh(ctx, t)
		if e != nil {
			return "", fmt.Errorf("Slack token 更新失敗: %w", e)
		}
		a.state.Token = updated
		if e = a.save(); e != nil {
			return "", e
		}
		t = updated
	}
	return t.Access, nil
}
func (a *app) testSlack(w http.ResponseWriter, r *http.Request) {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	a.mu.Lock()
	local := a.state.Settings.mode() == "local"
	a.mu.Unlock()
	if local {
		fail(w, http.StatusConflict, errLocalMode)
		return
	}
	token, e := a.validToken(r.Context())
	if e != nil {
		fail(w, 400, e)
		return
	}
	reply, e := a.slack.test(r.Context(), token)
	if e != nil {
		fail(w, 502, e)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "message": "Slack 連線成功", "user": reply.User, "userId": reply.UserID})
}
func (a *app) manualUpdate(w http.ResponseWriter, r *http.Request) {
	e := a.update(r.Context(), true)
	if e != nil {
		status := http.StatusBadGateway
		if errors.Is(e, errLocalMode) {
			status = http.StatusConflict
		}
		fail(w, status, e)
		return
	}
	a.wakeScheduler()
	jsonOut(w, 200, map[string]any{"ok": true, "message": "頭像已送至 Slack；若今天在倒數期間，這次成功計入今日完成紀錄。"})
}
func (a *app) record(at Attempt) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Attempts = append(a.state.Attempts, at)
	if len(a.state.Attempts) > 100 {
		a.state.Attempts = a.state.Attempts[len(a.state.Attempts)-100:]
	}
	return a.save()
}
func (a *app) update(ctx context.Context, manual bool) error {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	a.mu.Lock()
	s := a.state.Settings
	hasImage := a.state.HasImage
	if s.mode() == "local" {
		a.mu.Unlock()
		if manual {
			return errLocalMode
		}
		return nil
	}
	date, e := today(s, a.now())
	if e != nil {
		a.mu.Unlock()
		return e
	}
	if !manual {
		if !active(s, date) || !hasImage || a.state.Token.Access == "" {
			a.mu.Unlock()
			return nil
		}
		for _, at := range a.state.Attempts {
			if at.Date == date && at.Success {
				a.mu.Unlock()
				return nil
			}
		}
	}
	a.mu.Unlock()
	if !hasImage {
		return errors.New("請先上傳原圖")
	}
	if e = s.validate(); e != nil {
		return e
	}
	source, e := a.image()
	if e != nil {
		return e
	}
	pngData, e := renderAvatar(source, s, date)
	if e != nil {
		return e
	}
	at := Attempt{Date: date, At: a.now(), Manual: manual}
	token, e := a.validToken(ctx)
	if e == nil {
		e = a.slack.setPhoto(ctx, token, pngData)
	}
	if e != nil {
		at.Error = e.Error()
		if saveErr := a.record(at); saveErr != nil {
			return fmt.Errorf("%w；且無法儲存失敗紀錄: %v", e, saveErr)
		}
		return e
	}
	at.Success = true
	return a.record(at)
}
func (a *app) wakeScheduler() {
	select {
	case a.scheduleWake <- struct{}{}:
	default:
	}
}

// A zero delay waits for a settings or authorization change. Once today has
// succeeded, the next check is at midnight in the configured time zone.
func (a *app) nextScheduleDelay() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state.Settings
	if s.mode() == "local" {
		return 0
	}
	now := a.now()
	loc, e := time.LoadLocation(s.Timezone)
	if e != nil {
		return time.Hour
	}
	localNow := now.In(loc)
	date := localNow.Format("2006-01-02")
	for _, at := range a.state.Attempts {
		if at.Date == date && at.Success {
			return timeUntilNextDay(localNow)
		}
	}
	if active(s, date) && a.state.HasImage && a.state.Token.Access != "" {
		if untilTomorrow := timeUntilNextDay(localNow); untilTomorrow < time.Hour {
			return untilTomorrow // A new day takes precedence over yesterday's retry.
		}
		return time.Hour
	}
	return timeUntilNextDay(localNow)
}

func timeUntilNextDay(now time.Time) time.Duration {
	year, month, day := now.Date()
	next := time.Date(year, month, day+1, 0, 0, 0, 0, now.Location())
	if delay := next.Sub(now); delay > 0 {
		return delay
	}
	return time.Hour
}

func (a *app) runScheduler(ctx context.Context) {
	for {
		_ = a.update(ctx, false)
		delay := a.nextScheduleDelay()
		if delay == 0 {
			select {
			case <-ctx.Done():
				return
			case <-a.scheduleWake:
				continue
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.scheduleWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
