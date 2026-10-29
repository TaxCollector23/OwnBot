# ownbot demo handoff

The demo website is maintained in `demo-site/` in this repository. It replaces the earlier local-only demo source.

- Live site: https://ownbot-demo.vercel.app
- Demos and guides: https://ownbot-demo.vercel.app/demos
- README starts with links to the sources.
- Current CLI recording: disposable daemon, no models installed or invoked. Transcript and recording/render scripts are included.
- Flyover: refreshed landing, approved preview, 14.5 seconds at 60 fps. Animation engine and MIT license are included.
- QA: `docs/qa-2026-10-28.md`; Go and dashboard regressions pass, browser suite 15 passed / 3 macOS LAN skips.
- This completes the demo-site handoff, not the full product release gate. Live provider/device/hardware and signed-update validation remain in `TODO.md` and `STATUS.md`.

See `demo-site/README.md` for serving, recording, rendering, and deployment. Deploy only that static folder; never copy daemon state or credentials into it.
