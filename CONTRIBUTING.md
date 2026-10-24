# Contributing to FXP

## Prerequisites

- Rust (stable, edition 2024) with `cargo`
- Go matching the `toolchain` line in `go_gateway/go.mod` / `fix_bridge/go.mod`
- `protoc` 25.x on `PATH` (`fxp_core/build.rs` needs it; older distro builds
  such as Ubuntu's 3.12 generate Go code that does not compile)
- Python 3 for the FIX end-to-end suite

## Build and test

```bash
make proto      # generate go_gateway/protobuf/fxp.pb.go (gitignored)
make build      # release core + both Go modules
make test       # cargo test, go test -race in both Go modules
make e2e        # start core -> gateway -> fix_bridge and run fix_client.py
make bench      # Criterion encoding benchmarks (fxp_core/benches)
make fmt        # cargo fmt + gofmt
make lint       # clippy, go vet, golangci-lint (.golangci.yml)
```

CI (`.github/workflows/ci.yml`) runs the same checks on every PR: `rust`,
`cargo-deny`, `go (go_gateway)`, `go (fix_bridge)`, `e2e` and `proto`
(buf lint, plus a breaking-change check against the base branch).

Without `make`: see the commands in the `Makefile`; each target is a few
plain `cargo`/`go` invocations. A `go.work` at the repo root covers both Go
modules. Manual test clients live in `go_gateway/test_clients/tcp_client`
and `ws_client` (`go run ./test_clients/tcp_client`).

## Branches and pull requests

- Never push to `main`. Branch from the latest `main` and open a PR.
- Branch names: `fix/<topic>`, `feat/<topic>`, `chore/<topic>`, `docs/<topic>`.
- Keep a PR to one topic. Add or update a test that fails without the change.
- `make test` and `make fmt-check` must pass; note any pre-existing failure
  in the PR description instead of fixing it silently.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <summary>

<body: what and why>
```

Types: `fix`, `feat`, `test`, `docs`, `chore`, `refactor`, `perf`.
Scopes in use: `engine`, `core`, `gateway`, `fix-bridge`, `fix-codec`,
`fix-session`, `translator`, `deps`. Reference audit findings as `(#N)`
or `(§x.y)`. Keep formatting-only changes in their own commit.

## Files

New source files start with an SPDX header:
`// SPDX-License-Identifier: MIT` (or `#` for scripts). By contributing you
agree your work is licensed under the MIT License in `LICENSE`.

## QuickFIX FIX44 acceptance

After installing the normal build prerequisites and Ruby 3.3, run from the
repository root with ports 8080–8083 free:

```bash
python3 -m unittest discover -s tests/quickfix -v
python3 scripts/quickfix_acceptance.py
```

The script builds the three services, fetches QuickFIX C++ v1.15.1 at pinned
commit `6908dc8c1084eeb7af7f322d35216e9965684489`, and runs all 59 FIX44
server definitions through its official POSIX Ruby runner. It owns and stops
its service processes. A fresh bridge/store is used for each definition.
`--source /path/to/pinned/quickfix` reuses a verified checkout; `--no-build`
reuses current binaries. `--only NAME.def` is for diagnosis and never establishes
a complete-suite result. The normal `quickfix-acceptance` CI job runs the full
suite on PRs. See [adapter and allow-list rules](tests/quickfix/README.md).

Review every expected failure individually. Preserve meaningful assertions;
do not add a runner crash or timeout to the allow-list to make CI pass. Local
baseline: 28 passed, 31 allow-listed failures, 0 unexpected failures / runner
errors, 59 definitions. This is a regression gate, not certification.
