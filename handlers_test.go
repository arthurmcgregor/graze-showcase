package main

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// firstID is the article the fake serves from stream/items/contents.
func firstID(t *testing.T) string {
	t.Helper()
	return fixtureStream(t, "stream_unread.json").Articles[0].ID
}

func testHandler(t *testing.T, f *fakeFreshRSS) *Handler {
	t.Helper()
	return NewHandler(Config{
		APIURL:      f.URL,
		APIUser:     "admin",
		APIPassword: "correct-horse",
		FreshRSSURL: "https://rss.example.test",
		DefaultTZ:   "America/New_York",
	})
}

func get(t *testing.T, h *Handler, path string, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Remote-User", "reader")
	if hx {
		r.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	registerRoutes(mux, h)
	mux.ServeHTTP(w, r)
	return w
}

func post(t *testing.T, h *Handler, path string, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, nil)
	r.Header.Set("Remote-User", "reader")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hx {
		r.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	registerRoutes(mux, h)
	mux.ServeHTTP(w, r)
	return w
}

// fixtureCategories returns FreshRSS's categories in its own order, with the
// unread count it reports for each.
func fixtureCategories(t *testing.T) (names []string, counts map[string]int) {
	t.Helper()
	var subs struct {
		Subscriptions []subscription `json:"subscriptions"`
	}
	loadFixture(t, "subscription_list.json", &subs)
	var uc struct {
		Counts []struct {
			ID    string `json:"id"`
			Count int    `json:"count"`
		} `json:"unreadcounts"`
	}
	loadFixture(t, "unread_count.json", &uc)

	counts = map[string]int{}
	for _, c := range uc.Counts {
		counts[c.ID] = c.Count
	}
	seen := map[string]bool{}
	for _, s := range subs.Subscriptions {
		if name := s.feed().Category; name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, counts
}

func TestListShowsEveryCategoryWithAllPinnedFirst(t *testing.T) {
	f := newFakeFreshRSS(t)
	body := get(t, testHandler(t, f), "/", false).Body.String()

	labels, counts := fixtureCategories(t)
	if len(labels) < 2 {
		t.Fatal("need at least two categories in the fixture")
	}

	at := strings.Index(body, ">All<")
	if at < 0 {
		t.Fatal("All is missing from the chip strip")
	}
	var quiet int
	for _, label := range labels {
		i := strings.Index(body, ">"+html.EscapeString(label)+"<")
		if i < 0 {
			t.Errorf("chip strip missing %q", label)
			continue
		}
		if i < at {
			t.Errorf("All must be pinned first, but %q comes before it", label)
		}
		at = i
		// A category with nothing unread is greyed, never dropped.
		if counts["user/-/label/"+label] == 0 {
			quiet++
			chip := body[max(0, i-200):i]
			if !strings.Contains(chip, "chip-quiet") {
				t.Errorf("%q has nothing unread and should be greyed", label)
			}
		}
	}
	if quiet == 0 {
		t.Log("no zero-unread category in the fixture; the greying is unexercised")
	}
}

func fixtureStream(t *testing.T, name string) Page {
	t.Helper()
	var raw streamPage
	loadFixture(t, name, &raw)
	if len(raw.Items) == 0 {
		t.Fatalf("%s has no items", name)
	}
	return raw.page()
}

func TestListRendersRows(t *testing.T) {
	f := newFakeFreshRSS(t)
	body := get(t, testHandler(t, f), "/", false).Body.String()

	for _, a := range fixtureStream(t, "stream_unread.json").Articles {
		if !strings.Contains(body, `href="/a/`+a.ID) {
			t.Errorf("%s: no row linking to the article", a.ID)
		}
		if !strings.Contains(body, `href="/f/`+a.FeedID+`"`) {
			t.Errorf("%s: feed chip should link to /f/%s", a.ID, a.FeedID)
		}
	}
	if strings.Contains(body, "row row-read") {
		t.Error("the unread stream should produce no dimmed rows")
	}
}

// News rows carry the feed's excerpt, and the video feeds' their thumbnail.
func TestRowsCarryAnExcerptAndALeadImage(t *testing.T) {
	f := newFakeFreshRSS(t)
	body := get(t, testHandler(t, f), "/", false).Body.String()

	var excerpts, images int
	for _, a := range fixtureStream(t, "stream_unread.json").Articles {
		excerpt, image := preview(a.Body, a.Title)
		if excerpt != "" {
			excerpts++
			if !strings.Contains(body, `<p class="row-excerpt">`+html.EscapeString(excerpt)+`</p>`) {
				t.Errorf("%s: no excerpt %q", a.ID, excerpt)
			}
		}
		if image != "" {
			images++
			if !strings.Contains(body, `<img class="row-image" src="`+html.EscapeString(image)+`" alt="" loading="lazy" referrerpolicy="no-referrer">`) {
				t.Errorf("%s: no lead image %q", a.ID, image)
			}
		}
	}
	if excerpts == 0 || images == 0 {
		t.Fatalf("%d excerpts and %d images in the fixture; this proves nothing", excerpts, images)
	}
}

// Starred shows read articles too, and those are the ones that dim in place.
func TestReadRowsDimInPlace(t *testing.T) {
	f := newFakeFreshRSS(t)
	body := get(t, testHandler(t, f), "/starred", false).Body.String()

	var read int
	for _, a := range fixtureStream(t, "stream_starred.json").Articles {
		if !a.Read {
			continue
		}
		read++
		i := strings.Index(body, `id="row-`+a.ID+`"`)
		if i < 0 {
			t.Errorf("%s: missing from the starred list", a.ID)
			continue
		}
		if !strings.Contains(body[max(0, i-60):i], "row-read") {
			t.Errorf("%s: read row is not dimmed", a.ID)
		}
	}
	if read == 0 {
		t.Fatal("no read articles in the starred fixture")
	}
}

func TestListOffersTheNextPageOnlyWhenThereIsOne(t *testing.T) {
	f := newFakeFreshRSS(t)
	f.stream = "stream_paged.json" // captured with n=2, so FreshRSS filled it
	body := get(t, testHandler(t, f), "/", false).Body.String()

	if !strings.Contains(body, `hx-trigger="intersect once"`) {
		t.Error("a full page should end in a scroll sentinel")
	}
	if want := fixtureStream(t, "stream_paged.json").Continuation; !strings.Contains(body, "c="+want) {
		t.Errorf("the sentinel should carry %q verbatim", want)
	}

	f.stream = "stream_unread.json" // a partial page, so no continuation
	if body := get(t, testHandler(t, f), "/", false).Body.String(); strings.Contains(body, "intersect once") {
		t.Error("a partial page should not offer another")
	}
}

func TestContinuationReturnsRowsOnly(t *testing.T) {
	f := newFakeFreshRSS(t)
	body := get(t, testHandler(t, f), "/?c=1772998000000000", true).Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, "nav-bar") {
		t.Error("a continuation request should return rows, not a page")
	}
	if !strings.Contains(body, "row-title") {
		t.Error("expected rows")
	}
}

func TestContinuationAsksOnlyForTheNextPage(t *testing.T) {
	f := newFakeFreshRSS(t)
	get(t, testHandler(t, f), "/?c=1772998000000000", true)
	for _, path := range []string{"/reader/api/0/subscription/list", "/reader/api/0/unread-count"} {
		if n := f.hitsOn(path); n != 0 {
			t.Errorf("a continuation asked %s %d times; it needs only the articles", path, n)
		}
	}
}

func TestAFullPageAlwaysStartsAtTheTop(t *testing.T) {
	f := newFakeFreshRSS(t)
	if w := get(t, testHandler(t, f), "/?c=1772998000000000", false); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	q, err := url.ParseQuery(f.streamQuery())
	if err != nil || !q.Has("n") {
		t.Fatalf("the articles were not asked for: %q", f.streamQuery())
	}
	if q.Has("c") {
		t.Errorf("a full page should ignore c, but asked for %q", f.streamQuery())
	}
}

// Going back from an article must not show a list from before it was read.
func TestGoingBackRefetchesTheList(t *testing.T) {
	f := newFakeFreshRSS(t)
	h := testHandler(t, f)
	for _, path := range []string{"/", "/starred", "/c/Tech", "/f/1"} {
		if cc := get(t, h, path, false).Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store", path, cc)
		}
	}
	if cc := get(t, h, "/?c=1772998000000000", true).Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("continuation: Cache-Control %q, want no-store", cc)
	}
	// Some browsers keep a no-store page in the back/forward cache anyway.
	if body := get(t, h, "/", false).Body.String(); !strings.Contains(body, "e.persisted") {
		t.Error("a list restored from the back/forward cache should reload")
	}
}

