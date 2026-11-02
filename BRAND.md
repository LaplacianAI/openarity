# Brand

The mark, the palette, and the rules for using them. This file is the source:
`apps/cli` and `apps/dashboard` are separate modules and share no code, so a
colour is copied from here into each, and changing one means changing this
first.

---

## The mark

A squircle **O**, cut once on the diagonal into two halves, with a visor and
two nodes on the cut.

- **Two agents.** The halves are identical and only make the O together; the
  cut between them is the hand-off.
- **A graph.** The two nodes sit on the cut, so it reads as an edge between
  them - quietly, at the sizes where it can be seen.
- **A machine.** The visor is the opening of the O, made wide and low, so the
  shape reads as something built rather than drawn: a head with no face.

It replaced K5, the complete graph on five vertices, which read as a figure
hanging in a frame - partly because of a fill bug (below), and partly because
any five dots and crossing lines make a body.

### Why this shape and not another

Every candidate was judged by what it could be mistaken for, because a mark
is read before it is understood. These were drawn and refused:

|Shape|Read as|
|---|---|
|K5, a square with a hub and crossing diagonals|a figure hanging in a frame|
|a ring with a dot leaving it|an egg cell; and suggestive|
|a ring with a line leaving it|the male symbol, a magnifying glass, a key|
|a ring of dots|a loading spinner|
|four squares around a round hole|the Chase octagon; Microsoft|
|a robot head|a cartoon; Baymax|
|an O cut on the other diagonal|the "no" slash, Ø|

So the rules below are mostly about keeping it away from those.

### The drawing

All numbers are in a 64-unit box, and live in `assets/mark/geometry.py`:

|Part|Value|
|---|---|
|body|a 48 x 48 rounded square at (8, 8), corner radius 14.9|
|visor|26 x 17, corner radius 3, centred|
|cut|3 wide, along y = x: top-left to bottom-right|
|nodes|radius 3.1, on the cut, 14.3 from the centre on each axis|

Load-bearing:

- **The cut runs top-left to bottom-right.** The other diagonal is the
  "no" slash, and the mark becomes Ø.
- **The visor is square-cornered, not a pill.** A pill turns the cut and the
  visor into one bent line - a Z - and reads soft.
- **The visor is tall enough to stay an O.** At 12 high it was a slot; at 17
  the opening still reads as the letter.
- **The cut, the visor and the nodes are a mask, not shapes in a colour.**
  The page shows through them, so the mark sits on any background.

### Files

|File|For|
|---|---|
|`assets/logo.svg`|the mark, 64 x 64|
|`assets/logo-wordmark.svg`|mark and name locked up, 232 x 64|
|`assets/favicon.svg`|browser tab|
|`site/content/assets/logo.svg`, `logo-wordmark.svg`, `favicon.svg`|the docs site's copies; the header shows the mark, the tab the favicon|
|`site/content/assets/apple-touch-icon.png`, `icon-192.png`, `icon-512.png`, `site.webmanifest`|what an SVG cannot be: the iOS home-screen icon and the web manifest, linked from `site/overrides/main.html`|
|`apps/dashboard/public/favicon.svg`|the dashboard's copy; Vite's publicDir cannot reach outside the app|
|`apps/dashboard/src/components/mark.tsx`|the mark inline, in the sidebar and on sign-in: the dashboard's theme is a class on `<html>`, which an `<img>` cannot see; `mark.test.tsx` fails if its parts and `assets/logo.svg`'s differ|
|`assets/mark/geometry.py`|the numbers|
|`assets/mark/build.py`|writes every SVG above from them; standard library only|
|`assets/mark/raster.py`|writes the site's home-screen icons and its manifest; needs Pillow|
|`assets/mark/intro.py`|the presentation opener, in Manim|
|`assets/mark/intro-dark.mp4`, `intro-light.mp4`|the opener, rendered|
|`assets/mark/intro-dark.gif`, `intro-light.gif`|the opener at 720 px and 30 fps, for the README: GitHub plays a GIF inline and an MP4 not at all|

**Edit `geometry.py` and run `python3 assets/mark/build.py` and
`python3 assets/mark/raster.py`; never edit an icon by hand.** It writes every copy, so none can go stale, and
`apps/dashboard/src/test/favicon.test.ts` fails if the dashboard's copy and
`assets/favicon.svg` differ anyway.

`mark.tsx` is the one copy `build.py` does not write, since it is a React
component: a change to `geometry.py` is copied into it by hand, and its test
fails until it is. The palette is copied the same way, into
`apps/dashboard/src/styles.css` as `--spectral-*`, and
`apps/dashboard/src/test/palette.test.ts` reads the table below and fails when
a value there and in `styles.css` disagree.

**The favicon is not a scaled copy.** It fills more of its box and carries a
wider cut and larger nodes, so at 32 px the nodes are a clear notch and at
16 px they still widen the cut where they sit.

**Each file switches theme by itself**, with a `prefers-color-scheme` query in
a `<style>` block rather than `currentColor`. An SVG embedded with `<img>` is a
separate document and inherits no colour from the page, so a `currentColor`
mark renders **black** - which on GitHub in dark mode is black on black.

