# Releasing FXP v0.1.0

This is the container consumption contract for Sandbox spec F1/F2. Do not create
or move a tag until a reviewed main commit has passed CI. Commands below are
**maintainer instructions**, not actions performed by this PR.

## Five pinned images

`deploy/Dockerfile.release` has five independently runnable multi-stage targets.
Every external base pins a multi-platform index digest; builders use Rust 1.99.1,
Go 1.26.11 and SHA-256-checked protoc 25.3 archives for both supported CPUs.
Runtime images are distroless (C/C++ runtime for Rust, static runtime for Go),
with CA roots, UID/GID **65532:65532**, exec entrypoints and SIGTERM. All images
carry OCI `.version`, `.revision` (full source SHA), `.source` and `.licenses`
labels under `org.opencontainers.image`. The core/replay build receives the SHA
through existing `FXP_BUILD_GIT_SHA`, without copying Git metadata into images.

| Target / GHCR package suffix | Entrypoint |
| --- | --- |
| `fxp_core` | `/fxp_core` |
| `go_gateway` | `/go_gateway` |
| `simday` | `/simday` |
| `fix_bridge` | `/fix_bridge` |
| `fxp-replay` | `/fxp-replay` |

The prefix is **`ghcr.io/stepht0560/fxp`** (lowercase). Each image is published as
both `:v0.1.0` and `:<full-40-character-git-sha>`. Use **`@sha256:<index-digest>`**
for immutable consumption. Each index contains **linux/amd64** and **linux/arm64**;
release notes and `images.json` give the index and both platform manifest digests.
An image config ID from `docker inspect` is not a registry manifest/index digest.
Existing dev/demo Dockerfiles remain supported; release images contain only their
application binary, plus a shell-free readiness executable in core/gateway.

## Local build and test, without publishing

Install Docker with BuildKit/Compose v2 and Python >=3.10 with venv. Dependencies
are prepared before testing; smoke never builds, pulls or contacts a registry.

```bash
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -e sdk/python
make release-images                 # five local fxp-release/<target>:dev images
make release-smoke                  # Compose + SDK + SIGTERM + verify + report
python3 -m unittest discover -s tests/release -v
```

For an HTTPS inspection proxy, set `FXP_BUILD_CA=/path/to/trusted-ca-bundle.pem`;
it is a BuildKit secret, not an image layer. Never disable verification.
`FXP_IMAGE_PREFIX` and `FXP_IMAGE_TAG` override naming in both make targets.
BuildKit metadata is saved under ignored `target/release-images/`; smoke
correlates its config digest with the loaded image before recording the actual
local manifest digest. Pulled images use registry RepoDigests.

Smoke asserts all five images' non-root UID and OCI labels, starts a fresh
recorded core/gateway/six-participant simday project, places/fills an SDK order,
waits for simday success, stops gateway then core with SIGTERM, checks zero drain
exit codes, and expects **IDENTICAL** from replay. It generates HTML/JSON reports
and checks all five actual image references in bundle meta and both reports.
Private temporary projects/ports are unique; containers/volumes are removed on
exit. Non-secret evidence remains under ignored `target/release-smoke.*`.

## Run published images only

After the maintainer has tagged and the workflow has pushed the images:

```bash
export FXP_JWT_SECRET=$(openssl rand -hex 32)
export FXP_CORE_AUTH_TOKEN=$(openssl rand -hex 32) # distinct forwarding token
export FXP_IMAGE_TAG=v0.1.0
export FXP_RUN_ID=run-001
# Optional: FXP_CORE_IMAGE, FXP_GATEWAY_IMAGE, FXP_SIMDAY_IMAGE,
# FXP_FIX_IMAGE, FXP_REPLAY_IMAGE can each be immutable image@sha256:... refs.
docker compose -f deploy/compose.release.yml pull
docker compose -f deploy/compose.release.yml up
```