func TestContinuationKeepsItsList(t *testing.T) {
	f := newFakeFreshRSS(t)
	f.stream = "stream_paged.json"
	body := get(t, testHandler(t, f), "/f/97?c=1772998000000000", true).Body.String()
	if want := `hx-get="/f/97?c=` + fixtureStream(t, "stream_paged.json").Continuation + `"`; !strings.Contains(body, want) {
		t.Errorf("the next sentinel should stay on the feed, want %s", want)
	}
}

func TestStarredIsNotFilteredToUnread(t *testing.T) {
	f := newFakeFreshRSS(t)
	w := get(t, testHandler(t, f), "/starred", false)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if strings.Contains(f.streamQuery(), "xt=") {
		t.Errorf("starred should not exclude read articles; query was %q", f.streamQuery())
	}
}

func TestOpeningAnArticleMarksItRead(t *testing.T) {
	f := newFakeFreshRSS(t)
	w := get(t, testHandler(t, f), "/a/"+firstID(t)+"?from=%2F", false)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(f.body(), "a=user%2F-%2Fstate%2Fcom.google%2Fread") {
		t.Errorf("opening should mark read; last write was %q", f.body())
	}
	body := w.Body.String()
	if !strings.Contains(body, "article-body") {
		t.Error("expected the article body")
	}
	if !strings.Contains(body, `href="/"`) {
		t.Error("expected a back link to the list it came from")
	}
	original := fixtureStream(t, "stream_unread.json").Articles[0].Original
	host := strings.TrimPrefix(mustURL(t, original).Hostname(), "www.")
	if !strings.Contains(body, "Read on "+host) {
		t.Errorf("expected the read-the-original footer for %s", host)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSwipeReturnsTheRowInItsNewState(t *testing.T) {
	f := newFakeFreshRSS(t)
	w := post(t, testHandler(t, f), "/a/"+firstID(t)+"/unread", true)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(f.body(), "r=user%2F-%2Fstate%2Fcom.google%2Fread") {
		t.Errorf("expected the read state to be removed; last write was %q", f.body())
	}
	body := w.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), "<li") {
		t.Errorf("expected a row fragment, got %q", truncate(body, 80))
	}
	if !strings.Contains(body, `id="chips"`) {
		t.Error("expected the chips too, so their counts follow the row")
	}
}

