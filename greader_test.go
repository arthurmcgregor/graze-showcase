package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeFreshRSS serves the captured fixtures and records what it was asked.
type fakeFreshRSS struct {
	*httptest.Server
	editToken string
	// stream is the fixture served for stream/contents; the starred stream
	// always gets its own.
	stream string

	mu              sync.Mutex
	logins          int
	lastBody        string
	lastStreamQuery string
	lastStreamPath  string
	rejectOnce      bool
	hits            map[string]int // by path
	failing         map[string]bool
}

func (f *fakeFreshRSS) record(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeFreshRSS) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastBody
}

func (f *fakeFreshRSS) streamQuery() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastStreamQuery
}

func (f *fakeFreshRSS) streamPath() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastStreamPath
}

func (f *fakeFreshRSS) hitsOn(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

// fail makes every request to path answer 500 from now on.
func (f *fakeFreshRSS) fail(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing[path] = true
}

func (f *fakeFreshRSS) failed(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failing[path]
}

func (f *fakeFreshRSS) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func (f *fakeFreshRSS) reject() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectOnce = true
}

// taking reports whether this request is the one to reject, and disarms.
func (f *fakeFreshRSS) taking() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejectOnce {
		f.rejectOnce = false
		return true
	}
	return false
}

func newFakeFreshRSS(t *testing.T) *fakeFreshRSS {
	t.Helper()
	f := &fakeFreshRSS{editToken: "EDITTOKEN" + strings.Repeat("Z", 48), stream: "stream_unread.json", hits: map[string]int{}, failing: map[string]bool{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/accounts/ClientLogin", func(w http.ResponseWriter, r *http.Request) {
		f.record(func() { f.logins++ })
		if err := r.ParseForm(); err != nil || r.PostForm.Get("Passwd") != "correct-horse" {
			http.Error(w, "Unauthorized!", http.StatusUnauthorized)
			return
		}
		u := r.PostForm.Get("Email")
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		io.WriteString(w, "SID="+u+"/tok\nLSID=null\nAuth="+u+"/tok\n")
	})

	guard := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.record(func() { f.hits[r.URL.Path]++ })
			if f.failed(r.URL.Path) {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if f.taking() {
				w.Header().Set("Google-Bad-Token", "true")
				http.Error(w, "Unauthorized!", http.StatusUnauthorized)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "GoogleLogin auth=") {
				http.Error(w, "Unauthorized!", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}

	serve := func(fixture string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, err := os.ReadFile("testdata/" + fixture)
			if err != nil {
				t.Errorf("fixture %s: %v", fixture, err)
				http.Error(w, "missing fixture", 500)
				return
			}
			w.Write(b)
		}
	}

	mux.HandleFunc("/reader/api/0/token", guard(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, f.editToken+"\n")
	}))
	mux.HandleFunc("/reader/api/0/subscription/list", guard(serve("subscription_list.json")))
	mux.HandleFunc("/reader/api/0/unread-count", guard(serve("unread_count.json")))
	mux.HandleFunc("/reader/api/0/stream/contents/", guard(func(w http.ResponseWriter, r *http.Request) {
		f.record(func() { f.lastStreamQuery, f.lastStreamPath = r.URL.RawQuery, r.URL.EscapedPath() })
		if strings.HasSuffix(r.URL.Path, "/starred") {
			serve("stream_starred.json")(w, r)
			return
		}
		serve(f.stream)(w, r)
	}))
	mux.HandleFunc("/reader/api/0/stream/items/contents", guard(func(w http.ResponseWriter, r *http.Request) {
		serve(f.stream)(w, r)
	}))

	write := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.record(func() { f.lastBody = string(body) })
		form, _ := url.ParseQuery(string(body))
		if form.Get("T") != f.editToken {
			http.Error(w, "Unauthorized!", http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "OK")
	}
	mux.HandleFunc("/reader/api/0/edit-tag", guard(write))
	mux.HandleFunc("/reader/api/0/mark-all-as-read", guard(write))

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeFreshRSS) client() *Client {
	return NewClient(Config{APIURL: f.URL, APIUser: "admin", APIPassword: "correct-horse"})
}

