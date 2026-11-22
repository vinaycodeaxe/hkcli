#!/usr/bin/env python3
"""Draw docs/assets/social-card.png, the preview image shown when the site is shared.

Run it again after changing the tagline: python scripts/make_social_card.py
Needs Pillow 10.1+ (uses Pillow's bundled font, so the output is the same on every OS).
"""

from __future__ import annotations

from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

OUT = Path(__file__).resolve().parent.parent / "docs" / "assets" / "social-card.png"
WIDTH, HEIGHT = 1200, 630
BG = (8, 47, 44)
ACCENT = (45, 212, 191)
WHITE = (240, 253, 250)
MUTED = (153, 214, 204)


def font(size: int) -> ImageFont.FreeTypeFont:
    return ImageFont.load_default(size=size)


def main() -> None:
    image = Image.new("RGB", (WIDTH, HEIGHT), BG)
    draw = ImageDraw.Draw(image)
    for r in range(760, 0, -8):  # soft radial glow from the top-left corner
        mix = 0.2 * (1 - r / 760)
        draw.ellipse((-r, -r, r, r), fill=tuple(int(BG[c] + (ACCENT[c] - BG[c]) * mix) for c in range(3)))

    # Logo: speech bubble with a terminal prompt, same shapes as favicon.svg (scaled 3x).
    x, y, s = 90, 110, 3
    draw.rounded_rectangle((x, y, x + 64 * s, y + 64 * s), radius=14 * s, fill=(15, 118, 110))
    draw.rounded_rectangle((x + 9 * s, y + 13 * s, x + 55 * s, y + 44 * s), radius=8 * s, fill=WHITE)
    draw.polygon([(x + 17 * s, y + 42 * s), (x + 17 * s, y + 53 * s), (x + 28 * s, y + 43 * s)], fill=WHITE)
    draw.line([(x + 19 * s, y + 23 * s), (x + 26 * s, y + 28.5 * s), (x + 19 * s, y + 34 * s)], fill=(15, 118, 110), width=4 * s, joint="curve")
    draw.line([(x + 30 * s, y + 35 * s), (x + 43 * s, y + 35 * s)], fill=(15, 118, 110), width=4 * s)

    draw.text((90, 330), "hikecli", font=font(110), fill=WHITE)
    draw.text((94, 460), "Hike in your terminal", font=font(46), fill=ACCENT)
    draw.text((94, 530), "Sync  \u00b7  Search  \u00b7  Send  \u00b7  OpenClaw skill", font=font(30), fill=MUTED)
    draw.text((WIDTH - 90, 560), "vinaycodeaxe.github.io/hikecli", font=font(24), fill=MUTED, anchor="ra")

    OUT.parent.mkdir(parents=True, exist_ok=True)
    image.save(OUT, optimize=True)
    print(f"wrote {OUT} ({OUT.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
