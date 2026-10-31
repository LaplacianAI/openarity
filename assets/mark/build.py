"""Write every copy of the mark from geometry.py. Standard library only.

    python3 assets/mark/build.py

The cut, the visor and the nodes are a mask over one solid body rather than
shapes in the background colour, so the page shows through them on any
background, and nothing is a stroked outline: a CSS fill rule overriding
fill="none" is what turned the previous mark's diagonals into solid blades.
"""

import pathlib

from geometry import BOX, DARK, FAVICON, LIGHT, MARK, TEXT_DARK, TEXT_LIGHT

ROOT = pathlib.Path(__file__).resolve().parents[2]

STYLE = f"""  <style>
    .mark {{ fill: {LIGHT}; }}
    @media (prefers-color-scheme: dark) {{
      .mark {{ fill: {DARK}; }}
    }}{{wordmark}}
  </style>"""

WORDMARK_STYLE = f"""
    .wordmark {{ fill: {TEXT_LIGHT}; }}
    @media (prefers-color-scheme: dark) {{
      .wordmark {{ fill: {TEXT_DARK}; }}
    }}"""

NOTE = """  <!-- The openarity mark: a squircle O cut once on the diagonal into two
       agents, with a visor and two nodes on the cut - an agent that is a
       pair, and a graph edge between them. Written by assets/mark/build.py
       from geometry.py; edit those, not this file. See BRAND.md.

       The theme switch is inside the file, not currentColor: an SVG in an
       <img> is a separate document and inherits no colour, so currentColor
       renders black, and on GitHub in dark mode that is black on black. -->"""


def n(v):
    return f"{v:g}"


def mask(mid, g):
    x, y, side, r = g["body"]
    w, h, vr = g["visor"]
    c = BOX / 2
    parts = [
        f'    <rect x="{n(x)}" y="{n(y)}" width="{n(side)}" height="{n(side)}" rx="{n(r)}" fill="#fff"/>',
        f'    <rect x="{n(c - w / 2)}" y="{n(c - h / 2)}" width="{n(w)}" height="{n(h)}" rx="{n(vr)}" fill="#000"/>',
        f'    <path d="M0 0L{BOX} {BOX}" stroke="#000" stroke-width="{n(g["cut"])}"/>',
    ]
    if g["nodes"]:
        nr, at = g["nodes"]
        for p in (c - at, c + at):
            parts.append(f'    <circle cx="{n(p)}" cy="{n(p)}" r="{n(nr)}" fill="#000"/>')
    return (
        f'  <mask id="{mid}" maskUnits="userSpaceOnUse" x="0" y="0" width="{BOX}" height="{BOX}">\n'
        + "\n".join(parts)
        + "\n  </mask>"
    )


def svg(width, mid, g, extra=""):
    style = STYLE.replace("{wordmark}", WORDMARK_STYLE if extra else "")
    return f"""<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {width} {BOX}" role="img" aria-label="openarity">
  <title>openarity</title>
{NOTE}
{style}
{mask(mid, g)}
  <rect class="mark" width="{BOX}" height="{BOX}" mask="url(#{mid})"/>{extra}
</svg>
"""


WORD = """
  <text class="wordmark" x="72" y="41"
        font-family="ui-sans-serif, -apple-system, 'Segoe UI', Inter, Helvetica, Arial, sans-serif"
        font-size="28" font-weight="600" letter-spacing="-0.5">openarity</text>"""

FILES = {
    "logo.svg": svg(BOX, "oa-logo-cut", MARK),
    "favicon.svg": svg(BOX, "oa-favicon-cut", FAVICON),
    "logo-wordmark.svg": svg(232, "oa-wordmark-cut", MARK, WORD),
}

COPIES = {
    "logo.svg": ["assets", "site/static/images"],
    # The site root, not images/: the docs theme asks for /favicon.svg there.
    "favicon.svg": ["assets", "site/static", "apps/dashboard/public"],
    "logo-wordmark.svg": ["assets", "site/static/images"],
}

if __name__ == "__main__":
    for name, body in FILES.items():
        for folder in COPIES[name]:
            (ROOT / folder / name).write_text(body)
            print(f"wrote {folder}/{name}")
