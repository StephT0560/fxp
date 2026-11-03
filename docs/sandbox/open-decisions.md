# Sandbox open decisions

Audit baseline: public `StephT0560/fxp` main at `fedffdb27b9058a5a584d9a33fd96a604d8b0fd5`; uploaded **FXP Sandbox: hosted spec (bot-developer reproduction), v0.1 draft**, §11, originally based on `8e85fea`. These are recommendations for the private Sandbox product, not implemented behavior or decisions made on the maintainer's behalf.

| Spec §11 decision | Recommended default and reason |
| --- | --- |
| 1. GitHub-only sign-in, or also email magic link? | Use GitHub-only sign-in for the private beta, through the proposed OIDC boundary. It fits the initial developer audience and avoids implementing and operating a second identity and email-delivery flow before validating the reproduction workflow. |
| 2. Free-user run duration and concurrency? | Start with a 15-minute maximum and at most two simultaneous runs per workspace, with a separately configurable host admission limit. Treat these as conservative product-policy defaults rather than validated capacity: [the measurements](verification.md#docker-startup-and-memory) do not establish that 20 active runs fit a 4-vCPU host. |
| 3. Product name and domain? | Use **FXP Sandbox** as the working product name and localhost-only routing for the local beta; defer buying or configuring a public domain. The name connects the hosted workflow to the existing protocol project, while a domain and public TLS endpoint are unnecessary for local acceptance testing. |
| 4. Free-only beta, or show intended pricing early? | Keep the private beta free and show clearly labeled indicative pricing with no checkout or billing integration. This allows willingness-to-pay interviews without adding payment handling or suggesting that the draft prices or service guarantees are commitments. |

No cloud accounts, domain registration, identity service, pricing page, or quota enforcement is added by this audit.
