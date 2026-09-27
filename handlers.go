package main

import (
	"context"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const pageSize = 50

type Handler struct {
	cfg      Config
	api      *Client
	tmpls    map[string]*template.Template
	partials *template.Template
	subs     subCache
}

func NewHandler(cfg Config) *Handler {
	return &Handler{
		cfg:      cfg,
		api:      NewClient(cfg),
		tmpls:    parseTemplates(),
		partials: parsePartials(),
		subs:     subCache{ttl: 5 * time.Minute},
	}
}

// subCache holds the feeds, which change only when feeds are added or moved in
// FreshRSS. Unread counts are never cached — they are the thing the
// reader is checking.
type subCache struct {
	ttl    time.Duration
	mu     sync.Mutex
	at     time.Time
	loaded bool
	val    []Feed
}

func (c *subCache) get(ctx context.Context, load func(context.Context) ([]Feed, error)) ([]Feed, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && time.Since(c.at) < c.ttl {
		return c.val, nil
	}
	feeds, err := load(ctx)
	if err != nil {
		return nil, err
	}
	c.val, c.at, c.loaded = feeds, time.Now(), true
	return feeds, nil
}

type state struct{ Eyebrow, Title, Sub string }

func (h *Handler) feeds(ctx context.Context) ([]Feed, error) {
	return h.subs.get(ctx, h.api.Feeds)
}

// --- view models ---

type Chip struct {
	List   List
	Count  int
	Active bool
}

type Row struct {
	ID         string
	Title      string
	Feed       string
	FeedHref   string
	When       string
	ISO        string
	Href       string
	ReadHref   string
	UnreadHref string
	Read       bool
	Hides      bool // marking it read takes it out of the list
	Excerpt    string
	LeadImage  string
}

// chrome is what the layout needs from every page. List is the list the page
// shows, which the avatar menu's mark-all-read applies to, and nil on a page
// that shows none.
type chrome struct {
	Title       string
	User        string
	FreshRSSURL string
	List        *List
	Nonce       string
}

func (h *Handler) chromeFor(r *http.Request, title string, l *List) chrome {
	return chrome{Title: title, User: user(r), FreshRSSURL: h.cfg.FreshRSSURL, List: l, Nonce: nonceOf(r)}
}

type listPage struct {
	chrome
	Chips []Chip
	Rows  []Row
	More  string // continuation, empty when this is the last page
	Error *state
}

type articlePage struct {
	chrome
	From    List
	Article ArticleView
}

type ArticleView struct {
	Row
	Author   string
	Body     template.HTML
	Original string
	Starred  bool
}

// --- handlers ---

// List answers a list page, or with c from the scroll sentinel, the next page of
// its rows. A full page always starts at the top.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	// Otherwise going back from an article shows it as unread.
	w.Header().Set("Cache-Control", "no-store")
	var c string
	if isHTMX(r) {
		c = r.URL.Query().Get("c")
	}
	data, err := h.fetchList(r, parseList(r.URL.EscapedPath()), c)
	if err != nil {
		h.renderError(w, r, err)
		return
	}
	if c != "" {
		h.renderPartial(w, "rows.html", data)
		return
	}
	h.render(w, "list.html", data)
}

// fetchList fetches what a page of l shows. A continuation shows only rows, so it
// fetches neither the feeds nor the unread counts.
func (h *Handler) fetchList(r *http.Request, l List, c string) (listPage, error) {
	ctx := r.Context()
	var (
		wg     sync.WaitGroup
		feeds  []Feed
		unread Unread
		page   Page
		errs   [3]error
	)
	if c == "" {
		wg.Go(func() { feeds, errs[0] = h.feeds(ctx) })
		wg.Go(func() { unread, errs[1] = h.api.UnreadCounts(ctx) })
	}
	page, errs[2] = h.api.Stream(ctx, l, StreamOpts{Count: pageSize, Continuation: c})
	wg.Wait()
	if err := errors.Join(errs[:]...); err != nil {
		return listPage{}, err
	}
	var chips []Chip
	if c == "" {
		l = l.titled(feeds)
		chips = chipsFor(feeds, unread, l)
	}
	return listPage{
		chrome: h.chromeFor(r, l.Title(), &l),
		Chips:  chips,
		Rows:   h.rowsFor(r, page.Articles, l),
		More:   page.Continuation,
	}, nil
}