// A row's buttons carry the list it sits in, so the row that comes back from a
// swipe still opens its article with Back returning to that list.
func TestSwipeKeepsTheRowInItsList(t *testing.T) {
	f := newFakeFreshRSS(t)
	h := testHandler(t, f)
	id := firstID(t)

	action := "/a/" + id + "/unread?from=%2Fc%2FTech"
	if body := get(t, h, "/c/Tech", false).Body.String(); !strings.Contains(body, `hx-post="`+action+`"`) {
		t.Fatalf("the row's unread button should post %q", action)
	}
	row := post(t, h, action, true).Body.String()
	if want := `href="/a/` + id + `?from=%2Fc%2FTech"`; !strings.Contains(row, want) {
		t.Errorf("the swiped row should still link with %s", want)
	}
}

// The browser hides a row marked read in an unread list, so there is no row to
// send back, only the chips. Starred shows read articles too, so there the row
// comes back dimmed.
func TestMarkingReadInAnUnreadListSendsNoRow(t *testing.T) {
	f := newFakeFreshRSS(t)
	h := testHandler(t, f)
	id := firstID(t)

	w := post(t, h, "/a/"+id+"/read?from=%2Fc%2FTech", true)
	if got := w.Header().Get("HX-Reswap"); got != "none" {
		t.Errorf("HX-Reswap %q, want none", got)
	}
	if body := strings.TrimSpace(w.Body.String()); !strings.HasPrefix(body, `<nav class="chips"`) {
		t.Errorf("expected only the chips, got %q", truncate(body, 80))
	}
	if !strings.Contains(f.body(), "a=user%2F-%2Fstate%2Fcom.google%2Fread") {
		t.Errorf("expected the read state to be added; last write was %q", f.body())
	}
	row := post(t, h, "/a/"+id+"/read?from=%2Fstarred", true).Body.String()
	if !strings.HasPrefix(strings.TrimSpace(row), "<li") {
		t.Errorf("starred should get the row back, got %q", truncate(row, 80))
	}
}

