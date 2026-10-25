# TLS and trusted core forwarding

## Deployment

Gateway TCP, gateway WebSocket and FIX bridge share server settings:
`FXP_TLS_CERT_FILE`, `FXP_TLS_KEY_FILE`, optional `FXP_TLS_CLIENT_CA_FILE`, and
`FXP_TLS_REQUIRED=true`. Certificates and keys are PEM. The leaf must be currently
valid, match the key and contain the server's DNS/IP SAN. TLS uses Go's secure
cipher defaults, minimum TLS 1.2, negotiating TLS 1.3 when available. No insecure
client verification switch is provided. Client CA configuration requires a valid
client certificate on every listener, including WebSocket upgrades.

For a throwaway local setup:

```sh
bash scripts/gen_test_certs.sh certs
export FXP_TLS_CERT_FILE=/etc/fxp/certs/server.crt
export FXP_TLS_KEY_FILE=/etc/fxp/certs/server.key
export FXP_TLS_CLIENT_CA_FILE=/etc/fxp/certs/ca.crt
export FXP_TLS_REQUIRED=true
export FXP_GATEWAY_TLS=true
export FXP_GATEWAY_TLS_CA_FILE=/etc/fxp/certs/ca.crt
export FXP_GATEWAY_TLS_CERT_FILE=/etc/fxp/certs/client.crt
export FXP_GATEWAY_TLS_KEY_FILE=/etc/fxp/certs/client.key
export FXP_GATEWAY_TLS_SERVER_NAME=localhost
export FXP_CORE_AUTH_TOKEN="$(openssl rand -hex 32)"
# Also set production JWT and FIX credentials, then docker compose up --build.
```

Compose mounts `certs/` read-only under `/etc/fxp/certs` in gateway and bridge.
The image runs as UID 10001: arrange owner/group permissions so that user can read
keys while other users cannot. The helper creates owner-only throwaway keys and
two-day certificates; use a managed CA and appropriate permissions in production.
Do not commit keys; `certs/` and generated `target/` fixtures are ignored.

## Reload

A one-second file-content watcher reloads the certificate, private key and client
CA bundle together into an immutable snapshot. Atomic file replacement works.
An invalid/mismatched/expired replacement logs a warning and retains the last
valid snapshot; it never downgrades to plaintext. Replace the whole pair promptly
when changing keys. New handshakes use the updated snapshot. Established sessions
keep their original authentication until disconnected. mTLS session tickets are
disabled so new handshakes revalidate against a rotated client CA. Revoking a CA
does not terminate already established connections. Paths and enabling TLS itself
are startup settings and require restart.

## Clients

Go TCP/WS clients, benchmark and Python FIX client accept `--tls`, `--tls-ca`,
`--tls-cert`, `--tls-key` and `--tls-server-name`. Equivalent defaults come from
`FXP_CLIENT_TLS`, `FXP_CLIENT_TLS_CA_FILE`, `FXP_CLIENT_TLS_CERT_FILE`,
`FXP_CLIENT_TLS_KEY_FILE`, `FXP_CLIENT_TLS_SERVER_NAME`. TLS settings without TLS
being enabled fail. Python additionally accepts `--no-tls`; the WS client's
`wss://` URL enables TLS. Hostname verification and trusted CA verification remain
mandatory. Unset CA uses system roots. Certificate and key must be supplied as a
pair. For example:

```sh
python3 fix_test_client/fix_client.py --tls --tls-ca certs/ca.crt   --tls-cert certs/client.crt --tls-key certs/client.key --tls-server-name localhost
```

The bridge's separate upstream settings use `FXP_GATEWAY_TLS*`, as listed in the
README. It must use TLS when the gateway does; client credentials must be supplied
if the gateway requires mTLS. Its FIX listener configuration is independent.

## Core connection protocol

The core always binds `127.0.0.1:8080`, rejecting non-loopback listeners even when
a token is enabled. With `FXP_CORE_AUTH_TOKEN` unset/empty, the existing FXP wire
protocol is unchanged. When set, each new connection must send eight ASCII bytes
`FXPAUTH1`, a two-byte unsigned big-endian token byte length (1–1024), and the UTF-8
token bytes. Success returns one byte `0x01`; FXP framing follows. Wrong/missing
credentials close the connection with generic diagnostics before any sender or
order is parsed. Authentication is bounded by five seconds and shutdown. Gateway
pool creation and every reconnect perform this handshake. No application schema
or version change is needed. There is no fallback to unauthenticated FXP when a
configured handshake fails, and configuring only one side fails closed.

The bridge uses the gateway and does not receive the core token. The Docker core
health check authenticates when configured without printing the secret. Direct
core tools must implement the preface; the persistence test client does so.
See SECURITY.md for local attacker limitations: this token does not encrypt the
loopback link or authenticate a core server to the gateway.

## Verification and future work

Run `bash scripts/e2e_tls.sh` with the normal build dependencies and OpenSSL. It
creates temporary CA/server/client material, exercises native TCP and same-origin
wss, rejects wrong CAs/missing client certificates and unauthenticated core orders,
rotates all listener certificates live, then runs the existing FIX, order-type,
restart/persistence suite and benchmark smoke over mTLS. CI runs both plaintext
and TLS variants. Unit tests additionally cover expired peers, TLS version floor,
plaintext to a TLS listener, CA rotation and retention of invalid replacements.

Asymmetric JWT/JWKS remains a design discussion: a future change would need issuer
and audience enforcement, an explicit algorithm allow-list, bounded key caching,
rotation overlap and fail-closed unknown-key behavior. It is not implemented here.