func (h *Handler) Article(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	articles, err := h.api.Articles(ctx, r.PathValue("id"))
	if err != nil {
		h.renderError(w, r, err)
		return
	}
	if len(articles) == 0 {
		http.NotFound(w, r)
		return
	}
	a := articles[0]

	if !a.Read {
		if err := h.api.SetRead(ctx, a.ID, true); err != nil {
			log.Printf("marking %s read: %v", a.ID, err)
		}
	}

	from := fromList(r)
	h.render(w, "article.html", articlePage{
		chrome: h.chromeFor(r, a.Title, nil),
		From:   from,
		Article: ArticleView{
			Row:      h.rowFor(r, a, from),
			Author:   a.Author,
			Body:     rewriteContent(a.Body, h.cfg.EmbedHosts),
			Original: a.Original,
			Starred:  a.Starred,
		},
	})
}

// SetRead answers a row's button with the row in its new state and the chips
// with their new counts. A row marked read in an unread list has already been
// hidden by the browser, which offers Undo instead, so only the chips go back.
func (h *Handler) SetRead(w http.ResponseWriter, r *http.Request) {
	read := strings.HasSuffix(r.URL.Path, "/read")
	id := r.PathValue("id")
	if err := h.api.SetRead(r.Context(), id, read); err != nil {
		h.renderError(w, r, err)
		return
	}
	l := fromList(r)
	if !isHTMX(r) {
		http.Redirect(w, r, l.Path(), http.StatusSeeOther)
		return
	}
	if read && l.unreadOnly() {
		w.Header().Set("HX-Reswap", "none")
	} else {
		articles, err := h.api.Articles(r.Context(), id)
		if err != nil {
			h.renderError(w, r, err)
			return
		}
		if len(articles) == 0 {
			http.NotFound(w, r)
			return
		}
		h.renderPartial(w, "row.html", h.rowFor(r, articles[0], l))
	}
	h.renderChips(w, r, l)
}

func (h *Handler) SetStarred(w http.ResponseWriter, r *http.Request) {
	starred := strings.HasSuffix(r.URL.Path, "/star")
	if err := h.api.SetStarred(r.Context(), r.PathValue("id"), starred); err != nil {
		h.renderError(w, r, err)
		return
	}
	if !isHTMX(r) {
		http.Redirect(w, r, fromList(r).Path(), http.StatusSeeOther)
		return
	}
	h.renderPartial(w, "star.html", ArticleView{
		Row:     Row{ID: r.PathValue("id")},
		Starred: starred,
	})
}

func (h *Handler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	l := fromList(r)
	if l.CatchUp() {
		if err := h.api.MarkAllRead(r.Context(), l); err != nil {
			h.renderError(w, r, err)
			return
		}
	}
	http.Redirect(w, r, l.Path(), http.StatusSeeOther)
}

// --- assembly ---

// chipsFor builds the strip: All pinned first, then every category in FreshRSS's
// own order. A category with nothing unread is greyed rather than dropped — a
// strip that reorders itself between visits destroys muscle memory, and one that
// hides empty categories makes "did I read it, or did it break?" unanswerable.
func chipsFor(feeds []Feed, unread Unread, current List) []Chip {
	chips := []Chip{{Count: unread.Of(List{}), Active: current.Path() == "/"}}
	seen := map[string]bool{}
	for _, f := range feeds {
		if f.Category == "" || seen[f.Category] {
			continue
		}
		seen[f.Category] = true
		l := categoryList(f.Category)
		chips = append(chips, Chip{List: l, Count: unread.Of(l), Active: l.Path() == current.Path()})
	}
	return chips
}

