package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stream ids, as the GReader protocol spells them.
const (
	streamReadingList = "user/-/state/com.google/reading-list"
	stateRead         = "user/-/state/com.google/read"
	stateStarred      = "user/-/state/com.google/starred"
	labelPrefix       = "user/-/label/"
	feedPrefix        = "feed/"
	itemPrefix        = "tag:google.com,2005:reader/item/"
)

// streamOf is the stream a list reads from. A category's is its name exactly as
// FreshRSS reports it, which it never escapes.
func streamOf(l List) string {
	switch l.kind {
	case category:
		return labelPrefix + l.ref
	case feed:
		return feedPrefix + l.ref
	case starred:
		return stateStarred
	}
	return streamReadingList
}

// ErrUnreachable is what every caller gets when FreshRSS cannot be reached or
// refuses the credentials. Handlers turn it into the error state, which says
// what failed rather than rendering an empty list.
var ErrUnreachable = errors.New("freshrss unreachable")

// errRejected is the 401 FreshRSS returns for a stale auth token and for a
// stale edit token alike — it does not distinguish them — raised only after a
// re-login has already been tried.
var errRejected = fmt.Errorf("%w: credentials rejected", ErrUnreachable)

type Client struct {
	http *http.Client
	base string
	user string
	pass string

	mu        sync.Mutex
	auth      string // "<user>/<token>", for the Authorization header
	editToken string
}

func NewClient(cfg Config) *Client {
	return &Client{
		http: &http.Client{Timeout: 20 * time.Second},
		base: strings.TrimSuffix(cfg.APIURL, "/"),
		user: cfg.APIUser,
		pass: cfg.APIPassword,
	}
}

// --- what the client returns ---

type Feed struct {
	ID       string // the numeric tail of its stream id
	Title    string
	Category string // "" when FreshRSS reports none
}

type Article struct {
	ID        string // the hex tail of the item id, which is what graze's URLs carry
	Title     string
	Author    string
	FeedID    string
	FeedTitle string
	Published time.Time
	Read      bool
	Starred   bool
	Original  string // the publisher's own page
	// Body is the HTML as FreshRSS sent it, for rewriteContent. It is never
	// unescaped: restoring < and > inside markup would manufacture tags.
	Body string
}

type Page struct {
	Articles     []Article
	Continuation string // empty when this is the last page
}

// Unread holds the unread count of every list that has one.
type Unread struct{ counts map[string]int }

func (u Unread) Of(l List) int { return u.counts[streamOf(l)] }

// --- protocol types ---

type subscription struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Categories []struct {
		ID string `json:"id"`
	} `json:"categories"`
}

func (s subscription) feed() Feed {
	f := Feed{ID: strings.TrimPrefix(s.ID, feedPrefix), Title: unescapeFeedTitle(s.Title)}
	// Only subscription/list can say what a feed belongs to: an item's own
	// categories[] mixes GReader states, the feed's category, user labels in
	// the identical shape, and raw feed tags with no prefix.
	for _, c := range s.Categories {
		if name, ok := strings.CutPrefix(c.ID, labelPrefix); ok {
			f.Category = name
			break
		}
	}
	return f
}

type link struct {
	Href string `json:"href"`
}

type item struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// The API is hard-wired to compat mode, so the body is under summary,
	// never content, and there is no updated field.
	Summary struct {
		Content string `json:"content"`
	} `json:"summary"`
	Published  int64    `json:"published"`
	Author     string   `json:"author"`
	Categories []string `json:"categories"`
	Canonical  []link   `json:"canonical"`
	Alternate  []link   `json:"alternate"`
	Origin     struct {
		StreamID string `json:"streamId"`
		Title    string `json:"title"`
	} `json:"origin"`
}

func (i item) article() Article {
	return Article{
		ID:        strings.TrimPrefix(i.ID, itemPrefix),
		Title:     unescapeText(i.Title),
		Author:    unescapeText(i.Author),
		FeedID:    strings.TrimPrefix(i.Origin.StreamID, feedPrefix),
		FeedTitle: unescapeFeedTitle(i.Origin.Title),
		Published: time.Unix(i.Published, 0),
		Read:      slices.Contains(i.Categories, stateRead),
		Starred:   slices.Contains(i.Categories, stateStarred),
		Original:  i.original(),
		Body:      i.Summary.Content,
	}
}

