# Graze

A reading client for a FreshRSS instance that already exists. Graze owns no feeds, fetches nothing from the open internet, and stores nothing durable — FreshRSS is the source of truth for every noun below.

## Language

### The things being read

**Feed**:
One source you subscribe to, identified by its URL. Graze shows a feed's name and its articles; everything about managing one lives in FreshRSS.
_Avoid_: Subscription, channel, source

**Article**:
One entry from a feed. What FreshRSS's own interface calls an article, and what the reading loop is about.
_Avoid_: Item, entry, post, story

**Category**:
A named group of feeds — the folder a feed sits in. A category has an unread count and is the unit the chip bar switches between.
_Avoid_: Folder, tag, group

**Label**:
A per-article tag in FreshRSS. Graze does not use labels at all; the term exists here only because categories and labels are indistinguishable in the data Graze receives, and confusing the two silently mixes article tags into the category bar.

**Original**:
The publisher's own page for an article, away from both Graze and FreshRSS.
_Avoid_: Source, website, canonical

**Full text**:
The complete article body, retrieved by FreshRSS from the Original when a feed only publishes an excerpt. Graze never retrieves it; a feed either arrives complete or it does not.
_Avoid_: Scrape, fetch original, readability

**Excerpt**:
The few lines of an article a list row can show under its title. When FreshRSS retrieved the full text, it is what the feed itself published; otherwise it is the start of the article.
_Avoid_: Summary, extract, standfirst, teaser

**Lead image**:
An article's first image, which a list row can show beside its title. Graze shows it as the article has it, and never retrieves one of its own.
_Avoid_: Thumbnail, hero, featured image

### What a reader does

**Read**:
An article you have opened. Graze marks an article read the moment you open it, so "read" means "seen", not "finished".
_Avoid_: Seen, viewed, consumed

**Star**:
A marker you put on an article to keep it. Starring does not copy the article — if the feed or FreshRSS loses it, it is gone.
_Avoid_: Save, bookmark, favourite, keep

**List**:
What an article is being read from: All, one category, one feed, or starred. On a list's own page it is where the reader is; on an article it is where they came from and where Back returns.
_Avoid_: Destination, view, stream, inbox

**All**:
Every article from every subscribed feed, regardless of category. The default list, and a peer of the categories rather than one of them.
_Avoid_: Inbox, river, everything

**Starred**:
The collection of articles you have starred, newest star first. A list of its own, never a category.
