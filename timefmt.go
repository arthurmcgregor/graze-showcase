package main

import (
	"fmt"
	"net/http"
	"time"
)

// location resolves the reader's timezone: the tz cookie the layout sets from
// the browser, falling back to the configured default.
func (h *Handler) location(r *http.Request) *time.Location {
	if c, err := r.Cookie("tz"); err == nil {
		if loc, err := time.LoadLocation(c.Value); err == nil {
			return loc
		}
	}
	loc, err := time.LoadLocation(h.cfg.DefaultTZ)
	if err != nil {
		return time.UTC
	}
	return loc
}

// relTime is the list's timestamp: relative while an article is still recent
// enough for "3h" to mean something, absolute once it is not.
func relTime(t, now time.Time, loc *time.Location) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	t = t.In(loc)
	if t.Year() == now.In(loc).Year() {
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 2006")
}
