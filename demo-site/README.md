# ownbot demo site

Static landing page, guides, and two videos. Live: <https://ownbot-demo.vercel.app>.

- `index.html`: product landing; `demos/index.html`: recordings and guides.
- `videos/demo-terminal.cast`: actual CLI smoke-test transcript, recorded with disposable state. No model was installed or invoked. It does not demonstrate AI task quality.
- `scripts/demo-session.sh`: repeat that session against the built `bin/ownbot` and `bin/ownbotd`. Port 19065 must be free.
- `scripts/render-terminal.py`: replay the cast into MP4 with Pillow and ffmpeg; pass `--font /path/to/monospace.ttf` outside macOS.
- `flyover/`: animation source and configuration from [landing-flyover](https://github.com/arjunkshah12345-hash/landing-flyover). The MP4 is a page animation, not an execution recording. No music is included.
- Project QA: [daemon report](../docs/qa-2026-10-28.md) and [website report](../docs/website-qa-2026-10-28.md).

Serve locally from this folder with `python3 -m http.server 4173`. Open `/` or `/demos/`.

To refresh the terminal recording from the repository root:

```sh
asciinema rec --headless --overwrite --return --window-size 100x32 \
  --output-format asciicast-v2 \
  --command 'bash demo-site/scripts/demo-session.sh' \
  demo-site/videos/demo-terminal.cast
python3 demo-site/scripts/render-terminal.py \
  demo-site/videos/demo-terminal.cast demo-site/videos/demo-terminal.mp4
```

To refresh the flyover, serve this folder, run `npm ci` in `flyover/`, and set `config.json`'s `url` to the local server. Follow the [skill's capture, preview, and rendering instructions](https://github.com/arjunkshah12345-hash/landing-flyover). Captures, dependencies, and intermediate renders stay out of Git.

Deploy only this folder: `vercel deploy --prod --cwd demo-site`. It hosts static pages and videos, never the local daemon or its private data.

Website regression checks (uses the repository’s Playwright dependency and an installed Chromium):

```sh
OWNBOT_QA_TARGET=local node demo-site/scripts/test-site.cjs
OWNBOT_QA_TARGET=hosted node demo-site/scripts/test-site.cjs
```

Set `PLAYWRIGHT_CHROMIUM_EXECUTABLE` if Chromium is outside Playwright’s default cache. No model or daemon is needed.