func loadFixture(t *testing.T, name string, out any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// The category capture.sh names, whose name carries an & and whose feeds' titles
// carry a ?.
const escapedCategory = "Another & Ampersand"

func TestFeedsCarryTheirCategory(t *testing.T) {
	f := newFakeFreshRSS(t)
	feeds, err := f.client().Feeds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) == 0 {
		t.Fatal("no feeds in the fixture")
	}
	for _, feed := range feeds {
		if feed.ID == "" || strings.Contains(feed.ID, "/") || feed.Title == "" {
			t.Errorf("%+v: want a numeric id and a title", feed)
		}
		// FreshRSS puts a feed in exactly one category, and it is the only
		// place the category can be read from.
		if feed.Category == "" || strings.HasPrefix(feed.Category, "user/") {
			t.Errorf("%s: category = %q", feed.Title, feed.Category)
		}
	}
}

// FreshRSS escapes feed titles to fullwidth look-alikes and leaves category
// names alone, so the adapter reverses one and must not touch the other.
func TestFeedTitlesAreUnescapedAndCategoriesArrivePlain(t *testing.T) {
	var raw struct {
		Subscriptions []subscription `json:"subscriptions"`
	}
	loadFixture(t, "subscription_list.json", &raw)
	var escaped int
	for _, s := range raw.Subscriptions {
		if strings.ContainsAny(s.Title, "＆＜＞？＼／，；") {
			escaped++
		}
	}
	if escaped == 0 {
		t.Fatal("no escaped feed title in the fixture; recapture with capture.sh")
	}

	f := newFakeFreshRSS(t)
	feeds, err := f.client().Feeds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var inCategory int
	for _, feed := range feeds {
		if strings.ContainsAny(feed.Title, "＆＜＞？＼／，；") {
			t.Errorf("feed title %q is still escaped", feed.Title)
		}
		if feed.Category == escapedCategory {
			inCategory++
		}
	}
	if inCategory == 0 {
		t.Errorf("no feed in %q", escapedCategory)
	}
}

func TestUnreadCountsAnswerForEveryList(t *testing.T) {
	f := newFakeFreshRSS(t)
	c := f.client()
	unread, err := c.UnreadCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var raw struct {
		Max    int `json:"max"`
		Counts []struct {
			ID    string `json:"id"`
			Count int    `json:"count"`
		} `json:"unreadcounts"`
	}
	loadFixture(t, "unread_count.json", &raw)
	if got := unread.Of(List{}); got != raw.Max {
		t.Errorf("All = %d, want max (%d)", got, raw.Max)
	}
	for _, u := range raw.Counts {
		if name, ok := strings.CutPrefix(u.ID, "user/-/label/"); ok && unread.Of(categoryList(name)) != u.Count {
			t.Errorf("%s = %d, want %d", name, unread.Of(categoryList(name)), u.Count)
		}
	}
}

