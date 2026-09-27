package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRelTime(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, loc)

	tests := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "now"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h"},
		{23 * time.Hour, "23h"},
		{50 * time.Hour, "2d"},
		{6 * 24 * time.Hour, "6d"},
		{8 * 24 * time.Hour, "2 Mar"},
		{400 * 24 * time.Hour, "3 Feb 2025"},
	}
	for _, tt := range tests {
		if got := relTime(now.Add(-tt.ago), now, loc); got != tt.want {
			t.Errorf("%v ago = %q, want %q", tt.ago, got, tt.want)
		}
	}
}

func TestRelTimeFuture(t *testing.T) {
	now := time.Now()
	if got := relTime(now.Add(time.Hour), now, time.UTC); got != "now" {
		t.Errorf("future = %q, want %q", got, "now")
	}
}

func TestLocationFromCookie(t *testing.T) {
	h := NewHandler(Config{DefaultTZ: "America/New_York"})

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "tz", Value: "Europe/Lisbon"})
	if got := h.location(r).String(); got != "Europe/Lisbon" {
		t.Errorf("cookie tz = %q, want Europe/Lisbon", got)
	}

	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "tz", Value: "Not/AZone"})
	if got := h.location(r).String(); got != "America/New_York" {
		t.Errorf("bad cookie tz = %q, want the default", got)
	}

	if got := h.location(httptest.NewRequest("GET", "/", nil)).String(); got != "America/New_York" {
		t.Errorf("no cookie = %q, want the default", got)
	}
}
