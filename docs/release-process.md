# First release process

Release preparation does not create a tag. The prepared [0.1.0] changelog is
pending; record the actual date on the reviewed main commit before tagging.
See [artifact implementation](releasing.md) and [repository settings](repo-settings.md).

## Verify the candidate

1. Merge the reviewed PR only after `ci-success` is green. Enable the main
   ruleset in the settings checklist. Confirm the candidate's changelog, known
   limitations, license notices and deployment configuration.
2. Test the release workflow before tagging, from the branch:

   ```bash
   gh workflow run release.yml --repo StephT0560/fxp --ref chore/r4-ci-gate
   gh run list --repo StephT0560/fxp --workflow release.yml --branch chore/r4-ci-gate
   gh run watch RUN_ID --repo StephT0560/fxp --exit-status
   gh run download RUN_ID --repo StephT0560/fxp --dir release-check
   ```

   Alternatively: Actions → Static Linux release → Run workflow → select the
   branch. `workflow_dispatch` builds both architectures, uploads artifacts and
   skips `draft`; it never creates a release or tag. The workflow already exists
   on main, so branch dispatch is available.
3. Confirm amd64/arm64 tarballs and four nonempty CycloneDX inventories. Manual
   artifacts do not include the combined SHA256SUMS: that is generated in the
   tag-only draft job. To exercise the same checksum command locally, place all
   archives and inventories in one directory, then run:

   ```bash
   sha256sum -- *.tar.gz *.cdx.json > SHA256SUMS
   sha256sum -c SHA256SUMS
   ```

   Build-time ELF checks reject wrong architectures or dynamic dependencies.
   Run binaries on native amd64 and arm64 hosts as a deployment smoke test;
   cross-compilation alone does not validate arm64 runtime behavior. See
   `docs/releasing.md` for local build prerequisites and commands.

## Create the first tag (maintainer only)

Run only after approval, green main CI and final release-date review:

```bash
git checkout main
git pull --ff-only origin main
test -z "$(git status --porcelain)"
git log -1 --oneline  # confirm the reviewed release commit
git tag -a v0.1.0 -m 'FXP 0.1.0'
git push origin refs/tags/v0.1.0
```

The `v*` push builds static Linux amd64 and arm64 core/gateway/bridge binaries,
archives with LICENSE/BUILD-INFO, and Syft 1.27.1 CycloneDX source and binary
inventories. Once both builds pass, `draft` creates SHA256SUMS and an unpublished
GitHub Release using the automatic GITHUB_TOKEN (`contents: write` only there).
No private signing secrets are required. Checksums provide integrity, not a
cryptographic publisher signature. Source inventories include build/dev packages.

## Review and publish manually

Download the draft's assets, run `sha256sum -c SHA256SUMS`, inspect inventories
and archive contents, and replace the boilerplate draft notes with [0.1.0] notes
and known limitations. Confirm both architecture smoke tests and the exact tag
commit. Publish the draft manually only after review. If a build fails, inspect
logs and repair through a reviewed PR; do not silently move a released tag.
Benchmarks and release builds are not required PR checks.