func TestMarkAllReadAppliesToTheCurrentCategory(t *testing.T) {
	f := newFakeFreshRSS(t)
	w := post(t, testHandler(t, f), "/mark-all-read?from=%2Fc%2FTech", false)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/c/Tech" {
		t.Errorf("redirected to %q", got)
	}
	if !strings.Contains(f.body(), "s=user%2F-%2Flabel%2FTech") {
		t.Errorf("last write was %q", f.body())
	}
}

// Starred has nothing to catch up on, whatever the form claims.
func TestMarkAllReadLeavesStarredAlone(t *testing.T) {
	f := newFakeFreshRSS(t)
	w := post(t, testHandler(t, f), "/mark-all-read?from=%2Fstarred", false)
	if got := w.Header().Get("Location"); got != "/starred" {
		t.Errorf("redirected to %q", got)
	}
	if f.body() != "" {
		t.Errorf("starred should not be marked read; last write was %q", f.body())
	}
}

func TestUnreachableFreshRSSRendersTheErrorState(t *testing.T) {
	f := newFakeFreshRSS(t)
	h := testHandler(t, f)
	f.Close() // the backend goes away mid-session

	w := get(t, h, "/", false)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "FreshRSS did not reply.") {
		t.Error("expected the error state, not an empty list")
	}
	if !strings.Contains(body, "https://rss.example.test") {
		t.Error("the error state should offer FreshRSS directly")
	}
}

func TestUnauthenticatedIsRejected(t *testing.T) {
	f := newFakeFreshRSS(t)
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	registerRoutes(mux, testHandler(t, f))
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", w.Code)
	}
}

// Escaping from= twice made it %252F, which does not parse as a list,
// silently sending every Back to All.
func TestArticleReturnsToTheListItCameFrom(t *testing.T) {
	f := newFakeFreshRSS(t)
	h := testHandler(t, f)

	id := firstID(t)
	body := get(t, h, "/a/"+id+"?from=%2Fc%2FTech", false).Body.String()
	if !strings.Contains(body, `href="/c/Tech"`) {
		t.Error("back link should point at the category")
	}

	want := `action="/a/` + id + `/unread?from=%2Fc%2FTech"`
	if !strings.Contains(body, want) {
		t.Errorf("mark-unread form should carry the list once-escaped; body has %q",
			betweenAction(body))
	}

	w := post(t, h, "/a/"+id+"/unread?from=%2fc%2fTech", false)
	if got := w.Header().Get("Location"); got != "/c/Tech" {
		t.Errorf("redirected to %q, want /c/Tech", got)
	}
}

func betweenAction(body string) string {
	i := strings.Index(body, `action="`)
	if i < 0 {
		return ""
	}
	return body[i : i+70]
}

