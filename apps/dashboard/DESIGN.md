---
name: openarity dashboard
description: Where a team of agents is run, read as a framed sheet of paper in BRAND's Spectral palette.
colors:
  background: "#FBF7EF"
  surface: "#F2ECE0"
  foreground: "#2E3A3D"
  muted-foreground: "#5F6763"
  primary: "#0B6A5C"
  success: "#41692A"
  warning: "#85540F"
  destructive: "#A83229"
  background-dark: "#1A2023"
  surface-dark: "#232B2F"
  foreground-dark: "#D9D3C7"
  muted-foreground-dark: "#8A9391"
  primary-dark: "#5FD3BC"
  success-dark: "#93C97E"
  warning-dark: "#E3B778"
  destructive-dark: "#E88A82"
  frame: "#F2ECE0"
  sheet: "#FBF7EF"
  sheet-edge: "color-mix(in oklch, #2E3A3D 11%, #FBF7EF)"
  rule: "color-mix(in oklch, #2E3A3D 8%, #FBF7EF)"
  border: "color-mix(in oklch, #2E3A3D 13%, #FBF7EF)"
  border-strong: "color-mix(in oklch, #2E3A3D 22%, #FBF7EF)"
  input: "color-mix(in oklch, #2E3A3D 16%, #FBF7EF)"
  secondary: "color-mix(in oklch, #2E3A3D 7%, #F2ECE0)"
  muted: "color-mix(in oklch, #2E3A3D 5%, #F2ECE0)"
  accent-wash: "color-mix(in oklch, #0B6A5C 10%, #FBF7EF)"
typography:
  headline:
    fontFamily: "IBM Plex Sans, Plex Sans Fallback, ui-sans-serif, system-ui, sans-serif"
    fontSize: "1.25rem"
    fontWeight: 600
    lineHeight: "1.75rem"
    letterSpacing: "-0.025em"
  title:
    fontFamily: "IBM Plex Sans, Plex Sans Fallback, ui-sans-serif, system-ui, sans-serif"
    fontSize: "1.125rem"
    fontWeight: 600
    lineHeight: "1.625rem"
    letterSpacing: "-0.025em"
  section:
    fontFamily: "IBM Plex Sans, Plex Sans Fallback, ui-sans-serif, system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 500
    lineHeight: "1.5rem"
  body:
    fontFamily: "IBM Plex Sans, Plex Sans Fallback, ui-sans-serif, system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: "1.25rem"
    fontFeature: "tnum"
  label:
    fontFamily: "IBM Plex Sans, Plex Sans Fallback, ui-sans-serif, system-ui, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: "1rem"
    fontFeature: "tnum"
  code:
    fontFamily: "IBM Plex Mono, Plex Mono Fallback, ui-monospace, SFMono-Regular, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: "1rem"
rounded:
  control: "8px"
  sheet: "16px"
  full: "9999px"
spacing:
  hairline: "1px"
  inset: "8px"
  tight: "12px"
  gutter: "16px"
  step: "20px"
  section: "24px"
  columns: "32px"
components:
  sheet:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.foreground}"
    rounded: "{rounded.sheet}"
    padding: "24px"
  card:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.foreground}"
    rounded: "{rounded.control}"
    padding: "12px 16px"
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.background}"
    typography: "{typography.body}"
    rounded: "{rounded.control}"
    padding: "8px 16px"
    height: "36px"
  button-outline:
    backgroundColor: "{colors.background}"
    textColor: "{colors.foreground}"
    typography: "{typography.body}"
    rounded: "{rounded.control}"
    padding: "0 12px"
    height: "32px"
  button-ghost:
    textColor: "{colors.foreground}"
    typography: "{typography.body}"
    rounded: "{rounded.control}"
    padding: "0 12px"
    height: "32px"
  input:
    textColor: "{colors.foreground}"
    typography: "{typography.body}"
    rounded: "{rounded.control}"
    padding: "4px 12px"
    height: "36px"
  nav-item:
    textColor: "{colors.muted-foreground}"
    typography: "{typography.body}"
    rounded: "{rounded.control}"
    padding: "0 10px"
    height: "36px"
  nav-item-current:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.foreground}"
  status-pill:
    textColor: "{colors.foreground}"
    typography: "{typography.label}"
    rounded: "{rounded.full}"
    padding: "2px 8px"
  chip:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.foreground}"
    typography: "{typography.code}"
    rounded: "{rounded.control}"
    padding: "2px 6px"
  run-row:
    textColor: "{colors.foreground}"
    typography: "{typography.body}"
    padding: "10px 12px"
  run-row-current:
    backgroundColor: "{colors.sheet}"
  step-number:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.foreground}"
    typography: "{typography.label}"
    rounded: "{rounded.full}"
    size: "24px"
  token-bar:
    backgroundColor: "{colors.rule}"
    rounded: "{rounded.full}"
    height: "6px"
    width: "192px"
