package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func seededApp(t *testing.T, dir string, client *slackClient) *app {
	t.Helper()
	a, e := newApp(dir, client)
	if e != nil {
		t.Fatal(e)
	}
	a.state.Settings = defaultSettings()
	a.state.Settings.Mode = "slack"
	a.state.Settings.StartDate = "2026-09-01"
	a.state.Settings.EndDate = "2026-09-30"
	a.state.HasImage = true
	a.state.Token = Token{Access: "xoxp-mock", UserID: "U1"}
	a.now = func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) }
	if e = writePrivate(dir+"/original.png", testSource(t)); e != nil {
		t.Fatal(e)
	}
	if e = a.save(); e != nil {
		t.Fatal(e)
	}
	return a
}
func TestPreviewUploadSameBytesAndDedupRestart(t *testing.T) {
	var uploads atomic.Int32
	var uploaded []byte
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users.setPhoto" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer xoxp-mock" {
			t.Error("missing user bearer")
		}
		f, _, e := r.FormFile("image")
		if e != nil {
			t.Error(e)
		} else {
			data, _ := io.ReadAll(f)
			mu.Lock()
			uploaded = data
			mu.Unlock()
			f.Close()
		}
		uploads.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := newSlack("", "", "")
	client.base = server.URL
	dir := t.TempDir()
	a := seededApp(t, dir, client)
	a.state.Settings.Effects.Dust = true
	if err := a.save(); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(previewRequest{Date: "2026-09-24", Settings: a.state.Settings})
	req := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/preview", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	a.handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	preview := rec.Body.Bytes()
	var form bytes.Buffer
	var proposal previewRequest
	_ = json.Unmarshal(body, &proposal)
	writer := multipart.NewWriter(&form)
	settingsJSON, _ := json.Marshal(proposal.Settings)
	_ = writer.WriteField("settings", string(settingsJSON))
	_ = writer.WriteField("date", proposal.Date)
	part, _ := writer.CreateFormFile("image", "source.png")
	_, _ = part.Write(testSource(t))
	_ = writer.Close()
	formReq := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/preview", &form)
	formReq.Header.Set("Content-Type", writer.FormDataContentType())
	formRec := httptest.NewRecorder()
	a.handler().ServeHTTP(formRec, formReq)
	if formRec.Code != 200 || !bytes.Equal(preview, formRec.Body.Bytes()) {
		t.Fatal("unsaved image preview differs")
	}
	download := httptest.NewRecorder()
	a.handler().ServeHTTP(download, httptest.NewRequest("GET", "http://127.0.0.1:8080/api/download?date=2026-09-24", nil))
	if download.Code != 200 || !bytes.Equal(preview, download.Body.Bytes()) {
		t.Fatal("preview and download differ")
	}
	if e := a.update(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	same := bytes.Equal(preview, uploaded)
	mu.Unlock()
	if !same {
		t.Fatal("preview and upload differ")
	}
	if e := a.update(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	restarted, e := newApp(dir, client)
	if e != nil {
		t.Fatal(e)
	}
	restarted.now = a.now
	if e = restarted.update(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if uploads.Load() != 1 {
		t.Fatalf("dedup/restart uploads=%d", uploads.Load())
	}
}
func TestConcurrentAndFailureRetry(t *testing.T) {
	var n atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := n.Add(1)
		if count == 1 {
			_, _ = w.Write([]byte(`{"ok":false,"error":"not_allowed"}`))
			return
		}
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := newSlack("", "", "")
	client.base = server.URL
	a := seededApp(t, t.TempDir(), client)
	if e := a.update(context.Background(), false); e == nil || !strings.Contains(e.Error(), "not_allowed") {
		t.Fatalf("failure not surfaced: %v", e)
	}
	if len(a.state.Attempts) != 1 || a.state.Attempts[0].Success {
		t.Fatal("failure not recorded")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = a.update(context.Background(), false) }()
	}
	wg.Wait()
	if n.Load() != 2 {
		t.Fatalf("parallel duplicate uploads=%d", n.Load())
	}
	if len(a.state.Attempts) != 2 || !a.state.Attempts[1].Success {
		t.Fatal("retry not recorded")
	}
}

func TestManualCountsTodayAndAfterEnd(t *testing.T) {
	var n atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1); _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer server.Close()
	client := newSlack("", "", "")
	client.base = server.URL
	a := seededApp(t, t.TempDir(), client)
	if e := a.update(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	if e := a.update(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if n.Load() != 1 {
		t.Fatal("manual success did not count today")
	}
	a.now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	if e := a.update(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if n.Load() != 1 {
		t.Fatal("automatic update after end date")
	}
	if e := a.update(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	if n.Load() != 2 {
		t.Fatal("manual update after end date blocked")
	}
}

func TestCropSavedOriginalSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	client := newSlack("", "", "")
	a, e := newApp(dir, client)
	if e != nil {
		t.Fatal(e)
	}
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			if x < 100 {
				img.SetRGBA(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	var source bytes.Buffer
	_ = png.Encode(&source, img)
	s := defaultSettings()
	s.StartDate = "2026-09-01"
	s.EndDate = "2026-09-30"
	s.Crop = Crop{Mode: "crop", X: 100, Y: 50, Zoom: 100}
	settingJSON, _ := json.Marshal(s)
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	_ = writer.WriteField("settings", string(settingJSON))
	part, _ := writer.CreateFormFile("image", "wide.png")
	_, _ = part.Write(source.Bytes())
	_ = writer.Close()
	req := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/settings", &form)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	a.handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	restarted, e := newApp(dir, client)
	if e != nil {
		t.Fatal(e)
	}
	if restarted.state.Settings.Crop.X != 100 {
		t.Fatal("crop settings not saved")
	}
	raw, e := restarted.image()
	if e != nil {
		t.Fatal(e)
	}
	cfg, _, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || cfg.Width != 200 || cfg.Height != 100 {
		t.Fatal("original rectangle lost")
	}
	result, e := renderAvatar(raw, restarted.state.Settings, "2026-09-01")
	if e != nil {
		t.Fatal(e)
	}
	square, e := png.Decode(bytes.NewReader(result))
	if e != nil {
		t.Fatal(e)
	}
	pixel := color.RGBAModel.Convert(square.At(320, 320)).(color.RGBA)
	if pixel.B != 255 || pixel.R != 0 {
		t.Fatalf("saved crop wrong: %v", pixel)
	}
}
func TestOAuthStateAndRefresh(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/oauth.v2.access" {
			t.Error(r.URL.Path)
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			_, _ = w.Write([]byte(`{"ok":true,"authed_user":{"access_token":"new-user","refresh_token":"new-refresh","expires_in":43200}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"authed_user":{"id":"U1","scope":"users.profile:write","access_token":"xoxp-mock","refresh_token":"refresh","expires_in":43200},"team":{"name":"Mock"}}`))
	}))
	defer server.Close()
	client := newSlack("id", "secret", "http://127.0.0.1:8080/oauth/callback")
	client.base = server.URL
	a, _ := newApp(t.TempDir(), client)
	a.state.Settings.Mode = "slack"
	a.now = time.Now
	start := httptest.NewRecorder()
	a.handler().ServeHTTP(start, httptest.NewRequest("GET", "http://127.0.0.1:8080/oauth/start", nil))
	loc, e := url.Parse(start.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("no state")
	}
	bad := httptest.NewRecorder()
	a.handler().ServeHTTP(bad, httptest.NewRequest("GET", "http://127.0.0.1:8080/oauth/callback?state=bad&code=code", nil))
	if bad.Code != 400 || calls.Load() != 0 {
		t.Fatal("invalid state accepted")
	}
	a.state.OAuthState = state
	a.state.OAuthExpiry = time.Now().Add(time.Minute)
	good := httptest.NewRecorder()
	a.handler().ServeHTTP(good, httptest.NewRequest("GET", "http://127.0.0.1:8080/oauth/callback?state="+state+"&code=code", nil))
	if good.Code != 303 {
		t.Fatalf("oauth: %d %s", good.Code, good.Body.String())
	}
	a.state.Token.ExpiresAt = time.Now().Add(-time.Minute)
	token, e := a.validToken(context.Background())
	if e != nil || token != "new-user" || a.state.Token.Refresh != "new-refresh" {
		t.Fatalf("refresh: %s %v", token, e)
	}
}
func TestSlackMultipartAndStatus(t *testing.T) {
	var got bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users.setPhoto" {
			_, _, e := r.FormFile("image")
			got = e == nil
			_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"user":"mock","team":"Mock workspace"}`))
	}))
	defer server.Close()
	client := newSlack("", "", "")
	client.base = server.URL
	if e := client.setPhoto(context.Background(), "token", []byte("png")); e == nil || !strings.Contains(e.Error(), "missing_scope") || !got {
		t.Fatalf("setPhoto error=%v multipart=%v", e, got)
	}
	reply, e := client.test(context.Background(), "token")
	if e != nil || reply.User != "mock" {
		t.Fatal(e)
	}
	_ = multipart.ErrMessageTooLarge
}

func TestLocalModeShowsTodayAndNeverCallsSlack(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := newSlack("id", "secret", "http://127.0.0.1:8080/oauth/callback")
	client.base = server.URL
	dir := t.TempDir()
	a := seededApp(t, dir, client)
	a.now = func() time.Time { return time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC) } // Sep 25 in Taipei.
	localSettings := a.state.Settings
	localSettings.Mode = "local"
	settingsJSON, _ := json.Marshal(localSettings)
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	_ = writer.WriteField("settings", string(settingsJSON))
	_ = writer.Close()
	saveRequest := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/settings", &form)
	saveRequest.Header.Set("Content-Type", writer.FormDataContentType())
	saveResponse := httptest.NewRecorder()
	a.handler().ServeHTTP(saveResponse, saveRequest)
	if saveResponse.Code != 200 {
		t.Fatalf("mode switch: %d %s", saveResponse.Code, saveResponse.Body.String())
	}
	restarted, e := newApp(dir, client)
	if e != nil {
		t.Fatal(e)
	}
	restarted.now = a.now
	if e = restarted.update(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 0 || len(restarted.state.Attempts) != 0 {
		t.Fatal("local mode used Slack or recorded an upload")
	}
	for _, path := range []string{"/api/slack/update", "/api/slack/test"} {
		rec := httptest.NewRecorder()
		restarted.handler().ServeHTTP(rec, httptest.NewRequest("POST", "http://127.0.0.1:8080"+path, nil))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "純本機模式") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	oauth := httptest.NewRecorder()
	restarted.handler().ServeHTTP(oauth, httptest.NewRequest("GET", "http://127.0.0.1:8080/oauth/start", nil))
	if oauth.Code != http.StatusConflict || calls.Load() != 0 {
		t.Fatal("local mode allowed Slack authorization")
	}
	download := httptest.NewRecorder()
	restarted.handler().ServeHTTP(download, httptest.NewRequest("GET", "http://127.0.0.1:8080/api/download", nil))
	source, _ := restarted.image()
	want, e := renderAvatar(source, restarted.state.Settings, "2026-09-25")
	if e != nil || download.Code != 200 || !bytes.Equal(download.Body.Bytes(), want) {
		t.Fatal("local today download differs from the saved render for the chosen timezone")
	}
	status := httptest.NewRecorder()
	restarted.handler().ServeHTTP(status, httptest.NewRequest("GET", "http://127.0.0.1:8080/api/status", nil))
	if !bytes.Contains(status.Body.Bytes(), []byte(`"today":"2026-09-25"`)) {
		t.Fatal("today did not use the selected timezone")
	}
}

func TestModeValidationAndLegacySlack(t *testing.T) {
	s := defaultSettings()
	s.StartDate, s.EndDate = "2026-09-01", "2026-09-30"
	if s.mode() != "local" || s.validate() != nil {
		t.Fatal("new settings should default to local mode")
	}
	s.Mode = "unknown"
	if s.validate() == nil {
		t.Fatal("unknown mode accepted")
	}
	s.Mode = ""
	if s.mode() != "slack" || s.validate() != nil {
		t.Fatal("legacy settings should retain Slack mode")
	}
}

func TestSchedulerWaitsUntilLocalMidnightAfterSuccess(t *testing.T) {
	a := seededApp(t, t.TempDir(), newSlack("", "", ""))
	// 12:00 UTC is 20:00 in the configured Asia/Taipei time zone.
	if got := a.nextScheduleDelay(); got != time.Hour {
		t.Fatalf("pending update retry delay = %s", got)
	}
	a.now = func() time.Time { return time.Date(2026, 9, 24, 15, 30, 0, 0, time.UTC) } // 23:30 Taipei.
	if got := a.nextScheduleDelay(); got != 30*time.Minute {
		t.Fatalf("new local day should take precedence over retry, got %s", got)
	}
	a.now = func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) }
	a.state.Attempts = append(a.state.Attempts, Attempt{Date: "2026-09-24", Success: true})
	if got := a.nextScheduleDelay(); got != 4*time.Hour {
		t.Fatalf("successful day should wait until local midnight, got %s", got)
	}
	a.state.Settings.Mode = "local"
	if got := a.nextScheduleDelay(); got != 0 {
		t.Fatalf("local mode should wait for settings change, got %s", got)
	}
}
