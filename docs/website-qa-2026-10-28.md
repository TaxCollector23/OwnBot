# Website QA · 2026-10-28

## Local static copy

Ran `PLAYWRIGHT_CHROMIUM_EXECUTABLE=… OWNBOT_QA_TARGET=local node demo-site/scripts/test-site.cjs` after the final UI edits. The corrected regression suite passed **172 assertions with zero failures**. Earlier exploratory ArrowRight seeks at 320px were removed because narrow native video controls do not consistently expose that interaction. The suite verifies playback at phone and desktop sizes, native timeline seeking at desktop width, and keyboard play/pause.

Passing coverage included 320/375/768/1440 page width and content checks; landing CTA and docs TOC anchors; the mobile Install link; missing-route and missing-asset 404 behavior; transcript access; copy feedback and clipboard content; keyboard skip link and focus outline; blocked Google Fonts fallback; video metadata, playback and pause; and local byte-range responses.

The source link was present in the page and its referenced recording script exists in the checkout. A separate request to the exact GitHub recording-source URL returned 200.

## Hosted site

After deployment to https://ownbot-demo.vercel.app, the hosted target passed **173 assertions with zero failures**. The live landing HTML, docs HTML, and `site.js` also matched the reviewed local files byte for byte. This verifies the production mobile navigation, copy controls, routes, responsive layouts, keyboard behavior, font fallback, transcript/source links, media byte ranges, and video playback covered by the suite. Native timeline seeking is checked at desktop width; narrow-screen seeking remains browser-dependent.

The reviewed website code was pushed as `a7789ae` to GitHub `main`. No model, runtime, provider account, or user daemon state was installed or modified for these website checks. Real provider/device/hardware qualification remains outside this website test suite.

## Reusable command

`PLAYWRIGHT_CHROMIUM_EXECUTABLE=/path/to/chromium OWNBOT_QA_TARGET=local node demo-site/scripts/test-site.cjs`

Omit `OWNBOT_QA_TARGET` to run hosted and local checks. The Playwright executable path is optional when installed in Playwright’s default location.
