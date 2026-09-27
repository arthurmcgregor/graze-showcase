package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type Config struct {
	Port        string
	FreshRSSURL string
	APIURL      string
	APIUser     string
	APIPassword string
	DefaultTZ   string
	// EmbedHosts are PeerTube instances whose /videos/embed/ iframes may be
	// rendered. YouTube is always allowed; nothing else is.
	EmbedHosts []string
}

func loadConfig() Config {
	return Config{
		Port:        env("PORT", "8080"),
		FreshRSSURL: os.Getenv("FRESHRSS_WEB_URL"),
		APIURL:      env("FRESHRSS_API_URL", "http://freshrss:80/api/greader.php"),
		APIUser:     env("FRESHRSS_API_USER", ""),
		APIPassword: os.Getenv("FRESHRSS_API_PASSWORD"),
		DefaultTZ:   env("GRAZE_DEFAULT_TZ", "UTC"),
		EmbedHosts:  splitHosts(os.Getenv("GRAZE_EMBED_HOSTS")),
	}
}

func splitHosts(s string) []string {
	var hosts []string
	for _, h := range strings.Split(s, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg := loadConfig()
	h := NewHandler(cfg)

	addr := ":" + cfg.Port
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, newServer(cfg, h)))
}

func newServer(cfg Config, h *Handler) http.Handler {
	mux := http.NewServeMux()
	registerRoutes(mux, h)
	return requestLogger(devAuth(contentPolicy(cfg, mux)))
}

func registerRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /{$}", requireUser(h.List))
	mux.HandleFunc("GET /starred", requireUser(h.List))
	mux.HandleFunc("GET /c/{name...}", requireUser(h.List))
	mux.HandleFunc("GET /f/{id}", requireUser(h.List))
	mux.HandleFunc("GET /a/{id}", requireUser(h.Article))
	mux.HandleFunc("POST /a/{id}/read", requireUser(h.SetRead))
	mux.HandleFunc("POST /a/{id}/unread", requireUser(h.SetRead))
	mux.HandleFunc("POST /a/{id}/star", requireUser(h.SetStarred))
	mux.HandleFunc("POST /a/{id}/unstar", requireUser(h.SetStarred))
	mux.HandleFunc("POST /mark-all-read", requireUser(h.MarkAllRead))
	mux.Handle("GET /static/", http.StripPrefix("/static/", noCacheFileServer("static")))
}

// health answers without touching FreshRSS and without an authenticated user,
// so an outage here stays distinguishable from an outage there. It is probed on
// the container port, behind no proxy.
func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if sw.status == 0 {
		sw.status = 200
	}
	return sw.ResponseWriter.Write(b)
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}

		defer func() {
			if err := recover(); err != nil {
				log.Printf("PANIC %s %s — %v", r.Method, r.URL.Path, err)
				http.Error(sw, "internal server error", 500)
			}
		}()

		next.ServeHTTP(sw, r)

		if strings.HasPrefix(r.URL.Path, "/static/") {
			return
		}
		user := r.Header.Get("Remote-User")
		if user == "" {
			user = "-"
		}
		log.Printf("%s %s %d %s user=%s", r.Method, r.URL.Path, sw.status, formatDuration(time.Since(start)), user)
	})
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

type nonceKey struct{}

// contentPolicy issues a per-request nonce and names it in the CSP, so the
// layout's own scripts run and anything a feed manages to inject does not.
// Article HTML is rewritten before it is rendered, but that rewrite reparses
// and reserialises markup, which is not a value-preserving operation; this is
// the second lock on that door.
func contentPolicy(cfg Config, next http.Handler) http.Handler {
	frameSrc := "https://www.youtube.com https://www.youtube-nocookie.com"
	for _, h := range cfg.EmbedHosts {
		frameSrc += " https://" + h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		// URL alphabet, because html/template escapes a + in an attribute to
		// &#43; and the browser would then see a different nonce than the
		// header names — blocking the page's own scripts, half the time.
		nonce := base64.RawURLEncoding.EncodeToString(raw[:])
		w.Header().Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"script-src 'self' https://unpkg.com 'nonce-" + nonce + "'",
			"style-src 'self' 'unsafe-inline'",
			"img-src * data:",
			"media-src *",
			"frame-src " + frameSrc,
			"base-uri 'none'",
			"form-action 'self'",
			"frame-ancestors 'none'",
		}, "; "))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nonceKey{}, nonce)))
	})
}

func nonceOf(r *http.Request) string {
	n, _ := r.Context().Value(nonceKey{}).(string)
	return n
}

func noCacheFileServer(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("CDN-Cache-Control", "no-store")
		fs.ServeHTTP(w, r)
	})
}

func devAuth(next http.Handler) http.Handler {
	user := os.Getenv("DEV_USER")
	if user == "" {
		return next
	}
	log.Printf("dev auth enabled: user=%s", user)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Remote-User") == "" {
			r.Header.Set("Remote-User", user)
		}
		next.ServeHTTP(w, r)
	})
}