func TestStreamArticleShape(t *testing.T) {
	f := newFakeFreshRSS(t)
	page, err := f.client().Stream(context.Background(), List{}, StreamOpts{Count: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Articles) == 0 {
		t.Fatal("no articles in the fixture")
	}
	if !strings.Contains(f.streamQuery(), "xt=") {
		t.Errorf("All is unread only; query was %q", f.streamQuery())
	}

	for _, a := range page.Articles {
		// The API is hard-wired to compat mode: the body is under summary,
		// there is no content field and no updated field.
		if a.Body == "" {
			t.Errorf("%s: empty body", a.ID)
		}
		if len(a.ID) != 16 {
			t.Errorf("ID = %q, want the 16-char hex tail", a.ID)
		}
		if a.FeedID == "" || strings.Contains(a.FeedID, "/") || a.FeedTitle == "" {
			t.Errorf("%s: feed = %q %q", a.ID, a.FeedID, a.FeedTitle)
		}
		if a.Original == "" {
			t.Errorf("%s: no canonical or alternate link", a.ID)
		}
		if a.Published.IsZero() {
			t.Errorf("%s: no published time", a.ID)
		}
		// This stream was captured with xt=read.
		if a.Read {
			t.Errorf("%s: unread stream carries a read article", a.ID)
		}
	}
}

// A category's stream id is its name as FreshRSS reports it, and the feed
// titles on its articles come escaped like any other.
func TestCategoryStreamIsAskedForByName(t *testing.T) {
	f := newFakeFreshRSS(t)
	f.stream = "stream_category.json"
	page, err := f.client().Stream(context.Background(), categoryList(escapedCategory), StreamOpts{Count: 20})
	if err != nil {
		t.Fatal(err)
	}
	if want := "/user/-/label/Another%20&%20Ampersand"; !strings.HasSuffix(f.streamPath(), want) {
		t.Errorf("path = %q, want it to end %q", f.streamPath(), want)
	}
	if len(page.Articles) == 0 {
		t.Fatal("no articles in the fixture")
	}
	for _, a := range page.Articles {
		if strings.ContainsAny(a.FeedTitle, "＆＜＞？＼／，；") {
			t.Errorf("%s: feed title %q is still escaped", a.ID, a.FeedTitle)
		}
	}
}

// An article's own title and author are escaped only for & < and >, so the rest
// of the fullwidth set is the publisher's punctuation and has to survive.
func TestArticleTitlesKeepTheirOwnFullwidthPunctuation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ClientLogin") {
			io.WriteString(w, "Auth=admin/tok\n")
			return
		}
		io.WriteString(w, `{"items":[{"id":"tag:google.com,2005:reader/item/0006","title":"本当？ Fish ＆ Chips","author":"A ＜B＞；C","origin":{"streamId":"feed/1","title":"Rock ＆ Roll？"}}]}`)
	}))
	defer srv.Close()

	page, err := NewClient(Config{APIURL: srv.URL}).Stream(context.Background(), List{}, StreamOpts{})
	if err != nil {
		t.Fatal(err)
	}
	a := page.Articles[0]
	if a.Title != "本当？ Fish & Chips" || a.Author != "A <B>；C" || a.FeedTitle != "Rock & Roll?" {
		t.Errorf("got %q / %q / %q", a.Title, a.Author, a.FeedTitle)
	}
}

func TestStarredStreamDistinguishesReadFromUnread(t *testing.T) {
	f := newFakeFreshRSS(t)
	page, err := f.client().Stream(context.Background(), parseList("/starred"), StreamOpts{Count: 20})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.streamQuery(), "xt=") {
		t.Errorf("starred shows read articles too; query was %q", f.streamQuery())
	}
	var read int
	for _, a := range page.Articles {
		if !a.Starred {
			t.Errorf("%s: in the starred stream but not starred", a.ID)
		}
		if a.Read {
			read++
		}
	}
	// Unread is the absence of the read tag, so a fixture with none of each
	// would prove nothing either way.
	if read == 0 || read == len(page.Articles) {
		t.Errorf("%d of %d starred articles are read; want a mix", read, len(page.Articles))
	}
}

// FreshRSS emits a continuation only when it filled the page, which is how a
// client knows to stop asking.
func TestContinuationOnlyWhenThePageIsFull(t *testing.T) {
	var full, partial streamPage
	loadFixture(t, "stream_paged.json", &full)
	loadFixture(t, "stream_unread.json", &partial)

	if full.Continuation == "" {
		t.Error("a full page should carry a continuation")
	}
	if partial.Continuation != "" {
		t.Errorf("a partial page should not: got %q", partial.Continuation)
	}
}