// The subscription cache and the client's two tokens are the only mutable
// state graze has, and every request touches all three.
// The chip counts and the rows cannot disagree, so neither is shown without the
// other.
func TestOneFailedFetchIsTheErrorPage(t *testing.T) {
	for _, path := range []string{
		"/reader/api/0/subscription/list",
		"/reader/api/0/unread-count",
		"/reader/api/0/stream/contents/user/-/state/com.google/reading-list",
	} {
		f := newFakeFreshRSS(t)
		f.fail(path)
		w := get(t, testHandler(t, f), "/", false)
		if w.Code != http.StatusBadGateway {
			t.Errorf("%s failing: status %d, want 502", path, w.Code)
		}
		if strings.Contains(w.Body.String(), `class="row-title"`) {
			t.Errorf("%s failing: rows rendered anyway", path)
		}
	}
}

func TestConcurrentReadersShareTheCacheSafely(t *testing.T) {
	f := newFakeFreshRSS(t)
	h := testHandler(t, f)
	mux := http.NewServeMux()
	registerRoutes(mux, h)

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Remote-User", "reader")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("status %d", w.Code)
			}
		})
	}
	wg.Wait()
}

// A category's name is its identity: FreshRSS never escapes it, so the chip
// shows it and links by it as it came.
func TestChipsNameAndLinkACategoryAsFreshRSSDoes(t *testing.T) {
	chips := chipsFor([]Feed{{ID: "1", Category: "Rock & Roll"}}, Unread{}, List{})
	if len(chips) != 2 {
		t.Fatalf("got %d chips, want All plus one category", len(chips))
	}
	if got := chips[1].List.Title(); got != "Rock & Roll" {
		t.Errorf("label = %q", got)
	}
	if got := chips[1].List.Path(); got != "/c/Rock%20&%20Roll" {
		t.Errorf("href = %q", got)
	}
}

func TestAllIsNotActiveOnAFeedPage(t *testing.T) {
	chips := chipsFor(nil, Unread{}, parseList("/f/1"))
	if chips[0].Active {
		t.Error("All should not claim to be the current list on a feed page")
	}
}

func TestEveryResponseCarriesANoncedPolicy(t *testing.T) {
	f := newFakeFreshRSS(t)
	cfg := Config{APIURL: f.URL, APIUser: "admin", APIPassword: "correct-horse", DefaultTZ: "UTC"}
	srv := newServer(cfg, NewHandler(cfg))

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Remote-User", "reader")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	csp := w.Header().Get("Content-Security-Policy")
	nonce, _, ok := strings.Cut(strings.TrimPrefix(
		csp[strings.Index(csp, "'nonce-"):], "'nonce-"), "'")
	if !ok || nonce == "" {
		t.Fatalf("no nonce in %q", csp)
	}
	if !strings.Contains(w.Body.String(), `nonce="`+nonce+`"`) {
		t.Errorf("the layout's scripts should carry the nonce the header names (%q)", nonce)
	}
	if strings.ContainsAny(nonce, "+/=") {
		t.Errorf("nonce %q must survive attribute escaping", nonce)
	}
	for _, want := range []string{"frame-src https://www.youtube.com", "base-uri 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy missing %q", want)
		}
	}
	if strings.Contains(w.Body.String(), "onclick=") {
		t.Error("an inline handler would be blocked by the policy")
	}
}

func TestFailedSwipeAsksTheBrowserToReload(t *testing.T) {
	f := newFakeFreshRSS(t)
	h := testHandler(t, f)
	f.Close()

	w := post(t, h, "/a/"+firstID(t)+"/read", true)
	if w.Header().Get("HX-Refresh") != "true" {
		t.Errorf("want HX-Refresh so the reader lands on the error state; headers %v", w.Header())
	}
}

// Gatus probes this on the container port, so it must answer with no
// Remote-User header and without reaching FreshRSS — that is what keeps a
// graze outage distinguishable from a FreshRSS one.
func TestHealthAnswersWithoutAuthOrBackend(t *testing.T) {
	f := newFakeFreshRSS(t)
	h := testHandler(t, f)
	f.Close()

	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	registerRoutes(mux, h)
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "ok" {
		t.Errorf("body = %q", w.Body.String())
	}
}
