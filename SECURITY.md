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

## Scope notes

- `fxp_core` (127.0.0.1:8080) is unauthenticated by design and must only be
  reachable from the gateway. Reports that require direct access to the core
  port are in scope only if the core is exposed by the default configuration.
- The JWT secret `dev-secret-change-in-production` used by the benchmark and
  test clients is a development default, not a vulnerability.
