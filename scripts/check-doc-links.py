#!/usr/bin/env python3
"""Check local links in the static documentation site and repository Markdown."""
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlparse

ROOT = Path(__file__).resolve().parents[1]


class Links(HTMLParser):
    def __init__(self):
        super().__init__()
        self.hrefs = []

    def handle_starttag(self, tag, attrs):
        for name, value in attrs:
            if name in ("href", "src") and value:
                self.hrefs.append(value)


errors = []
for html in (ROOT / "docs-site").rglob("*.html"):
    parser = Links()
    parser.feed(html.read_text())
    for href in parser.hrefs:
        parsed = urlparse(href)
        if parsed.scheme or href.startswith("#"):
            continue
        target = (html.parent / unquote(parsed.path)).resolve()
        if not target.exists():
            errors.append(f"{html.relative_to(ROOT)}: missing {href}")

for source in [ROOT / "README.md", *sorted((ROOT / "docs").glob("*.md"))]:
    for line_no, line in enumerate(source.read_text().splitlines(), 1):
        start = 0
        while True:
            start = line.find("](", start)
            if start < 0:
                break
            end = line.find(")", start + 2)
            if end < 0:
                break
            target = line[start + 2:end].split("#", 1)[0]
            parsed = urlparse(target)
            if target and not parsed.scheme and not target.startswith("//"):
                path = (source.parent / unquote(target)).resolve()
                if not path.exists():
                    errors.append(f"{source.relative_to(ROOT)}:{line_no}: missing {target}")
            start = end + 1

if errors:
    print("\n".join(errors))
    raise SystemExit(1)
print("Documentation links: OK")
