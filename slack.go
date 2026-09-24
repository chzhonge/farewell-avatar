package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

type slackClient struct {
	http                                   *http.Client
	base, clientID, clientSecret, redirect string
}
type slackReply struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	AuthedUser   struct {
		ID           string `json:"id"`
		Scope        string `json:"scope"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	} `json:"authed_user"`
	Team   json.RawMessage `json:"team"`
	UserID string          `json:"user_id"`
	User   string          `json:"user"`
}

func newSlack(id, secret, redirect string) *slackClient {
	return &slackClient{http: &http.Client{Timeout: 25 * time.Second}, base: "https://slack.com/api", clientID: id, clientSecret: secret, redirect: redirect}
}
func (s *slackClient) authorizeURL(state string) string {
	v := url.Values{"client_id": {s.clientID}, "user_scope": {"users.profile:write"}, "redirect_uri": {s.redirect}, "state": {state}}
	return "https://slack.com/oauth/v2/authorize?" + v.Encode()
}
func (s *slackClient) call(ctx context.Context, method, token, contentType string, body io.Reader) (slackReply, error) {
	req, e := http.NewRequestWithContext(ctx, "POST", s.base+"/"+method, body)
	if e != nil {
		return slackReply{}, e
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, e := s.http.Do(req)
	if e != nil {
		return slackReply{}, fmt.Errorf("Slack 連線失敗: %w", e)
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return slackReply{}, e
	}
	var result slackReply
	if e = json.Unmarshal(data, &result); e != nil {
		return result, errors.New("Slack 回應格式錯誤")
	}
	if resp.StatusCode != 200 {
		return result, fmt.Errorf("Slack HTTP %d: %s", resp.StatusCode, result.Error)
	}
	if !result.OK {
		return result, fmt.Errorf("Slack API: %s", result.Error)
	}
	return result, nil
}
func (s *slackClient) oauth(ctx context.Context, code string) (Token, error) {
	v := url.Values{"client_id": {s.clientID}, "client_secret": {s.clientSecret}, "redirect_uri": {s.redirect}, "code": {code}}
	r, e := s.call(ctx, "oauth.v2.access", "", "application/x-www-form-urlencoded", strings.NewReader(v.Encode()))
	if e != nil {
		return Token{}, e
	}
	if r.AuthedUser.AccessToken == "" || !hasScope(r.AuthedUser.Scope) {
		return Token{}, errors.New("Slack 未授予 users.profile:write user token")
	}
	var team struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(r.Team, &team)
	t := Token{Access: r.AuthedUser.AccessToken, Refresh: r.AuthedUser.RefreshToken, UserID: r.AuthedUser.ID, Team: team.Name}
	if r.AuthedUser.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(r.AuthedUser.ExpiresIn) * time.Second)
	}
	return t, nil
}
func (s *slackClient) refresh(ctx context.Context, t Token) (Token, error) {
	v := url.Values{"client_id": {s.clientID}, "client_secret": {s.clientSecret}, "grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}}
	r, e := s.call(ctx, "oauth.v2.access", "", "application/x-www-form-urlencoded", strings.NewReader(v.Encode()))
	if e != nil {
		return t, e
	}
	access, refresh, expires := r.AuthedUser.AccessToken, r.AuthedUser.RefreshToken, r.AuthedUser.ExpiresIn
	if access == "" {
		access, refresh, expires = r.AccessToken, r.RefreshToken, r.ExpiresIn
	}
	if access == "" || refresh == "" || expires <= 0 {
		return t, errors.New("Slack 未回傳完整的更新 token")
	}
	t.Access = access
	t.Refresh = refresh
	t.ExpiresAt = time.Now().Add(time.Duration(expires) * time.Second)
	return t, nil
}
func (s *slackClient) test(ctx context.Context, token string) (slackReply, error) {
	return s.call(ctx, "auth.test", token, "application/x-www-form-urlencoded", strings.NewReader(""))
}
func (s *slackClient) setPhoto(ctx context.Context, token string, pngData []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, e := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="image"; filename="avatar.png"`}, "Content-Type": {"image/png"}})
	if e != nil {
		return e
	}
	if _, e = part.Write(pngData); e != nil {
		return e
	}
	if e = w.Close(); e != nil {
		return e
	}
	_, e = s.call(ctx, "users.setPhoto", token, w.FormDataContentType(), &body)
	return e
}