func (i item) original() string {
	for _, links := range [][]link{i.Canonical, i.Alternate} {
		for _, l := range links {
			if l.Href != "" {
				return l.Href
			}
		}
	}
	return ""
}

type streamPage struct {
	Items        []item `json:"items"`
	Continuation string `json:"continuation"`
}

func (p streamPage) page() Page {
	out := Page{Articles: make([]Article, 0, len(p.Items)), Continuation: p.Continuation}
	for _, i := range p.Items {
		out.Articles = append(out.Articles, i.article())
	}
	return out
}

// --- calls ---

func (c *Client) Feeds(ctx context.Context) ([]Feed, error) {
	var out struct {
		Subscriptions []subscription `json:"subscriptions"`
	}
	if err := c.getJSON(ctx, "/reader/api/0/subscription/list", url.Values{"output": {"json"}}, &out); err != nil {
		return nil, err
	}
	feeds := make([]Feed, 0, len(out.Subscriptions))
	for _, s := range out.Subscriptions {
		feeds = append(feeds, s.feed())
	}
	return feeds, nil
}

// UnreadCounts is keyed by stream id — feeds, categories, labels and the
// reading-list total all appear in the same list.
func (c *Client) UnreadCounts(ctx context.Context) (Unread, error) {
	var out struct {
		Counts []struct {
			ID    string `json:"id"`
			Count int    `json:"count"`
		} `json:"unreadcounts"`
	}
	if err := c.getJSON(ctx, "/reader/api/0/unread-count", url.Values{"output": {"json"}}, &out); err != nil {
		return Unread{}, err
	}
	counts := make(map[string]int, len(out.Counts))
	for _, u := range out.Counts {
		counts[u.ID] = u.Count
	}
	return Unread{counts}, nil
}

type StreamOpts struct {
	Count        int
	Continuation string // the last item id of the previous page
}

func (c *Client) Stream(ctx context.Context, l List, opts StreamOpts) (Page, error) {
	q := url.Values{"output": {"json"}}
	if opts.Count > 0 {
		q.Set("n", strconv.Itoa(opts.Count))
	}
	if opts.Continuation != "" {
		q.Set("c", opts.Continuation)
	}
	if l.unreadOnly() {
		q.Set("xt", stateRead)
	}
	var page streamPage
	err := c.getJSON(ctx, "/reader/api/0/stream/contents/"+escapePath(streamOf(l)), q, &page)
	return page.page(), err
}

// escapePath escapes each segment of a stream id. A category name reaches here
// straight from the request path and can hold a # or a ?, which would otherwise
// truncate the URL and take output=json with it.
func escapePath(streamID string) string {
	parts := strings.Split(streamID, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// Articles fetches specific articles by id. The response carries no continuation.
func (c *Client) Articles(ctx context.Context, ids ...string) ([]Article, error) {
	form := url.Values{"output": {"json"}}
	for _, id := range ids {
		form.Add("i", itemPrefix+id)
	}
	body, err := c.do(ctx, func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/reader/api/0/stream/items/contents", strings.NewReader(form.Encode()))
		if err == nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return r, err
	})
	if err != nil {
		return nil, err
	}
	var page streamPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("%w: decoding items: %w", ErrUnreachable, err)
	}
	return page.page().Articles, nil
}

func (c *Client) SetRead(ctx context.Context, id string, read bool) error {
	return c.editTag(ctx, id, stateRead, read)
}

func (c *Client) SetStarred(ctx context.Context, id string, starred bool) error {
	return c.editTag(ctx, id, stateStarred, starred)
}

func (c *Client) editTag(ctx context.Context, id, state string, add bool) error {
	form := url.Values{"i": {itemPrefix + id}}
	if add {
		form.Set("a", state)
	} else {
		form.Set("r", state)
	}
	return c.post(ctx, "/reader/api/0/edit-tag", form)
}

