package main

import (
	"net/url"
	"strings"
)

// List is what an article is being read from: All, a category, a feed, or
// starred. The zero value is All.
type List struct {
	kind      listKind
	ref       string // a category's name or a feed's id
	feedTitle string
}

type listKind int

const (
	all listKind = iota
	category
	feed
	starred
)

// parseList reads a list from an escaped path, and is the only thing that
// does. Anything that is not one of the four shapes is All, which is also what
// keeps a from= taken off a query string from redirecting to another host.
func parseList(path string) List {
	rest, ok := strings.CutPrefix(path, "/c/")
	if name, err := url.PathUnescape(rest); ok && err == nil && name != "" {
		return categoryList(name)
	}
	rest, ok = strings.CutPrefix(path, "/f/")
	if id, err := url.PathUnescape(rest); ok && err == nil && id != "" && !strings.Contains(id, "/") {
		return feedList(id)
	}
	if path == "/starred" {
		return List{kind: starred}
	}
	return List{}
}

func categoryList(name string) List { return List{kind: category, ref: name} }

func feedList(id string) List { return List{kind: feed, ref: id} }

// titled names a feed, which only the subscription list knows.
func (l List) titled(feeds []Feed) List {
	if l.kind != feed {
		return l
	}
	for _, f := range feeds {
		if f.ID == l.ref {
			l.feedTitle = f.Title
		}
	}
	return l
}

func (l List) Title() string {
	switch l.kind {
	case category:
		return l.ref
	case feed:
		return orDefault(l.feedTitle, "This feed")
	case starred:
		return "Starred"
	}
	return "All"
}

func (l List) Path() string {
	switch l.kind {
	case category:
		return "/c/" + url.PathEscape(l.ref)
	case feed:
		return "/f/" + url.PathEscape(l.ref)
	case starred:
		return "/starred"
	}
	return "/"
}

func (l List) Starred() bool { return l.kind == starred }

// Starred shows read articles too, and there is nothing in it to catch up on.
func (l List) unreadOnly() bool { return !l.Starred() }

func (l List) CatchUp() bool { return !l.Starred() }

func (l List) ArticleHref(id string) string { return "/a/" + id + l.from() }

func (l List) ActionHref(id, verb string) string { return "/a/" + id + "/" + verb + l.from() }

func (l List) CatchUpHref() string { return "/mark-all-read" + l.from() }

func (l List) from() string { return "?from=" + url.QueryEscape(l.Path()) }

func (l List) Empty() state {
	title := l.Title()
	switch l.kind {
	case category:
		return state{"All caught up", "Nothing left in " + title + ".", "Other categories may still have something. FreshRSS fetches at two and thirty-two past the hour."}
	case feed:
		return state{"All caught up", "Nothing left from " + title + ".", "Its next articles arrive when FreshRSS next fetches, at two and thirty-two past the hour."}
	case starred:
		return state{"Nothing kept", "No starred articles.", "Star an article to keep it here. Starring does not copy it — if the feed drops it, it is gone."}
	}
	return state{"All caught up", "Nothing left to read.", "Every feed is caught up. FreshRSS fetches at two and thirty-two past the hour."}
}