func TestStreamPassesPagingAndUnreadFilter(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ClientLogin") {
			io.WriteString(w, "Auth=admin/tok\n")
			return
		}
		got = r.URL.Query()
		io.WriteString(w, `{"items":[]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{APIURL: srv.URL, APIUser: "admin"})
	_, err := c.Stream(context.Background(), categoryList("Tech"), StreamOpts{Count: 50, Continuation: "123"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("n") != "50" || got.Get("c") != "123" || got.Get("xt") != "user/-/state/com.google/read" {
		t.Errorf("query = %v", got)
	}
}

func TestEditTagSendsIdAndStateInTheBody(t *testing.T) {
	f := newFakeFreshRSS(t)
	if err := f.client().SetStarred(context.Background(), "0006145e8b2c1f40", true); err != nil {
		t.Fatal(err)
	}
	// FreshRSS reads i, a and r off the raw body, so they must be posted there.
	form, err := url.ParseQuery(f.body())
	if err != nil {
		t.Fatal(err)
	}
	if want := "tag:google.com,2005:reader/item/0006145e8b2c1f40"; form.Get("i") != want {
		t.Errorf("i = %q, want %q", form.Get("i"), want)
	}
	if form.Get("a") != "user/-/state/com.google/starred" || form.Has("r") {
		t.Errorf("form = %v", form)
	}
}

func TestUnstarRemovesRatherThanAdds(t *testing.T) {
	f := newFakeFreshRSS(t)
	if err := f.client().SetStarred(context.Background(), "abc", false); err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(f.body())
	if form.Get("r") != "user/-/state/com.google/starred" || form.Has("a") {
		t.Errorf("form = %v", form)
	}
}

func TestMarkAllReadSendsMicrosecondTimestamp(t *testing.T) {
	f := newFakeFreshRSS(t)
	if err := f.client().MarkAllRead(context.Background(), categoryList(escapedCategory)); err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(f.body())
	if form.Get("s") != "user/-/label/"+escapedCategory {
		t.Errorf("s = %q", form.Get("s"))
	}
	// ts is compared against entry ids, which are 16-digit microseconds.
	if len(form.Get("ts")) != 16 {
		t.Errorf("ts = %q, want 16 digits of microseconds", form.Get("ts"))
	}
}

func TestTokenIsReusedAcrossCalls(t *testing.T) {
	f := newFakeFreshRSS(t)
	c := f.client()
	for range 3 {
		if _, err := c.Feeds(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.loginCount() != 1 {
		t.Errorf("logged in %d times, want 1", f.loginCount())
	}
}

func TestRejectedTokenTriggersOneRelogin(t *testing.T) {
	f := newFakeFreshRSS(t)
	c := f.client()
	if _, err := c.Feeds(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.reject()
	if _, err := c.Feeds(context.Background()); err != nil {
		t.Fatalf("a rejected token should be replaced, not surfaced: %v", err)
	}
	if f.loginCount() != 2 {
		t.Errorf("logged in %d times, want 2", f.loginCount())
	}
}

func TestBadPasswordIsUnreachable(t *testing.T) {
	f := newFakeFreshRSS(t)
	c := NewClient(Config{APIURL: f.URL, APIUser: "admin", APIPassword: "wrong"})
	_, err := c.Feeds(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), ErrUnreachable.Error()) {
		t.Errorf("err = %v, want it to wrap ErrUnreachable", err)
	}
}

func TestStreamIdsAreEscapedIntoThePath(t *testing.T) {
	var path, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ClientLogin") {
			io.WriteString(w, "Auth=admin/tok\n")
			return
		}
		path, query = r.URL.EscapedPath(), r.URL.RawQuery
		io.WriteString(w, `{"items":[]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{APIURL: srv.URL, APIUser: "admin"})
	// A category name can hold anything; a raw # would truncate the URL and
	// take output=json with it.
	if _, err := c.Stream(context.Background(), categoryList("a#b?c"), StreamOpts{Count: 50}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/user/-/label/a%23b%3Fc") {
		t.Errorf("path = %q", path)
	}
	if !strings.Contains(query, "output=json") || !strings.Contains(query, "n=50") {
		t.Errorf("query = %q", query)
	}
}
