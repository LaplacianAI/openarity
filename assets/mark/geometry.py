"""The mark's numbers, once. build.py writes the SVGs from them and intro.py
animates them; neither carries a number of its own.

Units are a 64-unit box, y down. The cut runs top-left to bottom-right along
y = x, never the other way: the other diagonal is the "no" slash.
"""

BOX = 64

LIGHT = "#0B6A5C"  # BRAND.md accent, light
DARK = "#5FD3BC"  # BRAND.md accent, dark
TEXT_LIGHT = "#2E3A3D"
TEXT_DARK = "#D9D3C7"

# The mark: a squircle, a visor, a diagonal cut, and two nodes on the cut.
MARK = {
    "body": (8.0, 8.0, 48.0, 14.9),  # x, y, side, corner radius
    "visor": (26.0, 17.0, 3.0),  # width, height, corner radius; centred
    "cut": 3.0,  # gutter width
    "nodes": (3.1, 14.3),  # radius; distance from the centre along each axis
}

# The favicon: the same drawing re-weighted for 16 px. The body fills more of
# the box, and the cut and the nodes are heavier: at the mark's own sizes the
# nodes are under a pixel at 16 px; at these they are a notch at 32 and still
# widen the cut at 16.
FAVICON = {
    "body": (4.0, 4.0, 56.0, 17.4),
    "visor": (30.3, 19.8, 3.5),
    "cut": 5.0,
    "nodes": (4.8, 16.6),
}