**The style sets `fill`, and nothing in the mark relies on `fill="none"`.**
A CSS rule beats a presentation attribute, which is how the previous mark's
`.mark { fill }` filled in its curved diagonals and drew the hanging figure.
Every part here is a fill or a hole.

The limit of the theme switch: it follows the browser's theme, not the
background it happens to sit on. A page with both a light and a dark section
must inline the SVG and style `.mark` directly.

### Motion

**The mark is still everywhere.** The opener in `assets/mark/` is for
presentations only - the start of a talk or a demo, where it is seen once -
and the head of the README, which is the project's first slide. A
swarm of agents converges into the outline, the O splits into two, the visor
opens, a signal runs down the cut from one node to the other, and the name
arrives. Six and a half seconds; nothing in the product animates the mark.

The opener sets the name in Avenir Next until a typeface is chosen (see "Not
decided yet"); changing it is one line in `intro.py` and a re-render.

### In a terminal

No terminal art yet; the CLI's banner is the wordmark alone. If one is drawn,
it is the whole mark - body, visor, cut - in braille, which gives 2 x 4
sub-cells per character; never a different picture.

### Rules

- **Two halves, one cut, top-left to bottom-right.** Never the other
  diagonal, never a second cut, never the halves apart except in the opener.
- **The gaps stay open.** Never fill the cut, the visor or the nodes with a
  colour; they are holes.
- **The wordmark is lowercase**: `openarity`, never `OpenArity` or
  `OPENARITY`. The one exception is an environment variable prefix, where the
  shell decides the case.
- **Never stretch it, never rotate it.** The squircle is square.
- **One colour per theme**: the accent below. No gradient, no outline, no
  shadow.
- **The mark is decoration, not status.** It belongs on a first-run screen and
  a page header. A mark on every command stops being a mark.

---

## Palette — Spectral

Warm graphite neutrals with a single cool accent. Derived rather than picked,
from three palettes that were designed rather than assembled:

- [Solarized](https://ethanschoonover.com/solarized/) — the rule that both
  modes are one palette at matching lightness, so neither feels heavier.
- [Nord](https://www.nordtheme.com/docs/colors-and-palettes) — roles grouped
  and kept few. Eight, not sixteen.
- [Everforest](https://github.com/sainnhe/everforest/blob/master/palette.md) —
  neutrals tinted warm instead of pure grey.

The combination is the part that is ours: Nord pairs cool neutrals with a cool
accent, Everforest pairs warm neutrals with a warm one. Warm neutrals with a
cool accent is unoccupied, and "spectral" is the honest word for it — spectral
graph theory is where the Laplacian comes from.

|Role|Dark|Light|Used for|
|---|---|---|---|
|`bg`|`#1A2023`|`#FBF7EF`|the page, the terminal default|
|`surface`|`#232B2F`|`#F2ECE0`|cards, raised panels, table headers|
|`text`|`#D9D3C7`|`#2E3A3D`|body|
|`muted`|`#8A9391`|`#5F6763`|labels, secondary detail, absent values|
|`accent`|`#5FD3BC`|`#0B6A5C`|the mark, headings, selection, focus|
|`ok`|`#93C97E`|`#41692A`|success, healthy, ready|
|`warn`|`#E3B778`|`#85540F`|degraded, deprecated, needs attention|
|`error`|`#E88A82`|`#A83229`|failure, refused, unreachable|

### Contrast

Computed, not estimated. WCAG 2.1 against **both** the background and the
raised surface, in both modes — a colour that passes on `bg` and fails on
`surface` is the usual way a palette breaks in practice.

```text
DARK   on bg / on surface        LIGHT  on bg / on surface
text   11.05 AAA / 9.67 AAA      text   10.99 AAA / 9.98 AAA
muted   5.23 AA  / 4.57 AA       muted   5.45 AA  / 4.95 AA
accent  9.04 AAA / 7.91 AAA      accent  6.08 AA  / 5.53 AA
ok      8.55 AAA / 7.47 AAA      ok      5.99 AA  / 5.44 AA
warn    8.87 AAA / 7.76 AAA      warn    6.01 AA  / 5.45 AA
error   6.58 AA  / 5.75 AA       error   6.23 AA  / 5.66 AA
```

Body text is 11.05:1 dark and 10.99:1 light. That pair is Solarized's symmetry
rule holding: switch modes and the perceived weight does not move.

Every role clears AA (4.5:1) everywhere it is used. Nothing here relies on
AA-large, because a 3:1 colour used for a label rather than a heading is a
failure nobody notices until someone reports it.

### Terminals

Colours are written as hex and degraded by the renderer — a 256-colour
terminal gets the nearest cube entry, and a terminal reporting no colour
support gets none. Nothing in the CLI branches on this; styles are built
against the writer they print to, and a pipe is not a terminal.

Two rules that follow from that:

- **Colour never carries meaning alone.** A failure says so in words. Someone
  reading a redirected log, or with a red-green deficiency, gets the same
  information.
- **The mark is never the only thing on a screen.** It is decoration on a
  first-run screen, not a status.

---

## Not decided yet

A typeface, raster fallbacks for the favicon, and anything for the dashboard
beyond the palette above, the mark in its tab, and the mark beside its name. They belong with the first
design that needs them, and choosing them now would mean choosing them twice.
