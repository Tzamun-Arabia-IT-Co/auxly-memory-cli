#!/usr/bin/env python3
"""Prepare a brand logo for terminal rendering: key out a solid background to
transparent (so it doesn't render as a colored box) and autocrop to the logo's
content box (so it fills the frame at tiny sizes). Used by gen-brand-logos.sh.

Usage: prep-logo.py <input> <output.png>
"""
import sys
from PIL import Image

inp, outp = sys.argv[1], sys.argv[2]
im = Image.open(inp).convert("RGBA")

# If the image is fully opaque, treat the top-left pixel as the background and
# key out everything close to it.
if im.getchannel("A").getextrema()[0] == 255:
    bg = im.getpixel((0, 0))[:3]
    tol = 32
    px = im.load()
    w, h = im.size
    for y in range(h):
        for x in range(w):
            r, g, b, a = px[x, y]
            if abs(r - bg[0]) <= tol and abs(g - bg[1]) <= tol and abs(b - bg[2]) <= tol:
                px[x, y] = (r, g, b, 0)

# Trim transparent margins so the mark fills the rendered cells.
bbox = im.getchannel("A").getbbox()
if bbox:
    im = im.crop(bbox)

# Pad to a centered square so chafa preserves the logo's aspect (no stretching)
# and every brand renders in the same footprint.
side = max(im.size)
canvas = Image.new("RGBA", (side, side), (0, 0, 0, 0))
canvas.paste(im, ((side - im.width) // 2, (side - im.height) // 2))
canvas.save(outp)
