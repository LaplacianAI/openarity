---
title: Web tools
---

Two tools let a run use the web: `web_search` finds pages, and `web_fetch` reads
one. The team's [hub](hub.md) always has both; any other agent has them when it
is defined with `web: true`.

## Searching

`web_search` goes through the team's search provider, set once per team:

```sh
oa teams settings set platform --search searxng --search-url http://searxng:8080
printf %s "$BRAVE_KEY" | oa teams settings set platform --search brave --search-key-stdin
```

The providers are `searxng` (self-hosted, no key), `brave`, `tavily`,
`firecrawl-cloud` and `firecrawl-oss` (self-hosted, key optional with
`--no-search-key`). A key is read from stdin and nowhere else — an argument is
readable by every process on the machine and lands in shell history — and is
kept in the secret store. A team with no provider has no `web_search`.

## Reading a page

`web_fetch(url)` takes an `http` or `https` address and returns the page as
markdown. A team whose provider is Firecrawl has pages read by Firecrawl;
every other team has them read by the brain itself:

- **Public addresses only**, checked on every redirect — at most five — so a
  page cannot send the brain to a private network or a cloud metadata address.
  `MCP_PRIVATE_NETWORKS` does not apply: that is for addresses an operator
  chose, and a fetch URL is a model's.
- **HTML, XHTML, plain text, markdown and JSON.** A type it cannot read, such as
  a PDF, is refused before the body is read. HTML is reduced to its article
  before it becomes markdown, so menus and banners stay out.
- **At most 2 MiB is read, and 32 KiB reaches the model**, cut at a character
  boundary with a line saying where it was cut.

Through Firecrawl, the brain checks the host before asking and the address the
page actually ended at afterwards, and refuses a page that ended somewhere
private.

## What the model sees

A page arrives wrapped in an envelope only the brain can close:

```text
<page url="https://go.dev/doc/go1.26" title="Go 1.26 Release Notes" end="WYVGAUXSP4QULT2LKKDAGNXVWF">
…the page, as markdown…
</page end="WYVGAUXSP4QULT2LKKDAGNXVWF">
```

`end` is drawn fresh for every call, so a page that writes its own
`</page>` cannot end the envelope early, and the hub's instructions treat what
`web_search`, `web_fetch` and `delegate` return as data, never as instructions.
In a transcript, `oa runs events` shows a page as
`title — url · 12 KiB · cut`; `--verbose` prints its body.

## Not built

- **DNS rebinding through Firecrawl.** Firecrawl resolves the name itself, so a
  name whose answer changes between the brain's check and Firecrawl's lookup is
  out of the brain's reach. A self-hosted Firecrawl needs network rules of its
  own that block private and link-local ranges.
- **Pages that need JavaScript** come back thin from the brain's own reader;
  Firecrawl renders them.
- **PDFs, images, crawling, a page cache, and per-team allow or deny lists.**
- **Firecrawl's cache.** A scrape may return a copy up to two days old.
