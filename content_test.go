package main

import (
	"html"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRewriteContent(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		notWant []string
	}{
		{
			name: "comic breaks out and surfaces the title as a caption",
			in:   `<img src="https://comics.example.test/a.png" title="The joke lives here." alt="Panel">`,
			want: []string{
				`<figure class="comic">`,
				`<figcaption>The joke lives here.</figcaption>`,
			},
		},
		{
			name: "an illustrated post is not a comic",
			in:   `<p>` + strings.Repeat("Words about the thing. ", 20) + `</p><img src="https://example.test/a.png" title="Fig 1">`,
			notWant: []string{
				`<figure class="comic">`,
				`<figcaption>`,
			},
		},
		{
			name: "comic with no title gets no empty caption",
			in:   `<img src="https://comics.example.test/a.png" alt="Panel">`,
			want: []string{`<figure class="comic">`},
			notWant: []string{
				`<figcaption>`,
			},
		},
		{
			name: "youtube embed is kept, unsized and wrapped",
			in:   `<iframe class="youtube-plugin-video" width="560" height="315" src="https://www.youtube.com/embed/dQw4w9WgXcQ" frameborder="0" allowfullscreen></iframe>`,
			want: []string{
				`<div class="video">`,
				`src="https://www.youtube.com/embed/dQw4w9WgXcQ"`,
				`class="video-frame"`,
			},
			notWant: []string{`width="560"`, `height="315"`},
		},
		{
			name: "a named peertube host is kept",
			in:   `<iframe src="https://video.example.test/videos/embed/abc123"></iframe>`,
			want: []string{`<div class="video">`, `/videos/embed/abc123`},
		},
		{
			name:    "an unnamed host serving the peertube path is dropped",
			in:      `<iframe src="https://evil.test/videos/embed/abc123"></iframe>`,
			notWant: []string{`<iframe`},
		},
		{
			name: "every attribute but the allowlist is dropped from a kept embed",
			in:   `<iframe src="https://www.youtube.com/embed/abc" srcdoc="&lt;script&gt;alert(1)&lt;/script&gt;" onload="alert(2)" sandbox=""></iframe>`,
			want: []string{`sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"`},
			notWant: []string{
				"srcdoc", "onload",
			},
		},
		{
			name: "hostile iframe is dropped",
			in:   `<p>before</p><iframe src="https://evil.test/login"></iframe><p>after</p>`,
			want: []string{`<p>before</p>`, `<p>after</p>`},
			notWant: []string{
				`<iframe`, `evil.test`,
			},
		},
		{
			name:    "an iframe merely mentioning youtube in the host is dropped",
			in:      `<iframe src="https://www.youtube.com.evil.test/embed/x"></iframe>`,
			notWant: []string{`<iframe`},
		},
		{
			name:    "a non-https embed is dropped",
			in:      `<iframe src="http://www.youtube.com/embed/x"></iframe>`,
			notWant: []string{`<iframe`},
		},
		{
			name: "lazy images are promoted",
			in:   `<p>text</p><img data-src="https://example.test/a.png" data-srcset="https://example.test/a2.png 2x" alt="a">`,
			want: []string{
				`src="https://example.test/a.png"`,
				`srcset="https://example.test/a2.png 2x"`,
				`loading="lazy"`,
			},
		},
		{
			// Some publishers answer 403 to any Referer but their own.
			name: "images are fetched without a referrer",
			in:   `<p>text</p><img src="https://example.test/wp-content/uploads/2026/09/crossing.png" alt="" width="800" height="579" data-sanitized-class="aligncenter size-full wp-image-90958">`,
			want: []string{`referrerpolicy="no-referrer"`},
		},
		{
			name:    "only the full text is shown when FreshRSS prepended it to the excerpt",
			in:      `<!-- FULLCONTENT start //--><p>The full text.</p><!-- FULLCONTENT end //-->The excerpt.`,
			want:    []string{`<p>The full text.</p>`},
			notWant: []string{`The excerpt.`},
		},
		{
			name: "a real src is not overwritten by data-src",
			in:   `<p>text</p><img src="https://example.test/real.png" data-src="https://example.test/placeholder.png">`,
			want: []string{`src="https://example.test/real.png"`},
			notWant: []string{
				`<img src="https://example.test/placeholder.png"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(rewriteContent(tt.in, []string{"video.example.test"}))
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q\ngot: %s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q\ngot: %s", w, got)
				}
			}
		})
	}
}

// The pass reparses and reserialises, which is not value-preserving: markup
// that arrives inert inside an attribute can leave as an element, having never
// been seen by FreshRSS's sanitiser in that form.
func TestRewriteContentStripsWhatReparsingCanResurrect(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		notWant []string
	}{
		{
			name:    "noscript attribute breakout",
			in:      `<noscript><p title="</noscript><img src=x onerror=alert(1)>"></noscript>`,
			notWant: []string{"onerror"},
		},
		{"inline handler", `<p onclick="alert(1)">hi</p>`, []string{"onclick"}},
		{"uppercase handler", `<p ONMOUSEOVER="alert(1)">hi</p>`, []string{"alert(1)"}},
		{"javascript href", `<a href="javascript:alert(1)">x</a>`, []string{"javascript:"}},
		{"javascript href with padding", "<a href=\"  \tJaVaScRiPt:alert(1)\">x</a>", []string{"alert(1)"}},
		{"data html src", `<img src="data:text/html;base64,PHNjcmlwdD4=">`, []string{"data:text/html"}},
		{"script element", `<p>a</p><script>alert(1)</script>`, []string{"<script", "alert(1)"}},
		{"form", `<form action="https://evil.test"><input name="p"></form>`, []string{"<form", "evil.test"}},
		{"base", `<base href="https://evil.test/">`, []string{"<base"}},
		{"lazy src smuggling a script url", `<img data-src="javascript:alert(1)">`, []string{"javascript:"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(rewriteContent(tt.in, nil))
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q\ngot: %s", w, got)
				}
			}
		})
	}
}

func TestFullTextIsShownWithoutTheExcerpt(t *testing.T) {
	for _, a := range fixtureStream(t, "stream_fulltext.json").Articles {
		excerpt, _, ok := strings.Cut(a.Body, fullTextStart)
		if !ok {
			t.Fatalf("%s has no full text; recapture it", a.ID)
		}
		got := string(rewriteContent(a.Body, nil))
		if strings.HasPrefix(got, excerpt) {
			t.Errorf("%s: shows the excerpt %q before the full text", a.ID, excerpt)
		}
		if len(got) <= len(excerpt) {
			t.Errorf("%s: shows no full text", a.ID)
		}
	}
}

func TestUnparseableContentIsNeverMarkedSafe(t *testing.T) {
	// The fallbacks are unreachable today, but they are the one place that
	// mints template.HTML, so they escape rather than pass through.
	if got := string(rewriteContent("", nil)); got != "" {
		t.Errorf("empty content = %q", got)
	}
}

// The captured bodies are real feed HTML, including the YouTube extension's
// iframe, which reaches API clients without passing FreshRSS's sanitiser.
func TestRewriteContentOverEveryCapturedBody(t *testing.T) {
	var embeds int
	for _, name := range []string{"stream_unread.json", "stream_starred.json", "stream_category.json", "stream_fulltext.json"} {
		for _, a := range fixtureStream(t, name).Articles {
			got := string(rewriteContent(a.Body, nil))

			for _, banned := range []string{"<script", " onerror=", " onload=", " onclick=", "javascript:"} {
				if strings.Contains(got, banned) {
					t.Errorf("%s %s: output contains %q", name, a.ID, banned)
				}
			}
			for _, iframe := range iframesIn(got) {
				embeds++
				if !strings.Contains(iframe, "youtube.com/embed/") {
					t.Errorf("%s %s: kept a non-embed iframe: %s", name, a.ID, iframe)
				}
				// The extension hard-codes both, and both overflow a phone.
				for _, sized := range []string{"width=", "height=", "style="} {
					if strings.Contains(iframe, sized) {
						t.Errorf("%s %s: iframe kept its %s", name, a.ID, sized)
					}
				}
				if !strings.Contains(iframe, "sandbox=") {
					t.Errorf("%s %s: iframe is not sandboxed", name, a.ID)
				}
			}
		}
	}
	if embeds == 0 {
		t.Fatal("no embeds in the captured bodies; this proves nothing")
	}
	t.Logf("rewrote %d embeds", embeds)
}

func iframesIn(html string) []string {
	var out []string
	for rest := html; ; {
		i := strings.Index(rest, "<iframe")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		j := strings.Index(rest, ">")
		if j < 0 {
			return out
		}
		out = append(out, rest[:j+1])
		rest = rest[j+1:]
	}
}

func TestTheExcerptIsTheFeedsOwnWhenFreshRSSFetchedTheFullText(t *testing.T) {
	for _, a := range fixtureStream(t, "stream_fulltext.json").Articles {
		own, _, _ := strings.Cut(a.Body, fullTextStart)
		excerpt, image := preview(a.Body, a.Title)
		if want := strings.TrimSpace(html.UnescapeString(own)); excerpt != want {
			t.Errorf("%s: excerpt = %q, want the feed's own %q", a.ID, excerpt, want)
		}
		// The feed's excerpt has no image, but the full text does.
		if image == "" {
			t.Errorf("%s: no lead image", a.ID)
		}
	}
}

func TestPreview(t *testing.T) {
	tests := []struct {
		name, raw, title string
		excerpt, image   string
	}{
		{"excerpt after the full text, as prepend leaves it",
			fullTextStart + `<p>The whole story.</p>` + fullTextEnd + `The standfirst.`, "",
			"The standfirst.", ""},
		{"no excerpt beside the full text",
			fullTextStart + `<p>The whole story.</p>` + fullTextEnd, "",
			"The whole story.", ""},
		{"blocks end words, inline elements do not",
			`<p><a href="https://example.test">Harbour</a>'s story</p><p>Next</p>`, "",
			"Harbour's story Next", ""},
		{"captions say nothing on their own",
			`<figure><img src="https://example.test/a.jpg"><figcaption>Photo: Harbour</figcaption></figure><p>The story.</p>`, "",
			"The story.", "https://example.test/a.jpg"},
		{"a repeated title is no excerpt",
			`<p>Same as the title</p>`, "Same as the title",
			"", ""},
		{"a bare link is no excerpt",
			`<a href="https://example.test/post">https://example.test/post</a>`, "",
			"", ""},
		{"images that declare themselves small are passed over",
			`<p>Text.</p><img src="https://example.test/pixel.gif" width="1" height="1"><img data-src="https://example.test/lazy.jpg"><img src="https://example.test/b.jpg">`, "",
			"Text.", "https://example.test/lazy.jpg"},
		{"only https images",
			`<p>Text.</p><img src="http://example.test/a.jpg"><img src="data:image/png;base64,AA">`, "",
			"Text.", ""},
		{"a comic is its image, so has neither",
			`<img src="https://example.test/comic.png" title="The joke"><p>Permanent link</p>`, "",
			"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			excerpt, image := preview(tt.raw, tt.title)
			if excerpt != tt.excerpt {
				t.Errorf("excerpt = %q, want %q", excerpt, tt.excerpt)
			}
			if image != tt.image {
				t.Errorf("image = %q, want %q", image, tt.image)
			}
		})
	}
}

func TestALongExcerptIsCutAtAWord(t *testing.T) {
	excerpt, _ := preview("<p>"+strings.Repeat("word ", 100)+"</p>", "")
	if !strings.HasSuffix(excerpt, "word…") {
		t.Errorf("excerpt ends %q", excerpt[len(excerpt)-10:])
	}
	if n := utf8.RuneCountInString(excerpt); n > excerptRunes+1 {
		t.Errorf("excerpt is %d characters", n)
	}
}