---

# Design System: openarity dashboard

## Overview

**Creative North Star: "The Framed Sheet"**

The dashboard is a sheet of paper laid in a frame. The frame, in BRAND's stone surface, holds the sidebar and a 48px top bar on one ground with no rule between them; the work sits on a lighter sheet of BRAND's bg, inset 8px from the window's right and bottom, edged by one 1px line, with 16px corners and no shadow. Everything a reader came for is on the sheet; everything that moves them between sheets is on the frame. Below 768px the frame drops away and the sheet is the screen, with navigation behind a menu.

The voice is plain and exact, set in IBM Plex Sans at five sizes. Plex Mono appears only where a reader might copy or compare exact characters: ids, tool names, arguments and output. Colour is BRAND's Spectral palette and nothing else: warm graphite neutrals and one cool teal, with BRAND's ok, warn and error carrying status, always beside an icon and a word. Depth is tone, not shadow: the frame is one step darker than the sheet, a card one step darker than the sheet again.

The reference screen, the session and its run trace, carries the system's signature: a run read as a timeline of numbered steps on a rail, each step's tokens drawn as a bar against the run's largest step, tool calls folded to one line each, and a delegation nested under the call that made it. The system as recorded here is what that screen, the shell and the states gallery ship. Home, Sessions list, Approvals, Agents, MCP servers, Skills, Channels, Graph, Team settings, Teams, Deleted teams and Sign in sit inside the new shell but still carry their pre-redesign internals; they are not evidence for this system and should be brought into it, not copied from.

**Key Characteristics:**
- A frame and a sheet, told apart by tone and a 1px edge, never by shadow.
- Eight colour roles copied from BRAND.md; every other colour is a role or an oklch mix of roles.
- One typeface family, five sizes (12, 14, 16, 18, 20), tabular figures throughout.
- Teal is rare and means "act here", "you are here" or "this is running".
- Status is always icon plus word, never colour alone.
- Text from people, models and tools stays text: shown, never restyled into something else.

**Open, not decided.** The finish review shipped the reference screen with these left open; they are not rules to copy:
- The token bar can read as a progress bar.
- Section heads are body size (16px, and 14px inside a run), so the hierarchy leans on weight alone.
- There is no authored motion yet (Phase 7). What moves today is library default: the running spinner, chevrons that rotate on open, colour transitions, and the phone menu's fade and slide. All of it stops under reduced motion.
- The conversation is a card per message.

## Colors

Warm graphite paper with a single cool teal: BRAND's Spectral palette, copied into `src/styles.css` as `--spectral-*` and pinned by `src/test/palette.test.ts`.

