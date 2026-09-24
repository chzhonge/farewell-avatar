package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Effects struct {
	Dust          bool   `json:"dust"`
	Gray          bool   `json:"gray"`
	Blur          bool   `json:"blur"`
	BlurMax       int    `json:"blurMax"`
	Pixel         bool   `json:"pixel"`
	PixelMax      int    `json:"pixelMax"`
	Ring          bool   `json:"ring"`
	RingColor     string `json:"ringColor"`
	Badge         bool   `json:"badge"`
	BadgePosition string `json:"badgePosition"`
}
type Crop struct {
	Mode string `json:"mode"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
	Zoom int    `json:"zoom"`
}
type Settings struct {
	Mode      string  `json:"mode"`
	StartDate string  `json:"startDate"`
	EndDate   string  `json:"endDate"`
	Timezone  string  `json:"timezone"`
	Crop      Crop    `json:"crop"`
	Effects   Effects `json:"effects"`
}
type Token struct {
	Access    string    `json:"access"`
	Refresh   string    `json:"refresh,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	UserID    string    `json:"userId"`
	Team      string    `json:"team"`
}
type Attempt struct {
	Date    string    `json:"date"`
	At      time.Time `json:"at"`
	Success bool      `json:"success"`
	Error   string    `json:"error,omitempty"`
	Manual  bool      `json:"manual"`
}
type State struct {
	Settings    Settings  `json:"settings"`
	Token       Token     `json:"token"`
	HasImage    bool      `json:"hasImage"`
	Attempts    []Attempt `json:"attempts"`
	OAuthState  string    `json:"oauthState,omitempty"`
	OAuthExpiry time.Time `json:"oauthExpiry,omitempty"`
}

func defaultSettings() Settings {
	return Settings{Mode: "local", Timezone: "Asia/Taipei", Crop: Crop{Mode: "fit", X: 50, Y: 50, Zoom: 100}, Effects: Effects{Gray: true, BlurMax: 8, PixelMax: 16, RingColor: "#36c5f0", BadgePosition: "bottom-right"}}
}
func (s Settings) mode() string {
	if s.Mode == "" { // Saved settings from before modes existed used Slack.
		return "slack"
	}
	return s.Mode
}
func parseDate(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }
func (s Settings) validate() error {
	if s.mode() != "local" && s.mode() != "slack" {
		return errors.New("無效的使用模式")
	}
	start, e := parseDate(s.StartDate)
	if e != nil {
		return errors.New("開始日格式須為 YYYY-MM-DD")
	}
	end, e := parseDate(s.EndDate)
	if e != nil {
		return errors.New("離職日格式須為 YYYY-MM-DD")
	}
	if end.Before(start) {
		return errors.New("離職日不可早於開始日")
	}
	if _, e = time.LoadLocation(s.Timezone); e != nil {
		return errors.New("無效的時區")
	}
	if s.Crop.Mode != "" && s.Crop.Mode != "fit" && s.Crop.Mode != "crop" {
		return errors.New("無效的裁切方式")
	}
	if s.Crop.Mode == "crop" && (s.Crop.X < 0 || s.Crop.X > 100 || s.Crop.Y < 0 || s.Crop.Y > 100 || s.Crop.Zoom < 100 || s.Crop.Zoom > 300) {
		return errors.New("裁切位置須為 0–100，縮放須為 100–300%")
	}
	if s.Effects.BlurMax < 0 || s.Effects.BlurMax > 20 {
		return errors.New("最大模糊強度須為 0–20")
	}
	if s.Effects.PixelMax < 2 || s.Effects.PixelMax > 64 {
		return errors.New("最大方塊大小須為 2–64")
	}
	if _, e = parseColor(s.Effects.RingColor); e != nil {
		return errors.New("進度條顏色須為 #RRGGBB")
	}
	switch s.Effects.BadgePosition {
	case "top-left", "top-right", "bottom-left", "bottom-right":
	default:
		return errors.New("無效的角標位置")
	}
	return nil
}
func dayInfo(s Settings, date string) (float64, int, error) {
	start, e := parseDate(s.StartDate)
	if e != nil {
		return 0, 0, e
	}
	end, e := parseDate(s.EndDate)
	if e != nil {
		return 0, 0, e
	}
	d, e := parseDate(date)
	if e != nil {
		return 0, 0, errors.New("預覽日期格式須為 YYYY-MM-DD")
	}
	total := int(end.Sub(start).Hours() / 24)
	elapsed := int(d.Sub(start).Hours() / 24)
	remaining := int(end.Sub(d).Hours() / 24)
	if remaining < 0 {
		remaining = 0
	}
	if total == 0 {
		if d.Before(start) {
			return 0, remaining, nil
		}
		return 1, remaining, nil
	}
	p := float64(elapsed) / float64(total)
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	return p, remaining, nil
}
func today(s Settings, now time.Time) (string, error) {
	loc, e := time.LoadLocation(s.Timezone)
	if e != nil {
		return "", e
	}
	return now.In(loc).Format("2006-01-02"), nil
}
func active(s Settings, date string) bool {
	return s.StartDate != "" && date >= s.StartDate && date <= s.EndDate
}
func validateImageSize(w, h int) error {
	if w < 1 || h < 1 || w > 8000 || h > 8000 || int64(w)*int64(h) > 20_000_000 {
		return fmt.Errorf("圖片尺寸不可超過 8000×8000 或 2000 萬像素")
	}
	return nil
}
func hasScope(scope string) bool {
	for _, v := range strings.Split(scope, ",") {
		if strings.TrimSpace(v) == "users.profile:write" {
			return true
		}
	}
	return false
}
