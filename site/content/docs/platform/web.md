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
- **At most 2 MiB is read, and kept whole.** A page is shown like any tool
  result: up to 48 KiB whole, a longer one as its first 4 KiB and last 2 KiB,
  so both ends of its envelope, and the model reads the rest with
  `read_output`, by line or by match. A page the fetch stopped at 2 MiB says
  so.

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

A run opens the browser at its first call and lets it go when the run ends,
and a run that stops to wait for an approval comes back to a fresh page. So
the browser is approved a page at a time, and only `browser_open` ever asks:

| Mode | A person who may approve their own | A guest | Nobody present, for a person who may approve their own | Nobody present, for anyone else |
| --- | --- | --- | --- | --- |
| `all`, `writes` | `browser_open` asks once | `browser_open` asks once | refused: only `browser_open` is offered, and says why | refused, the same |
| `destructive` | nothing asks | `browser_open` asks once | nothing asks: it may click and submit unseen | refused, the same |
| `off` | nothing asks | nothing asks | nothing asks | nothing asks |

`browser_click` and `browser_type` are writes, with or without a submit: a
click can buy, and a page's own script can send what is typed. They are asked
for at the open, where waiting loses nothing, and the request says so (`and:
click and type on the page it opens, on its own site only`). What is then done
on that page is not asked again, **on that page's site only**: a link may lead
to another site, and the page is read there, but a click or typing on it is
refused until `browser_open` opens it, which asks again. So is a button or a
submit that would send its form to another site. **Approve** covers the page
until the next `browser_open`; **Approve for this run** (`--for-run`) covers
every page the run opens, each on its own site. Where nothing asks, nothing is
kept to a site. The text typed is not shown to the approver.

What keeping to a site does not do:

- **It checks the next action, not the click that leaves.** A link to another
  site is followed, and that page is read, as `web_fetch` would read it; only
  what is done on it after is refused.
- **A site is a host,** exactly: not its scheme, not its port, and not
  `www.` and the bare name as one. The host asked for and the host the page
  lands on both count, so a redirect, an open redirect included, widens it to
  where it lands.
- **Only the page itself.** A frame from another site in the page is not
  checked apart from the page around it, and a page's own script may still
  send what is typed wherever it likes, as it could before.

Clicking and typing are writes, not destructive, by the maintainers' choice:
a team in `destructive` mode has its runs click and submit without asking,
and a scheduled run made for someone who may approve their own does so with
nobody present. A team that wants them asked uses
`writes`, or a rule on `browser_click` and `browser_type`.

A team's rules, per tool:

- A rule that asks for any of the five is asked at the open, with its
  `require_other`; an allow rule on `browser_open` alone does not stop it.
- A rule that denies `browser_open` leaves the run no browser: only the open
  is offered, and says why.
- A rule that denies `browser_click` or `browser_type` removes that one, and
  the request no longer says it covers it. Denying the click alone does not
  stop a form being sent: `browser_type` with `submit` sends it.

**What a page may reach.** Every request the page's tab makes, its scripts,
images, frames, styles, fetches and redirects included, is paused for the
brain to allow, and one to an address that is not public is blocked. The read
names up to ten of them and counts the rest. On a browser of the team's own,
requests are also paused browser-wide, so a popup's or a frame's from another
site are checked too. A page may open no socket of any kind (a websocket,
`WebSocketStream`, WebTransport, WebRTC), start no worker, and open no popup.
A link or form that targets another window opens in the page's own, and a
popup that opens all the same is closed. A page that needs any of these may
look blank or partly loaded; the tools tell the model so. A page that asks for
more than a thousand requests at once has the rest held back, and the read
says so.

On a browser runs share, each run's browser-wide guard sees every run's
requests, judged on a small queue of its own; what it cannot take in time is
failed, never let through. So a page that floods the browser can make another
run's requests fail, and a request from anything else that uses the browser
to a private address fails too.

A browser of the team's own is proved at start. It is sent to an address that
is always refused, then asked to fetch another from a page, each by a path of
its own: unless the guard pauses both, the browser is not used, whatever other
runs' guards pause at the same time. It also gets a browser context of its own for each run, so no cookie
or storage is carried from one run to the next. A hosted browser that will not
let its requests be paused, or will not keep the page in its tab, is used all
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
`OPENARITY_MCP_PRIVATE_NETWORKS` and the URL a team saves. That network is
internal: it routes nowhere, so the browser's one way out is a proxy the
worker serves on it (`OPENARITY_BROWSER_PROXY_BIND`). The proxy dials public
addresses only, on ports 80, 443, 8080 and 8443, and decides on the address
it connects to, so whatever a page asks for and however a name answers,
nothing private is reached: not the host, not the cloud's metadata address,
not the worker's other neighbours. Browserless takes the proxy in the URL a
team saves, as its overlay shows; without it, it reaches nothing. Measured on one machine, after opening one
page:

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

- **DNS rebinding through a hosted browser, or one you run elsewhere.** The
  browser resolves a name itself, after the brain judged it, so a name whose
  answer changes in between is out of the check's reach. The overlays here
  close it with the worker's proxy, which looks the name up once and connects
  to what it checked. A hosted browser resolves on its provider's network,
  which the brain cannot reach into; a browser run another way needs the
  same as the overlays: no route out but through a proxy like it.
- **Uploads, downloads, a second tab, and a page kept across an approval.**
- **A frame from another site in a process of its own.** Chrome with site
  isolation runs one as a target apart from the tab, so it gets none of the
  script that locks sockets and workers; its requests are still paused
  browser-wide. headless-shell runs such a frame in the page, where the tab's
  guard covers it, and Lightpanda loads none. Accepted: the browser's own
  network is what keeps such a socket off private addresses.
- **DNS rebinding through Firecrawl.** Firecrawl resolves the name itself, so a
  name whose answer changes between the brain's check and Firecrawl's lookup is
  out of the brain's reach. A self-hosted Firecrawl needs network rules of its
  own that block private and link-local ranges.
- **Pages that need JavaScript** come back thin from the brain's own reader;
  Firecrawl renders them.
- **PDFs, images, crawling, a page cache, and per-team allow or deny lists.**
- **Firecrawl's cache.** A scrape may return a copy up to two days old.