### Primary
- **Spectral Teal** (light #0B6A5C, dark #5FD3BC): the primary action (a filled button), the selection (the current nav item's icon, the chosen run's 1px edge, selected text at 22%), the running state (its spinner, its Live badge, the growing step's bar), the keyboard focus ring and caret, and links inside running text.

### Status
- **Moss** (light #41692A, dark #93C97E): succeeded, healthy, open.
- **Ochre** (light #85540F, dark #E3B778): queued and waiting, idle, degraded.
- **Brick** (light #A83229, dark #E88A82): failed, refused, a destructive action. A failed run's error sits on an 8% wash of it.
- **Muted Slate** (light #5F6763, dark #8A9391): cancelled, closed, absent.

### Neutral
- **Paper** (background / sheet; light #FBF7EF, dark #1A2023): the sheet the work sits on.
- **Stone** (surface / frame; light #F2ECE0, dark #232B2F): the frame around the sheet, and the fill of cards, chips, messages and step numbers on the sheet.
- **Graphite** (foreground; light #2E3A3D, dark #D9D3C7): body text and headings. Headings are not teal in this dashboard.
- **Muted Slate** (muted-foreground): labels, metadata, secondary detail.
- **Hairlines**, all Graphite mixed into Paper in oklch: the rule (8% light, 10% dark) between rows and down the trace's rail; the sheet edge (11% light, 14% dark) around the sheet and the current nav item; the border (13%) and the strong border (22%) for form controls and dashed empty states; the input stroke (16%).
- **Washes**: secondary (Graphite 7% into Stone) and muted (5%) fills; the accent wash (Teal 10% light, 14% dark, into Paper) behind a row focused from the keyboard.

### Named Rules
**The Eight Roles Rule.** A colour enters the dashboard only as one of BRAND's eight roles or an oklch mix of them. A token pointing at a colour of its own is a bug, and the palette test is the gate. A change starts in BRAND.md.

**The Rare Teal Rule.** Teal is for what you act on, where you are, and what is running: the primary action, the selection, the running state, plus the focus ring and in-text links. A bar, a badge, an avatar or a heading in teal says "act here" where nothing is to be done.

**The Icon And Word Rule.** Every status is an icon and its word in the status colour. Colour is never the only signal.

## Typography

**Body Font:** IBM Plex Sans 400, 400 italic, 500, 600 (with "Plex Sans Fallback", local Arial metric-matched to Plex, then system sans)
**Label/Mono Font:** IBM Plex Mono 400, 500 (with "Plex Mono Fallback", local Courier New metric-matched)

**Character:** One engineered sans for everything a person reads, and its mono sibling only for exact characters, so the shift into mono itself says "this is an id, an argument, output". Both are served from the app as Latin-1 woff2 with `font-display: swap`, and the fallbacks are sized to Plex's metrics so nothing reflows when Plex arrives.

### Hierarchy
- **Headline** (600, 20px, 28px line, -0.025em): the page title, such as a session's name.
- **Title** (600, 18px, 26px line, -0.025em): the subject of a pane, such as the run's agent.
- **Section** (500, 16px, 24px line): the heads of the two halves of a page ("Conversation", "Runs"). Inside a run, section heads (Input, Answer, Trace) are 500 at 14px.
- **Body** (400, 14px, 20px line, tabular figures): messages, the run's input and answer, step and call names. Text from people and models is held to 75ch.
- **Label** (400, 12px, 16px line, tabular figures): breadcrumbs, metadata, timestamps, token counts, durations, status words. Sentence case, no tracking.
- **Code** (Plex Mono 400 or 500, 12px, 16px line; 1.625 line in output blocks): ids, tool names, argument chips, raw output. Names, emails, counts and paths are text, not mono.

### Named Rules
**The Five Sizes Rule.** 12, 14, 16, 18 and 20px, a 1.125 step, and no others. A size between them is drift, not nuance.

**The Mono Means Exact Rule.** Plex Mono marks characters a reader may copy or compare: ids, tool names, arguments, output. Anything a person would read aloud stays in Plex Sans.

**The Sentence Case Rule.** Every head and label is sentence case at its role's size. Uppercase tracked labels are refused.

## Layout

**The frame.** From 768px the shell is a two-column, two-row grid: a 228px sidebar spanning both rows, a 48px top bar, and the sheet in the remaining cell, inset 8px from the window's right and bottom edges. The sidebar and the top bar share the frame's ground with no line between them. Inside the sheet, content is padded 24px; below 768px the sheet is the whole screen, padded 16px across and 20px down, and the top bar becomes a sticky 48px strip with the menu button, the mark and the team picker.

**The reference screen.** A title row (breadcrumb, headline, a status line of state, kind and times joined by middle dots), then two halves. At 1280px and wider they sit side by side, conversation on the left (two fifths) and the run on the right (three fifths), 32px apart. Narrower, they stack behind a line of tabs (Messages, Runs) that the URL remembers.

**Rhythm.** 24px between major blocks of a page or a run (header, answer, trace); 12px between a section head and its content and between related lines; 20px between steps on the trace; 8px between messages. Rows pad 10-12px vertically and 12px across. Lists divide with a rule, not with gaps between cards.

**The timeline.** Each step is a 24px numbered circle on a 1px rule that runs down to the next, with the step's name, its token bar (112px track at 390, 192px from 640) and its count on one line. Tool calls under a step sit in one card, one line each, divided by rules; a delegation's child run nests under its call.

### Named Rules
**The Page Scrolls Rule.** The trace is read in the page's own scroll. A trace scrolled inside a box is refused; only a long list of runs (capped at 256px) and raw output (320-384px) scroll in place.

## Elevation & Depth

Flat. Depth is tonal: the frame (Stone) sits behind the sheet (Paper), and on the sheet a card (Stone again) is one step back from the sheet. Edges are 1px hairlines in oklch mixes of Graphite into Paper. Where a hairline must not take layout space (the current nav item, the chosen run, step numbers, argument chips, output blocks), it is drawn as an inset 1px ring rather than a border.

### Shadow Vocabulary
- **Inset hairline** (`box-shadow: inset 0 0 0 1px var(--sheet-edge)`, or `var(--rule)` on quieter parts, `var(--primary)` on the chosen run): an edge that costs no space.
- **Keyboard row focus** (`box-shadow: inset 3px 0 0 0 var(--primary)` on the accent wash): a list row focused from the keyboard.
- **Floating layers** (dialogs, menus, select popovers, the phone menu sheet over a 40% black scrim): the only things that cast a shadow, because they float above the sheet.

### Named Rules
**The No Shadow Sheet Rule.** The sheet, cards, rows and controls at rest cast no shadow. Only a layer that floats over the sheet may.

## Shapes

Two working radii: 8px on controls, cards, messages, chips and list containers; 16px on the sheet alone. Things that are points or tracks are fully round: status dots, the trace's step numbers, status and hub pills, the Live badge, and token bars. Borders are 1px hairlines; a dashed strong border marks an empty state. A folded long answer fades out over its last 30% rather than ending on a hard line.

## Components

### Buttons
Quiet, compact, one filled button at most per view.
- **Shape:** gently rounded (8px).
- **Primary:** Teal fill, Paper text, 14px 500, 36px high (32px small).
- **Outline:** Paper fill with the border hairline; the everyday secondary action (Cancel a running run, Load more).
- **Ghost:** no fill; hover lays a 6% Graphite wash (a neutral one: teal stays on the action, the selection and the run). Used in the frame (Sign out, theme toggle, menu) and for in-flow toggles ("Show the whole answer").
- **Focus:** the teal ring at 50%, 3px.
- **Destructive:** Brick fill for a destructive confirmation.

### Status
- **Inline:** a 14px icon in the status colour and the word in 12px (running spins; succeeded a checked circle; failed a crossed circle; cancelled a ban sign; queued a clock).
- **Pill:** the same inside a fully round 1px sheet-edge outline, beside a run's title.
- **Live:** a fully round teal badge, teal dot and the word "Live" on a 10% teal wash, while a run streams.

### Chips
- **Style:** Plex Mono 12px, 8px radius, 2px by 6px. A filled chip (Stone) names a capability the run used; a quiet chip (no fill, inset rule) names one it had but did not use, and carries an argument's key and value on a tool call.

### Cards / Containers
- **Corner Style:** 8px.
- **Background:** Stone on the Paper sheet.
- **Shadow Strategy:** none (see Elevation).
- **Border:** none; rows inside divide with the rule.
- **Internal Padding:** 12px by 16px for a message or the answer; 10px by 12px for a row.

### Inputs / Fields
- **Style:** transparent fill, the input hairline, 8px radius, 36px high, 16px text on a phone and 14px from 768px.
- **Focus:** the border turns teal and a 3px teal ring at 50% appears.
- **Error / Disabled:** invalid turns the border Brick with a 20% Brick ring; disabled drops to 50% opacity.

### Navigation
- **Sidebar:** 36px rows, 14px Muted Slate text and a 16px icon, 8px radius. Hover lays the sheet at 60%. The current item becomes a small piece of sheet (Paper fill, inset sheet-edge ring, 500 Graphite text) with its icon in teal.
- **Phone:** a menu button opens the same list in a 288px sheet from the left over a 40% scrim.
- **Tabs (narrow screens):** a line of tabs over a rule; the current tab is Graphite with a 2px Graphite underline, the rest Muted Slate. A count sits in a small Stone pill.

### Run row
A list of a session's runs in one Stone card, divided by rules: agent name, status, tokens and time, which wraps to two lines on a phone. The chosen run becomes Paper with a 1px inset teal ring.

### The Trace Timeline (signature)
Numbered steps on a rail. Each step's token bar is a 6px fully round track in the rule colour, filled in Graphite at 45% to its share of the run's largest step (at least 2%), or in teal while that step is the one running. Tool calls fold to one line each: a chevron, the tool name in mono, argument chips clipped to one line, and the duration at the right; a failed call opens itself on its error in Brick mono. A delegation shows the target agent, its objective in two lines, the child's status, its time, and a link to the child's run. Events that are not steps (a plan, a missing tool, the finish) sit on the rail as small muted lines with a dot or a warn icon.

## Do's and Don'ts

### Do:
- **Do** take every colour from the eight Spectral roles or an oklch mix of them, and change BRAND.md first.
- **Do** separate the frame from the sheet by tone and a 1px edge (sheet 16px corners, inset 8px).
- **Do** show every status as its icon and its word in the status colour.
- **Do** keep text to the five sizes (12, 14, 16, 18, 20) with tabular figures, and Plex Mono for ids, tool names, arguments and output only.
- **Do** draw quantities as length (tokens as bar length against the run's largest step) on a shared track so they compare.
- **Do** fold repeated detail to one line and open the failing item by itself.
- **Do** write user-facing text in plain words: no backend settings, environment variables or code identifiers.

### Don't:
- **Don't** put a shadow on the sheet, a card, a row or a control at rest.
- **Don't** use teal for decoration, headings, avatars, counts or bars that are not the running step.
- **Don't** set uppercase tracked labels or small kickers above headings.
- **Don't** render a card per event in a trace, or scroll a trace inside a box.
- **Don't** introduce a size between the five, a sixth radius, or a grey that is not a mix of the roles.
