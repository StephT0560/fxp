# Embed the FXP Console against one run

The existing React console supports a host-supplied connection alongside the
unchanged demo login. No sign-in, token issuer, run list or new trading UI is
added. Build with `cd web && npm ci && npm run build`; serve `web/dist` from a
trusted host with the CSP/framing policy required for that deployment.

## Injected configuration

Set this **in the console window before its module entrypoint executes**:

```ts
window.__FXP_CONSOLE__ = {
  gatewayUrl: "wss://gateway.example/runs/run-123/ws",
  token: connectToken, // short-lived JWT obtained by the trusted host
  runId: "run-123",
  readOnly: false,
};
```

These four fields suffice: no `/api/token`, `/api/users`, `/api/symbols` or other
demo HTTP request is made. The client sends the supplied token in the first
binary WebSocket Logon frame, subscribes to market data, and rebuilds the blotter
from the existing authenticated order-status protocol. The header displays the
run ID; the demo user switch/login, simulator and scenario/latency panels are
hidden by default. Protocol inspection remains available with credential
redaction. The run ID is a host label, not an authorization claim.

With neither an injected object nor an enabled parent handshake, the normal
demo login, token fetching, HTTP endpoints and controls behave as before.
Malformed injected configuration fails closed with a generic error instead of
silently authenticating a demo user.

| Field | Contract |
| --- | --- |
| `gatewayUrl` | Absolute `wss:` URL; local development also permits `ws:` on `localhost`, `127.0.0.1`, `[::1]`. No user-info, query, fragment, or token in the URL. Max 2048 characters. |
| `token` | JWT string, max 16 KiB, with positive integer `exp`. Backend validates signature/claims; browser decoding is only for expiry UX. |
| `runId` | 1–128 ASCII letters/digits/dots/underscores/hyphens, starting with a letter/digit. |
| `readOnly` | Required boolean. Hides ticket, cancel/replace/cancel-all and price-picking actions; the client also refuses all four mutations before optimistic updates or frame construction. |
| `symbols` (optional) | 1–64 unique `{symbol, price_scale, tick_size, lot_size?, min_quantity?, max_quantity?}` rules. Scale 0–9, positive int64 tick string, optional positive int32 quantities. |
| `apiUrl` (optional) | Trusted HTTPS API **base**, or HTTP loopback for development, without user-info/query/fragment. Paths such as `api/sim/start` append to this base, including its run prefix. |
| `features.demoControls` (optional) | Explicit opt-in to existing demo-compatible simulator/scenario/latency panels; also requires `apiUrl` and writable mode. Does not enable demo token fetching or user switching. |
| `notifyTokenExpired` (optional) | Opt in to the parent message below. Injected config additionally needs `parentOrigin` and a matching child-owned allowlist. |
| `parentOrigin` (optional) | Canonical exact parent origin for injected-mode expiry notification. Handshake mode uses the accepted sender's origin. |

Without symbol rules the existing standard simulator symbols (AAPL, MSFT,
GOOGL, AMZN, NVDA, TSLA, META, JPM, ETH/USD, BTC/USD) use two decimals and a
one-minor-unit tick. **Supply the actual rules for custom runs**: the gateway
has no HTTP symbol-catalog endpoint or symbol-discovery message. A viewer's
blotter contains the orders visible to its token's subject; this change does not
grant cross-owner/workspace order access.

## Parent postMessage handshake

For a cross-origin iframe, configure the **console document**, before loading
its module, with a trusted origin list:

```ts
window.__FXP_CONSOLE_ALLOWED_ORIGINS__ = ["https://sandbox.example"];
```

Do not derive this list from messages, query parameters or an untrusted referrer.
Use canonical origins including the port when non-default, with no path/trailing
slash. Wildcards, `null`/opaque origins and suffix matches are rejected. At most
16 origins are accepted. A configured iframe waits for its host without loading
the demo login or making demo HTTP calls.

1. Console posts `{type: "fxp-console:ready", version: 1, nonce}` to each allowed
   parent origin, never `"*"`. The nonce is random and scoped to this document.
2. Parent checks both `event.source === iframe.contentWindow` and the exact
   console origin, then replies with a matching nonce:

   ```ts
   iframe.contentWindow.postMessage({
     type: "fxp-console:init", version: 1, nonce: event.data.nonce,
     config: { gatewayUrl, token, runId, readOnly },
   }, "https://console.example");
   ```

3. Console accepts only the actual parent source, an exact allowed origin, the
   correct version/nonce, and valid bounded configuration. Wrong-origin/source,
   malformed and replayed init messages are ignored. The first accepted session
   is immutable; replace/reload the iframe for a new run or refreshed token.

If both mechanisms exist, the injected object wins. Without a configured
allowlist, messages do not switch a demo page into run mode.

## Expiry and authenticated HTTP

