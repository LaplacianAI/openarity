# Brand

The mark, the palette, and the rules for using them. This file is the source:
`apps/cli` and `apps/dashboard` are separate modules and share no code, so a
colour is copied from here into each, and changing one means changing this
first.

---

## The mark

K5 — the complete graph on five vertices. Five nodes, every pair joined, ten
edges.

It is the **smallest non-planar graph**: by Kuratowski's theorem, K5 and K3,3
are the two obstructions to drawing a graph in the plane without crossings. So
the mark is the smallest graph that cannot be flattened — for a platform whose
argument is that capability is not a flat list.

### The drawing

A square with a hub, not a circle.

Every drawing of K5 must cross somewhere; that is what non-planar means, and no
layout escapes it. But a circular layout puts the crossings in a **pentagram**,
which reads as an occult symbol rather than a network — that was the first
draft and it went straight in the bin. Here the four outer nodes make a square,
the fifth sits at the centre, and the two long diagonals bow around it. The
crossing is still there. It just is not a star.

```text
4 sides + 4 spokes + 2 bowed diagonals = 10 edges
```

Two numbers in `assets/logo.svg` are load-bearing:

- **The bow is perpendicular to each diagonal, not vertical.** A vertical
  offset of the same magnitude leaves only `1/√2` of it as real clearance from
  the hub, and the hub is swallowed. It was, until it was measured.
- **The hub is larger than the outer nodes** (5.2 against 4.2). At equal size
  it reads as a crossing artefact rather than a vertex.

### Files

|File|For|
|---|---|
|`assets/logo.svg`|the mark, 64×64|
|`assets/logo-wordmark.svg`|mark and name locked up, 232×64|
|`assets/favicon.svg`|browser tab, 32×32|

**The favicon is not a scaled copy.** At 16px the full mark's 2.2/64 strokes
fall below one device pixel and the edges vanish. It carries heavier strokes,
proportionally larger nodes, and a tighter bow.

**Each file switches theme by itself**, with a `prefers-color-scheme` query in
a `<style>` block rather than `currentColor`. An SVG embedded with `<img>` is a
separate document and inherits no colour from the page, so a `currentColor`
mark renders **black** — which on GitHub in dark mode is black on black. The
internal query works there, as a favicon, and standalone.

The limit of that: it follows the browser's theme, not the background it
happens to sit on. A page with both a light and a dark section must inline the
SVG and style `.mark` directly.

### In a terminal

K5 has no ASCII form. It was rasterised onto character grids at 13×9, 21×11 and
25×13 and every one is static — cells cannot carry ten crossing edges. What
works is **braille**, which gives 2×4 sub-cells per character:

```text
⠀⣤⠤⠤⠤⠤⠤⠤⠤⠤⠤⠤⠤⠤⡄⠄
⠀⡿⡑⢄⠀⠀⠀⠀⠀⠀⠀⠀⡠⢊⡇⠁
⠀⡇⢳⡀⠑⢄⠀⠀⠀⠀⡠⠊⢀⡞⡇⠀
⠀⡇⠀⠳⡀⠀⠑⢄⡤⠊⠀⢀⠞⠀⡇⠀
⠀⡇⠀⠀⠙⢆⡠⠋⠕⢅⡰⠋⠀⠀⡇⠀
⠀⡇⠀⠀⡠⠊⠑⢦⡴⠊⠑⢄⠀⠀⡇⠀
⠀⡇⡠⢊⣠⠴⠚⠁⠈⠓⠦⣄⡑⢄⡇⠀
⠀⠍⠍⠉⠉⠉⠉⠉⠉⠉⠉⠉⠉⠉⠅⠅
```

Sixteen cells by eight. Twelve by six still reads; ten by five is mud, so that
is the floor.

Braille needs a font that has the glyphs, which most terminal fonts do and some
do not. **The fallback is the wordmark alone, never a different mark.** Drawing
a wheel — the square and hub without the bowed diagonals — would be eight edges
and a different graph, which is worse than no picture.

### Rules

- **All ten edges, or none.** A version missing the diagonals is a wheel, not
  K5, and the whole point is the graph it is.
- **Never on a circle.** That is the pentagram, and the reason this layout
  exists.
- **Nodes are filled, and the hub is the largest.**
- **The wordmark is lowercase**: `openarity`, never `OpenArity` or
  `OPENARITY`. The one exception is an environment variable prefix, where the
  shell decides the case.
- **Never stretch it.** The square is square.
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

A typeface, a favicon file, and anything for the dashboard beyond the palette
above. They belong with the first design that needs them, and choosing them
now would mean choosing them twice.