This file has **no build section**. Supply credentials through the environment,
never committed files. The example publishes **host loopback only**, with
intentional local plaintext; it is not a hosted TLS deployment. Online services
share core's loopback namespace because existing core/gateway addresses are fixed.
All services have read-only roots, dropped capabilities, no-new-privileges and a
PID limit. Recording uses a private named volume; replay reads it read-only with
no network and writes reports to a separate volume.

For clean shutdown, stop producers before forwarding/core, using the same shell
settings and project. Include `bridge` below only if its optional profile ran:

```bash
docker compose -f deploy/compose.release.yml stop simday gateway
docker compose -f deploy/compose.release.yml stop core
docker compose -f deploy/compose.release.yml run --rm --no-deps replay verify /runs/run-001
# Only after copying/retaining the bundle as appropriate:
docker compose -f deploy/compose.release.yml down --volumes
```

Enable FIX with `--profile fix` and securely set `FXP_FIX_CREDENTIALS=COMPID:<password>`.
Use `tools` for explicit replay commands, not an online daemon. An existing
recording directory is refused: choose a new `FXP_RUN_ID` for another run or
remove the disposable volume only after retaining its bundle. Never SIGKILL a
run and label it verified. Simday's existing signal behavior is unchanged; the
smoke normally lets its short simulation finish before draining gateway/core.

## Inject image digests through existing bundle meta (F2)

Download `images.json` from the reviewed release and use its immutable references
for corresponding `FXP_*_IMAGE` overrides. Before starting core, Sandbox can
supply all references through the existing JSON object:

```bash
export FXP_RUN_META_JSON=$(python3 - <<'PY'
import json
with open('images.json') as source:
    pins = json.load(source)
print(json.dumps({'images': {name: item['image'] for name, item in pins['images'].items()},
                  'release': pins['version']}))
PY
)
docker compose -f deploy/compose.release.yml up
```

`manifest.json.meta.images` contains those supplied strings. Normal
`fxp-replay report ... --html report.html --json report.json` includes them in
report provenance/embedded data. **No manifest or report schema changed.** The
existing JSON contract converts integer metadata to decimal strings (seed 7
becomes `"7"`); digest strings are unchanged. Anonymized reports intentionally
omit free-form meta. Never put credentials/private user data in it. The Runner
must resolve/verify its selected images: faithful recording of caller JSON is
not an authenticity guarantee.

## CI and publication

Existing `ci.yml` unit/race/integration/browser jobs are retained. A new required
`release-images` matrix builds all five images and runs this smoke on **native
amd64 and arm64 Ubuntu runners**. It has no package-write permission and never
pushes. `ci-success` requires this matrix and all existing jobs.

On a `v*.*.*` tag, `release.yml` builds five images on each native architecture
using `packages: write` and automatic repository `GITHUB_TOKEN`. Platform manifests
are pushed by digest; the publishing job validates the complete five-by-two set,
creates version/SHA indexes, verifies their exact platform digests, and writes
notes plus `images.json`. The existing static-artifact job attaches these to the
unpublished GitHub draft after all builds succeed. No new signing/SBOM system is
added; the pre-existing static inventory flow below is retained. Manual dispatch
continues to build static artifacts only, without container publication.

Permit the workflow's package/content write scopes and `ubuntu-24.04-arm` runners;
retain required `ci-success`. GHCR visibility is separate from release visibility:
for unauthenticated public use, make all five packages public after first publish
(or authenticate private pulls). No PAT/cloud account is required by the workflow.

### Exact maintainer tag commands (not executed here)

After merging this PR and confirming **that main commit's CI**:

```bash
git fetch origin main --tags
git switch main
git pull --ff-only origin main
test -z "$(git status --porcelain)"
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
# Stop if this tag exists; never move or force-update a release tag.
if git rev-parse --verify refs/tags/v0.1.0 >/dev/null 2>&1; then
  echo 'v0.1.0 already exists' >&2
  exit 1
fi
git tag -a v0.1.0 -m 'FXP v0.1.0'
git push origin refs/tags/v0.1.0
```