At JWT `exp`, rejected Logon, auth-denied HTTP (401/403), or terminal WS close
(1000/1008/4401/4403), run mode closes the connection, cancels retries and displays
**“Run ended or token expired”**. Transient transport loss uses bounded reconnect
backoff while the supplied token remains valid; it never fetches a demo token.
Expiry is checked before sends/reconnects as well as by a timer. This is client
UX, not a substitute for server-side revocation/permission enforcement.

With `notifyTokenExpired: true`, the console sends exactly once:

```ts
{ type: "fxp-console:token-expired", version: 1, runId: "run-123" }
```

The parent should validate source/origin again before calling its own
`onTokenExpired` handler. The message contains **no token**. Notifications go
only to the accepted parent origin (or the allowlisted `parentOrigin` for
injection), and cease if the parent navigates to another origin. There is no
automatic refresh protocol in this change.

Optional run HTTP requests use `Authorization: Bearer <token>`, `credentials:
omit`, no-store caching, and `redirect: error`; never query credentials. The host
API must implement compatible routes and authorize each operation, including
CORS for the console origin/Authorization header when cross-origin. A gateway
alone does not implement these demo HTTP APIs. Authenticated scenario SSE uses
bounded fetch streaming with the Bearer header rather than a query token;
expiry/disposal cancels the stream. Without `apiUrl`, no demo HTTP fallback or
FIX-translation HTTP request is attempted. Demo user switching stays unavailable
in run mode, even with demo controls enabled, because it would fetch demo tokens.

## Security model

- Token is kept in JS memory only. It is never written to localStorage,
  sessionStorage, cookies, URLs, logs or parent notifications. Injection is
  consumed and removed from the window property; the original trusted host may
  retain its own in-memory reference. The example clears its password input on
  connection and never interpolates a token into HTML or iframe URLs.
- Send a token only to trusted gateway/API endpoints. The browser must transmit
  it in WS Logon or an HTTP header; DevTools or trusted same-origin scripts can
  inspect it. Protect the host/console with HTTPS, restrictive CSP, trusted
  scripts and suitable `frame-ancestors`/`connect-src`. A compromised host or
  same-origin script is outside this frontend trust boundary.
- `readOnly` is an enforced **console behavior**, not server authorization.
  Existing gateway tokens do not gain a new read-only role here. A hostile user
  with a trading-capable token could use another client; the Sandbox/backend
  must issue/enforce appropriately restricted credentials for real viewers.
- Run identity, tenant isolation, token issuance, quotas and backend permission
  enforcement belong to the host/Runner. Message origin checks do not establish
  those backend guarantees. Config/data are parsed as bounded values, not code.
- The demo BFF deliberately retains `frame-ancestors 'none'` and same-origin
  WS checks. Serve a separate built console for embedding, or configure the
  real hosting edge's framing/connect policy; do not weaken the demo defaults.

## Static local example and tests

`examples/console-embed/index.html`, `host.js` and `bootstrap.js` are a static,
same-origin host example for both mechanisms. The fixed prelude asks the trusted
parent to set globals before the console module starts; no token is embedded in
markup. For production cross-origin use, the console server supplies its own
allowlist instead of reading a parent function.

Start the existing native core/gateway as documented in README (`make build`,
then `cargo run --release` in `fxp_core` and `go run .` in `go_gateway`), sharing a
fresh server-side `FXP_JWT_SECRET`. On gateway, set:

```bash
export FXP_GW_WS_ALLOWED_ORIGINS=http://127.0.0.1:3100
# In another terminal from the repo root, after npm ci in web:
make console-embed
```

The loopback-only server serves the example and built assets, not arbitrary
repository paths; traversal and escaping asset symlinks are refused. Paste a
short-lived token minted by your trusted local host into the password field,
choose injection or handshake, and connect to `ws://127.0.0.1:8081/ws`. Local
issuance can use SDK `dev_token(subject, ttl=900)` in memory with the same server
secret; do not log the token or put the secret in the host page. Run the existing
simday against that gateway to observe live tape/ladder updates. Current Chromium
may request local-network permission; the example delegates it to its iframe.

Against the existing `make demo` stack, the test hosts the **same static example
files** on its origin through Playwright file routes (no mocked token/market APIs):

```bash
cd web
npm run e2e -- e2e/embed.spec.ts
```

It asserts real ladder/tape/blotter data with only four injected fields, no demo
HTTP, absent read-only controls and order frames, actual foreign-origin message
rejection followed by trusted parent initialization, expiry callback, and no token
in URLs/storage/browser logs. The localhost browser context explicitly grants
Chromium local-network permission; credential-bearing traces/video are disabled
for these tests. Unit tests additionally isolate each origin/source/nonce check,
client mutation guard, expiry, authenticated HTTP and bounded SSE. The existing
demo suite remains required and runs alongside these tests.
