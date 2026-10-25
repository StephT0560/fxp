# Security Policy

FXP is trading infrastructure: a flaw can move money. Please report
vulnerabilities privately.

## Reporting a vulnerability

- Use GitHub's **Report a vulnerability** button (Security tab, private
  vulnerability reporting) on https://github.com/StephT0560/fxp.
- Do not open a public issue, PR or discussion for an unfixed vulnerability.
- Include the affected component (`fxp_core`, `go_gateway`, `fix_bridge`),
  the commit, steps or a proof of concept, and the impact you observed.

You should get an acknowledgement within 5 business days. We will agree on a
disclosure date with you once a fix is available, and credit you unless you
ask us not to.

## Supported versions

Only the latest `main` is supported. There are no released versions yet.

## Trust model

The core always binds `127.0.0.1:8080` and refuses non-loopback listeners. Do not
publish that port or proxy untrusted traffic to it. Compose shares a private
network namespace between the three services and publishes only the external
ports. Loopback isolation alone does not authenticate processes in that namespace.

| Configuration | Protection and remaining access |
|---|---|
| Development defaults: plaintext, no core token | External JWT/FIX authentication still applies, but traffic can be intercepted. Any process with core loopback access can claim any sender and submit orders. |
| External TLS | Encrypts and authenticates the server on TCP, wss and FIX links. Application JWT/FIX credentials still determine trader identity. Core loopback access remains trusted unless a token is configured. |
| External mTLS | Additionally admits only clients signed by the configured client CA. Certificate subjects are not mapped to trader identities; application authentication still applies. |
| Core shared token | Core rejects connections before parsing orders unless they present the configured token. An attacker with loopback access but without the token cannot assert a sender. An admitted gateway can assert sender identities and must remain trusted. |

Set `FXP_CORE_AUTH_TOKEN` identically on core and gateway, using a random secret
(e.g. `openssl rand -hex 32`) distinct from `FXP_JWT_SECRET`. The bridge connects
through the authenticated gateway pool, never directly to the core. Token errors
are generic and the token is never logged. Rotation requires coordinated core and
gateway restarts; there is no previous-token grace period.

Core token authentication provides possession-based admission over loopback. It
provides neither encryption nor server authentication: a privileged local sniffer,
a malicious process impersonating the core before it starts, or host/root access
can compromise that secret. Secrets in process environments, files and memory
require host access controls. A compromised admitted gateway can impersonate
traders. TLS does not protect compromised endpoints or replace order ownership
checks. Use container/network isolation and restrict local accounts accordingly.

Production listeners should set `FXP_TLS_REQUIRED=true`; the development default
only warns on non-loopback plaintext binds. See [TLS deployment](docs/tls-trust.md)
for certificate reload and client configuration. The development JWT secret
`dev-secret-change-in-production` is a documented local default, never a production
credential.
