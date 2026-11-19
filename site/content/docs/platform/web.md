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

The brain dials a search provider only at a public address, or in a range named
in `OPENARITY_MCP_PRIVATE_NETWORKS`. A SearXNG beside the brain, as in the
example, is at a private address: `deployment/docker-compose.searxng.yml` runs
one and says which `/32` to name. Without it, the setting saves and every
`web_search` fails.

## Reading a page

`web_fetch(url)` takes an `http` or `https` address and returns the page as
markdown. A team whose provider is Firecrawl has pages read by Firecrawl;
every other team has them read by the brain itself:

- **Public addresses only**, checked on every redirect — at most five — so a
  page cannot send the brain to a private network or a cloud metadata address.
  `OPENARITY_MCP_PRIVATE_NETWORKS` does not apply: that is for addresses an operator
  chose, and a fetch URL is a model's.
- **HTML, XHTML, plain text, markdown and JSON.** A type it cannot read, such as
  a PDF, is refused before the body is read. HTML is reduced to its article
  before it becomes markdown, so menus and banners stay out.
- **At most 2 MiB is read, and kept whole.** A page up to 32 KiB reaches the
  model whole; a longer one as its first 24 KiB and last 8 KiB, so both ends
  of its envelope, and the model reads the rest with `read_output`, by line or
  by match. A page the fetch stopped at 2 MiB says so.

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

## A browser

A team can also give its runs a browser: one that opens a page, runs its
scripts, and can be clicked and typed into. The team's hub has it; any other
agent has it when it is defined with `browser: true`, which is a grant of its
own, apart from `web`. The tools that drive it come in a later release; this
sets up the browser they use.

The browser is set once per team, in **Settings › Browser** or with `oa`:

```sh
oa teams settings set platform --browser lightpanda --browser-url ws://lightpanda:9222
printf %s "$BROWSERBASE_KEY" | oa teams settings set platform --browser browserbase --browser-key-stdin
```

| Provider | Where it runs | Key |
| --- | --- | --- |
| `lightpanda` | its cloud, or your own at `--browser-url` | for its cloud |
| `browserless` | its cloud, or your own at `--browser-url` | its token |
| `steel` | its cloud, or your own at `--browser-url` (its API address) | for its cloud |
| `cdp` | any browser that speaks the Chrome DevTools Protocol, at `--browser-url` | optional, sent as `?token=` |
| `browserbase`, `hyperbrowser`, `anchor`, `kernel` | hosted | its API key |
| `brightdata` | hosted, with its proxies | the zone's `USER:PASS` |

A key is read from stdin and kept in the secret store, as a search key is, and
is sent again when the provider or its address changes. A self-hosted browser
may have none; `--no-browser-key` removes a stored one. The browsers that make
a session for each run (Steel, Browserbase, Hyperbrowser, Anchor, Kernel) have
it ended when the run ends: some of them keep a session running, and billed,
until it times out.

To run one beside the brain, `make browser name=<one>` in `deployment/` starts
it on a network of its own, shared only with the worker, and each
`docker-compose.<name>.yml` says the address to name in
`OPENARITY_MCP_PRIVATE_NETWORKS` and the URL a team saves. Measured on one
machine, after opening one page:

| Browser | Image | Memory |
| --- | --- | --- |
| Lightpanda | 244 MB | about 63 MiB |
| headless-shell (`cdp`) | 539 MB | about 300 MiB |
| Browserless | 4 GB | about 600 MiB |
| Steel | 3.8 GB | about 980 MiB |

Lightpanda is a browser built for automation and draws nothing, so a few sites
do not work in it yet; the others are Chrome. headless-shell answers only at
an address, never a name, so a team saves `http://172.31.255.218:9222`. None
of the self-hosted ones but Browserless asks for a key, so whatever reaches
their port drives them: keep them on the network the overlay gives them.

## Not built

- **DNS rebinding through Firecrawl.** Firecrawl resolves the name itself, so a
  name whose answer changes between the brain's check and Firecrawl's lookup is
  out of the brain's reach. A self-hosted Firecrawl needs network rules of its
  own that block private and link-local ranges.
- **Pages that need JavaScript** come back thin from the brain's own reader;
  Firecrawl renders them.
- **PDFs, images, crawling, a page cache, and per-team allow or deny lists.**
- **Firecrawl's cache.** A scrape may return a copy up to two days old.