Review the workflow result, all five multiarch references, package visibility and
the GitHub draft; then publish that reviewed draft:

```bash
gh release view v0.1.0 --repo StephT0560/fxp
gh release edit v0.1.0 --repo StephT0560/fxp --draft=false
```

The tag push publishes container images automatically; the GitHub release remains
a draft until reviewed. Investigate failed jobs before retrying; missing digests
or incomplete platforms cannot constitute a release. No tag, GHCR push or release
publication was performed while implementing this change.

## Existing static Linux artifacts and repository setup

`release.yml` runs on tags matching `v*.*.*`. It cross-builds amd64 and arm64 on
`ubuntu-24.04` using Rust musl targets with the bundled rust-lld linker, and Go
with `CGO_ENABLED=0`. No system C cross compiler or private signing secret is
needed for the current pure-Rust dependency graph. Each archive includes core,
gateway, FIX bridge, LICENSE and BUILD-INFO with the source SHA and compiler
versions. An ELF check rejects wrong architectures, PT_INTERP and DT_NEEDED;
static PIE without a shared-library dependency is accepted.

Syft **1.27.1** emits separate CycloneDX source and binary inventories for each
architecture. Source scans are confined to Cargo/Go manifests and lockfiles;
they include Rust build/dev dependencies and are explicitly an inventory
superset rather than a claim that every dependency is linked at runtime.
Binary scans identify the Go module metadata in the packaged executables.
Build directories/caches are excluded from source inventories. Both inventories
are checked for nonempty CycloneDX components. The draft contains both archives,
four inventories and SHA256SUMS covering archives and inventories. Checksums
provide integrity; artifacts are not cryptographically signed by this workflow.

After both architecture jobs succeed, the least-privilege publishing job creates
an **unpublished draft** using the repository GITHUB_TOKEN. Maintainers review
notes and artifacts, verify checksums and publish manually. A manual
`workflow_dispatch` run creates downloadable build artifacts only, never a
GitHub Release. There are no automatic release thresholds or promotion steps.

Local artifact-only verification (install protoc 25.3, the repository Go/Rust
toolchains and Syft 1.27.1 on PATH):

```bash
rustup target add x86_64-unknown-linux-musl aarch64-unknown-linux-musl
bash scripts/build_release.sh amd64 target/release-check/amd64
bash scripts/build_release.sh arm64 target/release-check/arm64
python3 -m unittest discover -s tests/release -v
```

The checks prove ELF architecture/static linkage and dependency-inventory
construction. Cross-built arm64 binaries still need native deployment/runtime
verification; an amd64-hosted build alone is not that verification.

## Maintainer configuration

1. Enable GitHub Actions and allow the actions used by CI, benchmark and release
   workflows, including the Anchore Syft downloader. Permit the release job's
   `contents: write` under any organization token policy; no PAT is required.
2. Configure the `main` ruleset requiring only **ci-success**, with up-to-date
   branches, PRs, no force pushes and no deletions, following
   [the settings checklist](repo-settings.md).
3. Restrict creation/update/deletion of `v*` tags to release maintainers with a tag
   ruleset. Push a tag pointing at a reviewed main commit only when ready to
   create a draft. Review draft notes and inventories before manual publication.
4. Enable scheduled workflows (forks and inactive repositories may disable them).
   Benchmarks run nightly at **03:19 UTC**, use `ubuntu-24.04`, and retain JSON,
   Markdown, raw Criterion estimates and metadata for 14 days. Hosted hardware
   is shared and not a fixed CPU model despite the fixed runner label.
5. Do **not** require scheduled benchmark or tag-release checks on PRs: those
   events do not run on every PR and would block merges. Benchmark results have
   no automatic numerical pass/fail threshold. Failed execution still fails the
   workflow so incomplete measurements are visible.

Editing release configuration does not change repository protection,
create a tag, publish a release, or rewrite git history.
