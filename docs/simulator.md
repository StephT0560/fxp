# Trading-day simulator

From `go_gateway/`, build `go build -o ../bin/simday ./simulation`, or run
`go run simulation/trading_day.go`. Start core and gateway first.

Supply `FXP_JWT_SECRET` through the environment, matching the gateway. It must
contain at least 24 **bytes** (the gateway's shared minimum). There is no CLI
secret option and no development-secret fallback. Neither the secret nor JWTs
are printed. `devjwt.Generate` supplies `FXP_JWT_ISSUER` (default `fxp-dev`),
`FXP_JWT_AUDIENCE` (default `fxp-gateway`) and optional `FXP_JWT_KID` exactly as
for the test clients; set the same values on gateway and simulator.

| Setting | Default | CLI override |
|---|---|---|
| `FXP_SIM_GATEWAY_ADDR` | `127.0.0.1:8082` | `-gateway host:port` |
| `FXP_CLIENT_TLS` | `false` | `-tls` / `-tls=false` |
| `FXP_CLIENT_TLS_CA_FILE` | system roots | `-tls-ca path` |
| `FXP_CLIENT_TLS_CERT_FILE` | unset | `-tls-cert path` |
| `FXP_CLIENT_TLS_KEY_FILE` | unset | `-tls-key path` |
| `FXP_CLIENT_TLS_SERVER_NAME` | dial host | `-tls-server-name name` |

Flags override environment defaults. TLS options reuse `tlsconfig.ClientOptions`
(including verification, optional client identity, certificate/key pair checks
and rejection of TLS settings when TLS is disabled). Client key flags name files;
signing material itself must never be supplied as an argument. Invalid secrets
or TLS configuration fail before any network connection.

For the explicit Compose dev profile, export the same random secret used by
Compose (the development constant is forbidden on the container's non-loopback
listener), start `docker compose -f docker-compose.dev.yml --profile dev up --build`,
then run from the repository root:

```sh
./bin/simday -duration 5 -symbols 2 -participants 4 -seed 7
```

For secure Compose, follow [TLS trust](tls-trust.md) to create the disposable test
CA and start `docker compose up --build`. Use the host paths to the test identity:

```sh
./bin/simday -gateway 127.0.0.1:8082 -tls \
  -tls-ca certs/ca.crt -tls-cert certs/client.crt -tls-key certs/client.key \
  -tls-server-name localhost -duration 5 -symbols 2 -participants 4 -seed 7
```

The simulator never needs the core token. Secure Compose services still require
it in their environment. Keep secrets out of committed files and shell tracing.

`-seed 0` chooses the current time; a nonzero seed controls the existing random
streams and order generation. Unit tests compare order content with a fixed seed
and controlled call order (timestamps excluded). Live concurrent wall-clock runs
can interleave participants, price walks and responses differently; identical
trade results or whole-run order sequences are not guaranteed. Phase labels
also do not configure core auctions: set `FXP_SESSION_SCHEDULE` on the core.

`bash scripts/simday_smoke.sh` builds and starts a disposable loopback core and
gateway, generates a random 32-byte secret in memory (hex encoded in the
environment), sets custom issuer/audience/key id, and requires the five-second
four-participant run to exit zero with fills. Ports 8080–8082 must be free. Logs
are retained under `target/simday-smoke.*`; processes are stopped on exit. CI runs
this step in the existing `e2e` job, already required by `ci-success`.
