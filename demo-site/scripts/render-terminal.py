#!/usr/bin/env python3
"""Render our asciicast-v2 recording to MP4. Requires Pillow and ffmpeg.

The session uses plain text plus clear-screen/home controls. Unsupported cursor
controls fail rather than silently presenting an inaccurate terminal replay.
"""
import argparse
import json
import math
import os
from pathlib import Path
import re
import subprocess

from PIL import Image, ImageDraw, ImageFont

parser = argparse.ArgumentParser()
parser.add_argument("cast", type=Path)
parser.add_argument("output", type=Path)
parser.add_argument("--font", default=os.environ.get("OWNBOT_DEMO_FONT", "/System/Library/Fonts/Menlo.ttc"))
args = parser.parse_args()
records = [json.loads(line) for line in args.cast.read_text().splitlines()]
header, events = records[0], records[1:]
if header.get("version") != 2:
    raise SystemExit("Record with asciinema --output-format asciicast-v2")
columns, rows = header["width"], header["height"]
font = ImageFont.truetype(args.font, 20)
small = ImageFont.truetype(args.font, 17)
width, height, fps = 1280, 960, 10
screen, row, col, pending = [""] * rows, 0, 0, ""


def apply(text):
    global screen, row, col, pending
    text = pending + text
    pending = ""
    index = 0
    while index < len(text):
        char = text[index]
        if char == "\x1b":
            match = re.match(r"\x1b\[([0-9;?]*)([A-Za-z])", text[index:])
            if not match:
                pending = text[index:]
                return
            value, code = match.groups()
            if code == "J" and value == "2":
                screen = [""] * rows
            elif code in ("H", "f") and value in ("", "1;1"):
                row, col = 0, 0
            elif code != "m":
                raise ValueError(f"Unsupported terminal control: {match.group()!r}")
            index += len(match.group())
            continue
        if char == "\r":
            col = 0
        elif char == "\n":
            row += 1
            if row >= rows:
                screen.pop(0)
                screen.append("")
                row = rows - 1
        elif char == "\t":
            col = (col // 8 + 1) * 8
        elif char.isprintable():
            if col >= columns:
                apply("\r\n")
            line = screen[row].ljust(col)
            screen[row] = line[:col] + char + line[col + 1:]
            col += 1
        index += 1


duration = max(event[0] for event in events) + 1
command = ["ffmpeg", "-y", "-v", "error", "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", f"{width}x{height}", "-r", str(fps), "-i", "-", "-an", "-c:v", "libx264", "-preset", "fast", "-crf", "18", "-pix_fmt", "yuv420p", "-movflags", "+faststart", str(args.output)]
encoder = subprocess.Popen(command, stdin=subprocess.PIPE)
position, frame_bytes = 0, None
try:
    for frame in range(math.ceil(duration * fps)):
        changed = frame_bytes is None
        while position < len(events) and events[position][0] <= frame / fps:
            _, kind, text = events[position][:3]
            if kind == "o":
                apply(text)
                changed = True
            position += 1
        if changed:
            canvas = Image.new("RGB", (width, height), "#121212")
            draw = ImageDraw.Draw(canvas)
            draw.text((32, 24), "ownbot / CLI smoke test / no model installed", font=small, fill="#b6b8c4")
            draw.line((32, 58, width - 32, 58), fill="#393939")
            for y, line in enumerate(screen):
                color = "#9aafff" if line.startswith("$") else "#bbbcb5" if line.startswith("#") else "#f3f2ee"
                draw.text((32, 78 + y * 26), line, font=font, fill=color)
            frame_bytes = canvas.tobytes()
        encoder.stdin.write(frame_bytes)
finally:
    encoder.stdin.close()
if encoder.wait() != 0:
    raise SystemExit("ffmpeg failed")
if pending:
    raise SystemExit("Incomplete terminal control sequence")
print(f"Rendered actual terminal recording: {duration:.1f}s")
