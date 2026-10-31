"""Write the docs site's raster icons from geometry.py.

    python3 assets/mark/raster.py      # needs Pillow: pip install pillow

Only what an SVG cannot be: the icon iOS puts on a home screen, and the two a
web manifest asks for, beside the manifest itself. The tab icon stays the SVG,
which follows the reader's theme; site/overrides/main.html links these, after
the theme's own favicon link, so none of them is a rel="icon" that could win
over the SVG.

They sit on the light background, since iOS fills transparency with black.
"""

import json
import pathlib

from PIL import Image, ImageDraw

from geometry import BOX, LIGHT, MARK

ROOT = pathlib.Path(__file__).resolve().parents[2]
SITE = ROOT / "site/content/assets"
PAPER = "#FBF7EF"  # BRAND.md bg, light
SS = 8  # supersampling, for clean edges


def render(g, px, background=None):
    size = px * SS
    k = size / BOX
    mask = Image.new("L", (size, size), 0)
    d = ImageDraw.Draw(mask)
    x, y, side, r = g["body"]
    d.rounded_rectangle([x * k, y * k, (x + side) * k, (y + side) * k], radius=r * k, fill=255)
    w, h, vr = g["visor"]
    c = BOX / 2
    d.rounded_rectangle([(c - w / 2) * k, (c - h / 2) * k, (c + w / 2) * k, (c + h / 2) * k], radius=vr * k, fill=0)
    d.line([(0, 0), (size, size)], fill=0, width=round(g["cut"] * k))
    if g["nodes"]:
        nr, at = g["nodes"]
        for p in (c - at, c + at):
            d.ellipse([(p - nr) * k, (p - nr) * k, (p + nr) * k, (p + nr) * k], fill=0)
    image = Image.new("RGBA", (size, size), background or (0, 0, 0, 0))
    image.paste(Image.new("RGBA", (size, size), LIGHT), mask=mask)
    return image.resize((px, px), Image.LANCZOS)


if __name__ == "__main__":
    render(MARK, 180, PAPER).save(SITE / "apple-touch-icon.png")
    for px in (192, 512):
        render(MARK, px, PAPER).save(SITE / f"icon-{px}.png")
    manifest = {
        "name": "openarity",
        "short_name": "openarity",
        "icons": [{"src": f"icon-{px}.png", "sizes": f"{px}x{px}", "type": "image/png"} for px in (192, 512)],
        "theme_color": PAPER,
        "background_color": PAPER,
        "display": "standalone",
    }
    (SITE / "site.webmanifest").write_text(json.dumps(manifest, indent=2) + "\n")
    print("wrote", ", ".join(sorted(p.name for p in SITE.iterdir() if p.is_file())))
