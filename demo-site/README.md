# ownbot documentation site

Static documentation at <https://ownbot-demo.vercel.app>. The root and `/demos` retain the same guides and anchors for existing links. No videos are embedded.

- `index.html` and `demos/index.html`: installation, models, SSH/LAN access, approvals, and diagnostics.
- `site.js`: browser-local search and command copying.
- Historical recording sources remain in Git; they are not part of the documentation experience.

Serve this folder with `python3 -m http.server 4173`.

Run website checks with the repository's Playwright dependency and an existing Chromium:

```sh
OWNBOT_QA_TARGET=local node demo-site/scripts/test-site.cjs
OWNBOT_QA_TARGET=hosted node demo-site/scripts/test-site.cjs
```

Set `PLAYWRIGHT_CHROMIUM_EXECUTABLE` if needed. No model is required.

Deploy only this static folder with `vercel deploy --prod --cwd demo-site`.