func (c *Client) MarkAllRead(ctx context.Context, l List) error {
	return c.post(ctx, "/reader/api/0/mark-all-as-read", url.Values{
		"s":  {streamOf(l)},
		"ts": {strconv.FormatInt(time.Now().UnixMicro(), 10)},
	})
}

// --- transport ---

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	body, err := c.do(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: decoding %s: %w", ErrUnreachable, path, err)
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, form url.Values) error {
	token, err := c.edit(ctx, false)
	if err != nil {
		return err
	}
	send := func() ([]byte, error) {
		form.Set("T", token)
		return c.do(ctx, func() (*http.Request, error) {
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
			if err == nil {
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			return r, err
		})
	}
	body, err := send()
	// A rejected write is the one case worth a second try: FreshRSS answers a
	// stale edit token with the same 401 it uses for a stale auth token, so a
	// re-login alone does not fix it.
	if errors.Is(err, errRejected) {
		if token, err = c.edit(ctx, true); err != nil {
			return err
		}
		body, err = send()
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(string(body)), "OK") {
		return fmt.Errorf("%w: %s rejected: %s", ErrUnreachable, path, truncate(string(body), 120))
	}
	return nil
}

// do sends the request with the cached auth token, logging in first if there is
// none and once more if the server rejects the one it has.
func (c *Client) do(ctx context.Context, build func() (*http.Request, error)) ([]byte, error) {
	auth, err := c.authorize(ctx, false)
	if err != nil {
		return nil, err
	}
	body, status, err := c.send(build, auth)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		if auth, err = c.authorize(ctx, true); err != nil {
			return nil, err
		}
		body, status, err = c.send(build, auth)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, errRejected
		}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d: %s", ErrUnreachable, status, truncate(string(body), 120))
	}
	return body, nil
}

func (c *Client) send(build func() (*http.Request, error), auth string) ([]byte, int, error) {
	req, err := build()
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "GoogleLogin auth="+auth)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return body, resp.StatusCode, nil
}

func (c *Client) authorize(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	cached := c.auth
	c.mu.Unlock()
	if cached != "" && !force {
		return cached, nil
	}

	form := url.Values{"Email": {c.user}, "Passwd": {c.pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/accounts/ClientLogin", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: login: %w", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: login rejected: status %d", ErrUnreachable, resp.StatusCode)
	}
	auth := fieldValue(string(body), "Auth")
	if auth == "" {
		return "", fmt.Errorf("%w: login response carried no Auth", ErrUnreachable)
	}

	c.mu.Lock()
	c.auth, c.editToken = auth, ""
	c.mu.Unlock()
	return auth, nil
}

// edit returns the write token, which is separate from the auth token and has
// to be asked for.
func (c *Client) edit(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	cached := c.editToken
	c.mu.Unlock()
	if cached != "" && !force {
		return cached, nil
	}
	body, err := c.do(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/reader/api/0/token", nil)
	})
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("%w: empty edit token", ErrUnreachable)
	}
	c.mu.Lock()
	c.editToken = token
	c.mu.Unlock()
	return token, nil
}

// fieldValue reads one line of ClientLogin's "key=value" body.
func fieldValue(body, key string) string {
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// FreshRSS serialises for the API through escapeToUnicodeAlternative, which
// swaps some characters for fullwidth look-alikes and cannot be turned off, so
// "Rock & Roll" arrives as "Rock ＆ Roll". Feed titles get the extended set,
// article titles and authors only & < and >, and category names nothing. The
// extended set is meant to include ' " and ^ as well, but FreshRSS merges the
// two arrays by key and loses them. Reversing each set is right for everything
// but a fullwidth character the publisher wrote in the same position.
var (
	unescapeText      = strings.NewReplacer("＆", "&", "＜", "<", "＞", ">").Replace
	unescapeFeedTitle = strings.NewReplacer(
		"＆", "&", "＜", "<", "＞", ">",
		"？", "?", "＼", "\\", "／", "/", "，", ",", "；", ";",
	).Replace
)
