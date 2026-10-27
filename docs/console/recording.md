# Regenerate the console presentation

The [three-beat script](demo-script.md) is implemented by
[`web/record/demo.spec.ts`](../../web/record/demo.spec.ts), using the separate
[`playwright.record.config.ts`](../../web/playwright.record.config.ts).
It uses real gateway sessions and the existing BFF, not mocked frames or data.

## Prerequisites

- Docker with Compose >=2.24.4 and Python 3 for `make demo`.
- Node.js 22+ and npm on the recording host; install the pinned dependencies and
  Playwright Chromium (including its video encoder):

  ```bash
  cd web
  npm ci
  npx playwright install --with-deps chromium
  cd ..
  ```

- An idle, disposable local demo stack, with no scenario running or other traders
  using Alice/Bob. Start it with `make demo` and wait for the health checks.
  For a clean take after previous experiments, use `make demo-down && make demo`.
  Do not run this concurrently with `npm run e2e`.

## Record and capture stills

From the repository root, with the Compose stack already up:

```bash
make record
# Equivalent: cd web && npm run record
```

The harness records only Alice's 1920×1080 browser context. Bob acts in a separate
context with no `recordVideo` setting and never opens another recorded window.
A 60 ms Playwright `slowMo` makes interaction visible; explicit presentation
holds aim for ~75 seconds total. Assertions gate the auction print, partial fill,
replacement, own-order marker, authoritative FIX comparison and both ACK series.
A slow host may take longer; failed assertions fail the take, with retries off.

Output:

| File | Purpose |
|---|---|
| `web/recordings/fxp-console.webm` | Stable video filename, written after Alice's context closes |
| `web/recordings/raw/*.webm` | Playwright's original Alice video (including a failed take) |
| `web/recordings/results/` | Isolated recording test output |
| `docs/media/console-auction.png` | Opening-call ladder/tape and visible countdown |
| `docs/media/console-partial-fill.png` | Partial blotter row, replaced remainder and own ladder marker |
| `docs/media/console-protocol.png` | Same order's decoded FXP, raw hex and FIX 4.4 translation |

All videos and test output stay under gitignored `web/recordings/`. Only the
three PNG stills are intended for commit. Chromium writes compressed PNGs at CSS
scale (no HiDPI upscaling); the harness rejects any still over **250,000 bytes**.
Inspect them after UI changes before committing. Failed takes can leave partial
stills: commit only after the complete harness passes.

The seed is **42**, opening call **10 seconds**, simulation duration **120
seconds**, with the UI's default ten symbols/twenty participants. `DEMO` is never
traded by the simulator. Two 25-lot at-open orders guarantee an auction print;
Bob then sells 40 and Alice buys 100, leaving 60 for replacement. The simulator
is stopped after the auction to retain the relevant frames in the bounded
inspector history. Existing Alice/Bob orders are canceled at the start; remaining
demo orders belong to the disposable stack. The simulator is stopped and both
contexts are closed even if a step fails.

The random seed does not freeze scheduling, timestamps or client order IDs.
This is a reproducible sequence of asserted outcomes, not a byte-identical film.
No audio is recorded; use the script as a voiceover guide. No credentials, token
responses or browser traces are exported by the harness.

`FXP_CONSOLE_URL` can select another **local** BFF origin; the default is
`http://127.0.0.1:3000`. `FXP_CONSOLE_CHROMIUM` optionally selects a system Chromium
executable; Playwright's video encoder must still be installed. The harness does
not start services, change application code, or publish artifacts.

## Optional MP4 conversion

If `ffmpeg` with libx264 is installed, from the repository root:

```bash
ffmpeg -i web/recordings/fxp-console.webm \
  -c:v libx264 -crf 20 -preset medium -pix_fmt yuv420p \
  -movflags +faststart -an web/recordings/fxp-console.mp4
```

Without ffmpeg, retain the WebM or convert it later on another machine. Do not
commit either format. Upload the final MP4 to GitHub and replace the README's
video placeholder manually. Publishing a prebuilt demo image is also a separate,
optional maintainer step; this harness publishes nothing.

The video placeholder is an HTML comment in the root README Demo section; the maintainer should replace that comment with the uploaded video link.
