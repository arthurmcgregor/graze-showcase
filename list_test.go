package main

import "testing"

func TestParseList(t *testing.T) {
	for _, tc := range []struct {
		path, wantPath, wantStream, wantTitle string
		catchUp                               bool
	}{
		{"/", "/", streamReadingList, "All", true},
		{"/starred", "/starred", stateStarred, "Starred", false},
		{"/c/Tech", "/c/Tech", "user/-/label/Tech", "Tech", true},
		{"/c/Rock%20&%20Roll", "/c/Rock%20&%20Roll", "user/-/label/Rock & Roll", "Rock & Roll", true},
		{"/c/a%2Fb", "/c/a%2Fb", "user/-/label/a/b", "a/b", true},
		{"/c/50%25%20off%3F", "/c/50%25%20off%3F", "user/-/label/50% off?", "50% off?", true},
		{"/f/42", "/f/42", "feed/42", "This feed", true},

		// Anything else is All, which is what stops from= being an open redirect.
		{"", "/", streamReadingList, "All", true},
		{"https://evil.test", "/", streamReadingList, "All", true},
		{"//evil.test", "/", streamReadingList, "All", true},
		{`/\evil.test`, "/", streamReadingList, "All", true},
		{"/c/", "/", streamReadingList, "All", true},
		{"/c/%zz", "/", streamReadingList, "All", true},
		{"/f/1%2F2", "/", streamReadingList, "All", true},
		{"/a/00065be1c7caf617", "/", streamReadingList, "All", true},
	} {
		l := parseList(tc.path)
		if l.Path() != tc.wantPath || streamOf(l) != tc.wantStream || l.Title() != tc.wantTitle || l.CatchUp() != tc.catchUp {
			t.Errorf("parseList(%q) = %q %q %q %v, want %q %q %q %v", tc.path,
				l.Path(), streamOf(l), l.Title(), l.CatchUp(),
				tc.wantPath, tc.wantStream, tc.wantTitle, tc.catchUp)
		}
	}
}

func TestAFeedIsTitledFromTheSubscriptionList(t *testing.T) {
	feeds := []Feed{{ID: "42", Title: "Rock & Roll Weekly"}}
	if got := parseList("/f/42").titled(feeds).Title(); got != "Rock & Roll Weekly" {
		t.Errorf("title = %q", got)
	}
}
