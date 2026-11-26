# FXP threat model review

The review traces the security controls in FXP at commit `dfb27fe7b5d2`.
[SECURITY.md](../SECURITY.md) describes the deployment trust assumptions and
the private vulnerability reporting channel.

## Verified controls

- The gateway binds requests to a JWT subject. The core compares the sender
  with the stored owner for live and parked order cancellation and replacement.
  Order status and mass cancel filter by sender. The admitted gateway remains
  trusted to assert the sender.
- The FIX registry encodes each session-key component before deriving the
  store path and allows one active connection per session key. FIX session
  parsing caps body size at 4 KiB before Logon and 1 MiB afterward.
- Gateway startup requires at least 24 bytes for provisioned JWT keys and
  rejects the known development key on non-loopback listeners. Use independent,
  randomly generated signing keys and core admission tokens.
- A core market-data subscription looks up existing books. Gateway
  subscription membership and reconnect work need their own resource budget
  because the gateway registers symbols before the core handles the request.

## Residual risks and operational controls

- The FIX bridge holds the gateway JWT signing key. Treat bridge compromise
  as issuer compromise for incident response. Provision a fresh signing key
  and remove every compromised key from gateway verification, including
  `FXP_JWT_PREVIOUS_SECRET` and its `FXP_JWT_PREVIOUS_KID` rotation slot.
  Replace the bridge's key and the gateway's `FXP_JWT_SECRET` and
  `FXP_JWT_KID` through the deployment's secret configuration, then restart
  affected processes to apply the environment and terminate existing sessions.
  Verify that tokens signed with each revoked key fail authentication on
  every gateway instance. A separate, scope-limited bridge credential
  would reduce the impact of future compromise.
- Coordinate principal assignment across JWT and FIX issuers. Reserve distinct
  trader namespaces where identities are independent and exclude every colon
  (`:`) from issued trader IDs while the gateway uses delimiter-based order
  IDs. Enforce this in JWT subject and FIX SenderCompID provisioning. Excluding
  only `::` still permits a boundary collision between trader `desk:` with
  order `1` and trader `desk` with order `:1`, both encoded as `desk:::1`. Any
  encoding change needs a migration plan for resting orders and report routes.
- Treat FIX resend stores and epoch archives as sensitive order records.
  Define access, retention, and disk budgets for both the active log and
  archived epochs. Monitor the volume and exercise recovery from a failed
  write or interrupted reset.
- Replay verifies consistency against a bundle's manifest and expected
  digests. Establish producer identity separately when a bundle is used as
  evidence. Apply access and retention controls to journals, traces, and
  generated reports. Where a worker processes bundles from a different trust
  domain, enforce read-only inputs, separate output storage, and CPU, memory,
  and disk limits.
- Inventory actual listener reachability by deployment. Secure Compose,
  loopback demo and development, and the disposable loopback release example
  have different trust assumptions. Include internal observability endpoints,
  presence data, and demo token and simulator controls in that inventory.
