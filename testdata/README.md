# GReader fixtures

The client is tested against these rather than against mocks, because the
response shapes are the main thing that can be wrong — `summary` versus
`content`, string versus number ids, omitted-when-empty fields.

Captured from a live instance with `./capture.sh`, then made synthetic for
publishing: feed names, words, people and URLs are replaced, and everything
else — ids, timestamps, tags, markup, escaping, continuations — is exactly what
FreshRSS sent. Re-run the capture after a FreshRSS upgrade — a diff here is a
diff in the contract.

The tests derive what they expect from these files rather than hard-coding it,
so a recapture against different feeds does not break them. What they assert is
the shape: content under `summary.content` and not `content`, unread as the
absence of the read tag, a category readable only from `subscription/list`, and
a continuation only on a page FreshRSS filled.

`stream_unread.json` is the reading list with `xt=read`, `stream_paged.json` the
same stream at `n=2` so it carries a continuation, and `stream_starred.json`
holds a mix of read and unread, which is what exercises dim-in-place.

`stream_category.json` is one category's stream. The category is named with an
`&` and its feeds with a `?`, because FreshRSS escapes feed titles to fullwidth
look-alikes and leaves category names alone. `json.tool` writes anything
non-ASCII as a `\u` escape, so grep for the escape, not the character.

`stream_fulltext.json` is a feed FreshRSS fetches the full text for, set to the
append content action: each body is the feed's excerpt, then the full text
between `FULLCONTENT` comments.
