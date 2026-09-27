package main

import (
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Article HTML arrives from FreshRSS already sanitised, with image URLs already
// absolutised. This pass is only the handful of rewrites left over, all of them
// about rendering on a phone:
//
//   - iframes from the YouTube extension bypass FreshRSS's sanitiser entirely,
//     because the extension runs on the EntryBeforeDisplay hook, after it. The
//     allowlist below is the only gate on them.
//   - the extension hard-codes 560x315, which overflows a 390px screen.
//   - FreshRSS promotes data-src to src when rendering its own UI, not when
//     serialising for the API, so lazy images would arrive blank.
//   - some publishers refuse an image whose Referer is not their own site, so
//     article images are fetched without one.
//   - a comic's punchline lives in the image's title attribute, and a phone has
//     no hover to reveal it.
//   - a feed FreshRSS fetches the full text for can keep its excerpt beside it,
//     which the full text would only repeat.
func rewriteContent(raw string, embedHosts []string) template.HTML {
	_, full := splitFullText(raw)
	body := parseBody(full)
	if body == nil {
		return template.HTML(template.HTMLEscapeString(raw))
	}

	var imgs []*html.Node
	walk(body, func(n *html.Node) {
		if !strip(n) {
			return
		}
		switch n.DataAtom {
		case atom.Iframe:
			rewriteIframe(n, embedHosts)
		case atom.Img:
			promoteLazy(n)
			setAttr(n, "referrerpolicy", "no-referrer")
			imgs = append(imgs, n)
		}
	})

	if len(imgs) == 1 && isComic(body, imgs[0]) {
		breakOut(imgs[0])
	}

	var out strings.Builder
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		html.Render(&out, c)
	}
	return template.HTML(out.String())
}

// FreshRSS wraps the full text it fetched from the original in these, whatever
// its content action does with the excerpt.
const (
	fullTextStart = "<!-- FULLCONTENT start //-->"
	fullTextEnd   = "<!-- FULLCONTENT end //-->"
)

// splitFullText separates the feed's own excerpt from the full text FreshRSS
// fetched. The excerpt sits before the full text or after it, depending on the
// feed's content action. Without full text, both are the whole body.
func splitFullText(raw string) (excerpt, full string) {
	before, rest, ok := strings.Cut(raw, fullTextStart)
	if !ok {
		return raw, raw
	}
	full, after, _ := strings.Cut(rest, fullTextEnd)
	return before + after, full
}

func parseBody(s string) *html.Node {
	doc, err := html.Parse(strings.NewReader("<html><body>" + s))
	if err != nil {
		return nil
	}
	return findBody(doc)
}

// preview is what a list row shows of an article besides its title: the start
// of its excerpt, and its lead image. Both are plain strings, for html/template
// to escape. A comic has neither, because its image is the whole article.
func preview(raw, title string) (excerpt, image string) {
	short, full := splitFullText(raw)
	body := parseBody(full)
	if body == nil {
		return "", ""
	}
	var imgs []*html.Node
	walk(body, func(n *html.Node) {
		if n.DataAtom == atom.Img {
			imgs = append(imgs, n)
		}
	})
	if len(imgs) == 1 && isComic(body, imgs[0]) {
		return "", ""
	}

	if short != full {
		excerpt = excerptOf(parseBody(short))
	}
	if excerpt == "" {
		excerpt = excerptOf(body)
	}
	if excerpt == title || isBareURL(excerpt) {
		excerpt = ""
	}
	return excerpt, leadImage(imgs)
}

const excerptRunes = 300

// excerptOf is the first few hundred characters of text, leaving out captions
// and credits, which say nothing about the article on their own.
func excerptOf(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch c.DataAtom {
			case atom.Figure, atom.Figcaption, atom.Iframe:
				continue
			case atom.P, atom.Div, atom.Br, atom.Li, atom.Blockquote, atom.Td, atom.Th,
				atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				b.WriteByte(' ')
			}
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
			}
			rec(c)
		}
	}
	rec(n)

	var words []string
	length := 0
	for _, w := range strings.Fields(b.String()) {
		length += utf8.RuneCountInString(w) + 1
		if length > excerptRunes {
			return strings.Join(words, " ") + "…"
		}
		words = append(words, w)
	}
	return strings.Join(words, " ")
}

func isBareURL(s string) bool {
	return !strings.Contains(s, " ") && (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://"))
}

// leadImage is the first image worth a thumbnail. Without fetching it there is
// no knowing its size, so this drops only what declares itself small, and the
// browser hides whatever turns out to be.
func leadImage(imgs []*html.Node) string {
	for _, n := range imgs {
		src := attr(n, "src")
		if src == "" {
			src = attr(n, "data-src")
		}
		if !strings.HasPrefix(src, "https://") || declaredSmall(n, "width") || declaredSmall(n, "height") {
			continue
		}
		return src
	}
	return ""
}

func declaredSmall(n *html.Node, key string) bool {
	v, err := strconv.Atoi(attr(n, key))
	return err == nil && v < 100
}

// strip removes what must never survive, and reports whether the node did.
// FreshRSS sanitises the string it stores, but this pass reparses and
// reserialises that string, and those are not inverse operations: markup that
// went in inert inside an attribute can come out as an element. So the output
// is checked too, rather than the input being trusted twice.
func strip(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Script, atom.Object, atom.Embed, atom.Form, atom.Base, atom.Meta, atom.Link, atom.Style:
		remove(n)
		return false
	}
	kept := n.Attr[:0]
	for _, a := range n.Attr {
		if strings.HasPrefix(strings.ToLower(a.Key), "on") {
			continue
		}
		if isURLAttr(a.Key) && isScriptURL(a.Val) {
			continue
		}
		kept = append(kept, a)
	}
	n.Attr = kept
	return true
}

