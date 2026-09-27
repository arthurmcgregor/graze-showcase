# CLAUDE.md

Guidance for Claude Code working in this repository.

## What Graze is

A phone-first reading client for a FreshRSS instance that already exists.
FreshRSS is the source of truth for feeds, articles, read state and stars.
Graze holds an auth token and a short-TTL cache of the subscription list, and
nothing else. There is no database, and Graze never fetches an original URL.

`CONTEXT.md` fixes the vocabulary — feed, article, category, label, original,
full text, read, star, All, starred. Use those words, and no synonyms, in code,
templates, commits and comments.

## Commands

```bash
mise exec -- go run .        # listens on :8080, or PORT
mise exec -- go test ./...
mise exec -- go build -o graze .
```

## Git

Commit and push straight to `main`. No branches, no pull requests.

## Architecture

Single-package Go (`package main`), flat layout, no framework: stdlib
`net/http` routing, `html/template`, HTMX from the CDN with SRI, no JS build
step.

- `main.go` — config, routes, request logging, dev auth shim
- `greader.go` — the FreshRSS client and its protocol types
- `handlers.go` — handlers, view models, template parsing
- `list.go` — the list a page shows or an article came from, and every URL that carries it
- `content.go` — the article HTML rewrite pass
- `timefmt.go` — timestamps against the reader's timezone
- `templates/layout.html` — base layout; pages define `content`
- `templates/partials/` — parsed as a glob into every page's template set

Routes:

| Path | What |
|---|---|
| `/` | All, unread |
| `/c/{name...}` | one category, unread |
| `/f/{id}` | one feed, unread |
| `/starred` | starred, read or not |
| `/a/{id}` | one article; opening it marks it read |
| `POST /a/{id}/{read,unread,star,unstar}` | row and header actions |
| `POST /mark-all-read` | the avatar menu's catch-up |

Every list page issues one browser request; the server fans out to
`subscription/list`, `unread-count` and `stream/contents` so the chip counts
and the rows cannot disagree. `?c=<continuation>` with `HX-Request` returns the
next page of rows and a fresh sentinel, and nothing else.

Article ids in graze's URLs are the hex tail of the GReader id, because the full
id carries a slash, a colon and a comma.

Two auth layers, easy to conflate:

1. **User → Graze** — the forward-auth proxy in front of graze sets
   `Remote-User`. No local user table. Missing header is a 401.
2. **Graze → FreshRSS** — the GReader API password, which is *not* the
   FreshRSS login.

## Style

`static/style.css` is hand-written, with the colours and spacing as tokens on
`:root`. The brand accent is `--brand-accent`, `#b692f6`.

## What is trusted

FreshRSS sanitises article HTML before it stores it, but two things reach a
reader that it never sanitised, so graze gates both:

- the YouTube extension's iframe, which is added on the `EntryBeforeDisplay`
  hook — after sanitising, before serialising. `content.go` keeps an allowlist
  of embed hosts and rebuilds the iframe from a fixed set of attributes rather
  than subtracting a few, because `srcdoc` runs in graze's own origin.
- the output of graze's own rewrite, which reparses and reserialises markup.
  Those are not inverse operations: a string that arrived inert inside an
  attribute can leave as an element. So `strip` runs over the result too, and
  every response carries a CSP whose script-src is a per-request nonce.

`rewriteContent` is the only function that mints `template.HTML`, and its
fallbacks escape rather than pass through.

## Testing

Real adapters, no handler mocking. The GReader client is tested against
`httptest.NewServer` serving response fixtures **captured from the live
instance**; the response shapes are the main thing that can be wrong. Handlers
go through `httptest.NewRequest` with a `Remote-User` header.
