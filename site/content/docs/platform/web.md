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
own, apart from `web`.

| Tool | What it does |
| --- | --- |
| `browser_open(url)` | opens a page and reads it |
| `browser_snapshot()` | reads the open page again, as it is now |
| `browser_click(ref)` | clicks a control, then reads the page it leaves |
| `browser_type(ref, text, submit)` | puts text in a field, sends its form when `submit` is true, then reads the page |
| `browser_screenshot()` | the page as a picture, for what its text does not say |

A read is the page's text with every link, button and field numbered:

```text
# Flights
[1 field text] "to" = ""  [2 button] Search
[3 link] Deals (/deals)
```

`ref` is that number, good until the next read; a page's own text that looks
like one is escaped. A read keeps at most 200,000 characters of the page, and
leaves out what a password field holds. `browser_type` takes at most 4,000
characters, and in a select it chooses the option with that name or value.
After a click or a submit the read waits for the page it goes to, up to 15
seconds; what a page fetches for itself later may come only with
`browser_snapshot`. No call takes more than 30 seconds.

Each call follows the team's approval rule for reaching the internet, as
`web_fetch` does. A run opens the browser at its first call and lets it go
when the run ends. A run that stops to wait for an approval comes back to a
fresh page, so a click asked about after the page was opened has nothing to
click: approve `browser_open` for the rest of the run (`--for-run`) rather
than one call at a time.

**What a page may reach.** Every request the page's tab makes, its scripts,
images, frames, styles, fetches, a worker's fetches and redirects included, is
paused for the brain to allow, and one to an address that is not public is
blocked; the read says what was. A page may open no popup and no websocket,
and a link or form that targets another window opens in its own. A browser of
the team's own is proved at start: it is sent to an address that is always
refused, and one that loads it is not used. It also gets a browser context of
its own for each run, so no cookie or storage is carried from one run to the
next. A hosted browser that will not let its requests be paused is used all
the same, since what it reaches is its provider's network, and every read
says so.

On Bright Data a session takes one page: each `browser_open` makes a new one,
and a click that would go to another page may fail.

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

Lightpanda is a browser built for automation, and draws a plain version of a
page without its styles, so a few sites do not work in it yet; the others are
Chrome. headless-shell answers only at
an address, never a name, so a team saves `http://172.31.255.218:9222`. None
of the self-hosted ones but Browserless asks for a key, so whatever reaches
their port drives them: keep them on the network the overlay gives them.

## Not built

- **DNS rebinding through the browser.** The browser resolves a name itself,
  after the brain judged it, so a name whose answer changes in between is out
  of the check's reach. For a browser of the team's own, its network is the
  boundary that holds: the deployment guide's "A browser" gives the host
  rules that keep it off private and link-local addresses.
- **A worker's own websocket in Chrome.** Fetch pauses no websocket, and no
  script reaches into a worker, so a worker could open one; WebRTC is not
  checked either. The same network rules stand behind them. Lightpanda runs
  neither.
- **Uploads, downloads, a second tab, and a page kept across an approval.**
- **DNS rebinding through Firecrawl.** Firecrawl resolves the name itself, so a
  name whose answer changes between the brain's check and Firecrawl's lookup is
  out of the brain's reach. A self-hosted Firecrawl needs network rules of its
  own that block private and link-local ranges.
- **Pages that need JavaScript** come back thin from the brain's own reader;
  Firecrawl renders them.
- **PDFs, images, crawling, a page cache, and per-team allow or deny lists.**
- **Firecrawl's cache.** A scrape may return a copy up to two days old.