func isURLAttr(key string) bool {
	switch strings.ToLower(key) {
	case "href", "src", "srcset", "action", "formaction", "data-src", "data-srcset", "xlink:href":
		return true
	}
	return false
}

func isScriptURL(v string) bool {
	// Browsers ignore leading whitespace and control characters in a URL.
	v = strings.ToLower(strings.TrimFunc(v, func(r rune) bool { return r <= ' ' }))
	v = strings.NewReplacer("\t", "", "\n", "", "\r", "", "\x00", "").Replace(v)
	return strings.HasPrefix(v, "javascript:") ||
		strings.HasPrefix(v, "vbscript:") ||
		strings.HasPrefix(v, "data:text/html")
}

func rewriteIframe(n *html.Node, embedHosts []string) {
	src := attr(n, "src")
	if !embedAllowed(src, embedHosts) {
		remove(n)
		return
	}
	// Every other attribute goes, rather than a few being subtracted. srcdoc
	// wins over src and runs in this document's origin, and an onload= is an
	// onload=; keeping an allowlist of attributes is the only version of this
	// that is safe by construction. The sandbox withholds top-level navigation,
	// so a compromised embed cannot redirect the page out from under the reader.
	n.Attr = []html.Attribute{
		{Key: "src", Val: src},
		{Key: "class", Val: "video-frame"},
		{Key: "loading", Val: "lazy"},
		{Key: "sandbox", Val: "allow-scripts allow-same-origin allow-presentation allow-popups"},
		{Key: "allowfullscreen"},
	}
	wrap(n, &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div",
		Attr: []html.Attribute{{Key: "class", Val: "video"}}})
}

// YouTube's embed hosts, which is what the extension emits. PeerTube uses the
// same /videos/embed/ shape on a host of its own, so those hosts have to be
// named in GRAZE_EMBED_HOSTS — "any https host serving that path" would let a
// feed frame anything it liked.
var youTubeHosts = []string{"www.youtube.com", "youtube.com", "www.youtube-nocookie.com", "youtube-nocookie.com"}

func embedAllowed(src string, extraHosts []string) bool {
	u, err := url.Parse(src)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	for _, h := range youTubeHosts {
		if host == h {
			return strings.HasPrefix(u.Path, "/embed/")
		}
	}
	for _, h := range extraHosts {
		if host == h {
			return strings.HasPrefix(u.Path, "/videos/embed/")
		}
	}
	return false
}

func promoteLazy(n *html.Node) {
	if attr(n, "src") == "" {
		if v := attr(n, "data-src"); v != "" {
			setAttr(n, "src", v)
		}
	}
	if attr(n, "srcset") == "" {
		if v := attr(n, "data-srcset"); v != "" {
			setAttr(n, "srcset", v)
		}
	}
	if attr(n, "loading") == "" {
		setAttr(n, "loading", "lazy")
	}
}

// isComic asks whether the body is essentially just the image. The threshold is
// generous enough for an alt-text line or a "permanent link" tail, and far short
// of a post that happens to carry one illustration.
func isComic(body, img *html.Node) bool {
	return len(strings.TrimSpace(textOf(body))) <= 200 && !hasAncestorTag(img, body, atom.Figure)
}

// breakOut lifts the image out of the text column and surfaces its title
// attribute, which is where a comic keeps the joke and a phone cannot hover.
func breakOut(img *html.Node) {
	fig := &html.Node{Type: html.ElementNode, DataAtom: atom.Figure, Data: "figure",
		Attr: []html.Attribute{{Key: "class", Val: "comic"}}}
	wrap(img, fig)
	if title := attr(img, "title"); title != "" {
		fc := &html.Node{Type: html.ElementNode, DataAtom: atom.Figcaption, Data: "figcaption"}
		fc.AppendChild(&html.Node{Type: html.TextNode, Data: title})
		fig.AppendChild(fc)
	}
}

// --- node helpers ---

func findBody(n *html.Node) *html.Node {
	if n.DataAtom == atom.Body {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b := findBody(c); b != nil {
			return b
		}
	}
	return nil
}

// walk visits every node under n, and tolerates fn removing the node it is given.
func walk(n *html.Node, fn func(*html.Node)) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode {
			fn(c)
			if c.Parent != nil {
				walk(c, fn)
			}
		}
		c = next
	}
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return b.String()
}

func hasAncestorTag(n, stop *html.Node, a atom.Atom) bool {
	for p := n.Parent; p != nil && p != stop; p = p.Parent {
		if p.DataAtom == a {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func remove(n *html.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

func wrap(n, wrapper *html.Node) {
	parent, next := n.Parent, n.NextSibling
	parent.RemoveChild(n)
	wrapper.AppendChild(n)
	parent.InsertBefore(wrapper, next)
}