// renderChips appends the chips to a row's answer, for htmx to swap in out of
// band. The mark has already gone through, so a failure here leaves the old
// counts rather than telling the reader it did not.
func (h *Handler) renderChips(w http.ResponseWriter, r *http.Request, l List) {
	feeds, err := h.feeds(r.Context())
	var unread Unread
	if err == nil {
		unread, err = h.api.UnreadCounts(r.Context())
	}
	if err != nil {
		log.Printf("%s %s: chips: %v", r.Method, r.URL.Path, err)
		return
	}
	h.renderPartial(w, "chips.html", listPage{Chips: chipsFor(feeds, unread, l)})
}

func (h *Handler) rowsFor(r *http.Request, articles []Article, l List) []Row {
	loc, now := h.location(r), time.Now()
	rows := make([]Row, 0, len(articles))
	for _, a := range articles {
		rows = append(rows, row(a, l, loc, now))
	}
	return rows
}

func (h *Handler) rowFor(r *http.Request, a Article, l List) Row {
	return row(a, l, h.location(r), time.Now())
}

func row(a Article, l List, loc *time.Location, now time.Time) Row {
	excerpt, image := preview(a.Body, a.Title)
	return Row{
		ID:         a.ID,
		Title:      a.Title,
		Feed:       a.FeedTitle,
		FeedHref:   feedList(a.FeedID).Path(),
		When:       relTime(a.Published, now, loc),
		ISO:        a.Published.In(loc).Format(time.RFC3339),
		Read:       a.Read,
		Hides:      l.unreadOnly(),
		Href:       l.ArticleHref(a.ID),
		ReadHref:   l.ActionHref(a.ID, "read"),
		UnreadHref: l.ActionHref(a.ID, "unread"),
		Excerpt:    excerpt,
		LeadImage:  image,
	}
}

// --- rendering ---

func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	if !errors.Is(err, ErrUnreachable) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	// A fragment has nowhere to put this. Reloading lands the reader on the
	// error state below, which says what failed.
	if isHTMX(r) {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	// A reader that renders an empty list when the backend is down is
	// indistinguishable from one with nothing to read.
	w.WriteHeader(http.StatusBadGateway)
	h.render(w, "list.html", listPage{
		chrome: h.chromeFor(r, "Offline", nil),
		Error: &state{
			"No answer", "FreshRSS did not reply.",
			"Graze holds nothing of its own, so there is nothing to show until it does. Retry, or open FreshRSS directly to see whether it is up.",
		},
	})
}

func funcs() template.FuncMap {
	return template.FuncMap{
		"dict": func(pairs ...any) map[string]any {
			m := make(map[string]any)
			for i := 0; i < len(pairs)-1; i += 2 {
				m[pairs[i].(string)] = pairs[i+1]
			}
			return m
		},
		"icon": func(name string) template.HTML {
			return template.HTML(`<svg class="icon" aria-hidden="true"><use href="/static/icons.svg#` + name + `"/></svg>`)
		},
		"host": func(raw string) string {
			if u, err := url.Parse(raw); err == nil {
				return strings.TrimPrefix(u.Hostname(), "www.")
			}
			return raw
		},
		// slice would cut a multi-byte name mid-rune.
		"initial": func(s string) string {
			for _, r := range s {
				return string(r)
			}
			return "?"
		},
	}
}

func parseTemplates() map[string]*template.Template {
	funcMap := funcs()

	base := template.Must(parsePartials().Funcs(funcMap).Clone())
	template.Must(base.ParseFiles("templates/layout.html"))

	pages := []string{"list.html", "article.html"}
	tmpls := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		tmpls[page] = template.Must(template.Must(base.Clone()).ParseFiles("templates/" + page))
	}
	return tmpls
}

func parsePartials() *template.Template {
	return template.Must(template.New("partials").Funcs(funcs()).ParseGlob("templates/partials/*.html"))
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	t, ok := h.tmpls[name]
	if !ok {
		log.Printf("template not found: %s", name)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (h *Handler) renderPartial(w http.ResponseWriter, name string, data any) {
	if err := h.partials.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

// --- request helpers ---

func user(r *http.Request) string { return r.Header.Get("Remote-User") }

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func fromList(r *http.Request) List { return parseList(r.FormValue("from")) }

func orDefault(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if user(r) == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
