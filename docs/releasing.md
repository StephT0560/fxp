# Static Linux release and repository setup

`release.yml` runs on tags matching `v*`. It cross-builds amd64 and arm64 on
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

This PR adds configuration files only; it does not change repository protection,
create a tag, publish a release, or rewrite git history.
