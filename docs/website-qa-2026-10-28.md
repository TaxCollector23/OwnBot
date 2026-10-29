# Website QA · 2026-10-28

## Local static copy

Ran `PLAYWRIGHT_CHROMIUM_EXECUTABLE=… OWNBOT_QA_TARGET=local node demo-site/scripts/test-site.cjs` after the final UI edits. The corrected regression suite passed **172 assertions with zero failures**. Earlier exploratory ArrowRight seeks at 320px were removed because narrow native video controls do not consistently expose that interaction. The suite verifies playback at phone and desktop sizes, native timeline seeking at desktop width, and keyboard play/pause.

Passing coverage included 320/375/768/1440 page width and content checks; landing CTA and docs TOC anchors; the mobile Install link; missing-route and missing-asset 404 behavior; transcript access; copy feedback and clipboard content; keyboard skip link and focus outline; blocked Google Fonts fallback; video metadata, playback and pause; and local byte-range responses.

The source link was present in the page and its referenced recording script exists in the checkout. A separate request to the exact GitHub recording-source URL returned 200.

## Hosted site

The existing deployment was pre-change during this QA. Baseline route checks showed `/demos` returns 200, `/demos/` redirects with 308 to `/demos`, missing paths/assets return non-HTML 404s, and the transcript/logo load. The 320px hosted page still hid the mobile Install nav link, matching the deployed older CSS. No full hosted pass is claimed for the local UI revision; run the hosted target after deployment.

## Reusable command

`PLAYWRIGHT_CHROMIUM_EXECUTABLE=/path/to/chromium OWNBOT_QA_TARGET=local node demo-site/scripts/test-site.cjs`

Omit `OWNBOT_QA_TARGET` to run hosted and local checks. The Playwright executable path is optional when installed in Playwright’s default location.
