## Summary

<!-- What changes and why. Link the issue or audit finding (#N / §x.y). -->

## Changes

| Finding / issue | Commit | Test |
|---|---|---|
|  |  |  |

## Behaviour, wire or config changes

<!-- New/changed env vars, FIX/FXP wire behaviour, breaking changes. "None" if none. -->

## Verification

- [ ] `make fmt-check lint test`
- [ ] `cargo build --release --locked && cargo test` (fxp_core)
- [ ] `go vet ./... && go test -race -count=1 ./...` (go_gateway, fix_bridge)
- [ ] `bash scripts/gen_proto.sh && bash scripts/e2e.sh`
- [ ] Added or updated a test that fails without this change

<!-- Note pre-existing failures separately instead of fixing them silently. -->

## Not done / follow-ups
