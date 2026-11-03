# Sandbox spec verification

Audited public `StephT0560/fxp` main at **`fedffdb27b9058a5a584d9a33fd96a604d8b0fd5`** on 2026-11-02 (America/New_York). The input is the uploaded **`FXP_HOSTED_SANDBOX_SPEC.md`**, titled *FXP Sandbox: hosted spec (bot-developer reproduction) — v0.1 draft*, originally based on `8e85fea`; it was not present in either repository. This document does not add the proposed hosted product or amend the uploaded spec.

Statuses: **confirmed** means code and/or this audit's command supports the claim within the stated scope; **changed** means the claim needs the qualification in the note; **false** means current behavior contradicts it. Future product requirements are not evidence of existing behavior. Source references below refer to the baseline commit, before these documentation additions.

## Every tagged claim

The spec contains **13 `[verified…]` markers**, including its introductory legend. Each marker is accounted for below; the compound claims in §§3, 4 and 10 are split into independently assessable rows. Untagged proposed capabilities, including workspace isolation, hosted endpoints and the §3 concurrency caps, remain proposals.

| Claim | Spec section | Evidence (file:line or command + output) | Status | Note |
| --- | --- | --- | --- | --- |
| Legend: verified facts were based on `main @ 8e85fea` | Intro | `git rev-parse origin/main` → `fedffdb27b9058a5a584d9a33fd96a604d8b0fd5` | changed | This is a new audit of current main, not confirmation of the prior round's testing or a claim that the old SHA is current. |
| Every session is recorded at the engine boundary | 1, Promise | [server.rs:1339](../../fxp_core/src/server.rs#L1339): recording is selected only when `FXP_RUN_RECORD` is set | changed | Recording is opt-in and per core process. A hosted Runner must explicitly configure it for every run; default native/Compose sessions are not automatically recorded. |
| Recorded commands reproduce ordered business events and final book state, verified by hash | 1, Promise | Fresh `make repro-demo BUG=stale-replace` → exit 0, `IDENTICAL events=9 symbols=1`; fresh signal test → `IDENTICAL events=1 symbols=1`; [reproducibility.rs:182](../../fxp_core/src/reproducibility.rs#L182), [docs/reproducibility.md:3](../reproducibility.md#L3) | confirmed | Complete, integrity-checked, supported bundles; engine boundary only. Hash consistency is not a signature or proof that an uploader is trustworthy. |
| Replay does not reproduce bot clocks, randomness or decisions | 1, Honest limit | [docs/reproducibility.md:16](../reproducibility.md#L16); `diff` between two fresh one-order runs reports differing admission timestamps at sequence 1 | confirmed | Live scheduling, transport delivery, gateway/FIX sessions and rejects outside the engine are outside the recording guarantee. A shared seed alone is insufficient. |
| `fxp_client.Client.connect(host, port, token)` exists | 2, step 4 | [client.py:104](../../sdk/python/fxp_client/client.py#L104); live audit uses `Client.connect('127.0.0.1',8082,token)` and receives an ExecutionReport after submitting an order | confirmed | Token is the third positional argument; timeout/TLS/frame cap are keyword-only. SDK live integration also runs through `make repro-demo` with its test-client opt-in. |
| Timeline, explain and HTML report are available | 2, step 7 | Every gallery invocation runs `timeline`, `explain`, `report --html ... --json ...`; exit 0 and `PASS stable report schema, independent CSP SHA-256/Base64, embedded data and snapshot`; [repro_demo.sh:84](../../scripts/repro_demo.sh#L84) | confirmed | Offline CLI/report generation works. A hosted browser investigation service is not implemented by these commands. |
| Diff reports first divergence, order/fill counts and rejects by reason; exits 1 on divergence | 2, step 9 | `fxp-replay diff target/sandbox-audit/signal-term target/sandbox-audit/signal-int --json` → exit **1**, `different:true`, first sequence **1**, A/B `orders:1,fills:0,rejects_by_reason:{}`; [replay_cli.rs](../../fxp_core/src/replay_cli.rs) | confirmed | Both inputs were fresh complete recordings. Here the first difference is command time, not evidence of a bot fix; diff does not align or normalize different live runs. |
| All four gallery practice bugs exist and run | 2, gallery | `make repro-demo BUG=<name>` for `stale-replace`, `fill-cancel-race`, `stp-surprise`, `auction-phase`: all exit **0**, independently `IDENTICAL`, and all report checks pass; [scenarios.py](../../examples/bot_repro/gallery/scenarios.py) | confirmed | These are deliberately faulty local examples, not hosted templates already installed in a Sandbox product. |
| Recording is a per-core-process setting, `FXP_RUN_RECORD=<new dir>` | 3 | [server.rs:1339](../../fxp_core/src/server.rs#L1339), [reproducibility.rs:438](../../fxp_core/src/reproducibility.rs#L438); restart with existing directory → exit **1**, `File exists (os error 17)` | confirmed | Parent must exist; neither complete nor incomplete directories may be reused. `FXP_EVENT_LOG` is mutually exclusive. |
| Recording uses synchronous journal and global command serialization | 3 | [server.rs:53](../../fxp_core/src/server.rs#L53), [server.rs:1383](../../fxp_core/src/server.rs#L1383), [event_log.rs:275](../../fxp_core/src/event_log.rs#L275), [docs/reproducibility.md:35](../reproducibility.md#L35) | confirmed | One process-wide journal admits commands in a global sequence; this is not independent tenant journals in a shared core. |
| Bundle finalizes only on clean SIGTERM drain | 3 | [main.rs:33](../../fxp_core/src/main.rs#L33); actual SIGTERM **and SIGINT** each exit **0**, manifest `complete:true`, verify **0/IDENTICAL**; SIGKILL exits **-9**, `complete:false`, verify **2** | changed | Clean SIGINT also finalizes. Successful draining and syncing are required; receiving a signal alone does not guarantee completion. |
| All bundle strings are untrusted | 4, replay worker | [docs/reproducibility.md](../reproducibility.md), “All bundle strings are untrusted”; [report/mod.rs:10](../../fxp_core/src/replay_cli/report/mod.rs#L10), [report renderer](../../fxp_core/src/replay_cli/report) | confirmed | Report escaping/CSP and report size limits exist. Do not infer safe archive extraction, authentication, or universal resource caps for verify/diff; hosted worker containment remains required. |
| Gateway enforces alg and signing key from `FXP_JWT_SECRET` | 4, auth flow | [auth.go:259](../../go_gateway/transport/auth.go#L259); live Logon: HS256 accepted; `alg=none`, `alg=RS256`, wrong secret rejected; verbose `go test ... -run TestValidateJWT` → PASS | confirmed | Exact HS256 required, configured kid selection, HMAC comparison. Startup requires >=24-byte secrets; normal hosted users must not receive operator-role tokens. |
| Gateway enforces issuer/audience | 4, auth flow | [auth.go:320](../../go_gateway/transport/auth.go#L320); live wrong-iss/wrong-aud Logon attempts both rejected; existing JWT tests PASS | confirmed | Must configure the same expected issuer/audience across signers. Defaults are `fxp-dev` / `fxp-gateway`; the secret alone does not select them. |
| Gateway enforces exp/nbf | 4, auth flow | [auth.go:331](../../go_gateway/transport/auth.go#L331); live expired token and `nbf=now+120` rejected; existing JWT tests PASS | confirmed | `exp` is required, checked with `now > exp`; equality passes. Optional nbf has default 30s leeway; nonzero iat is required, but future iat is not rejected. |
| Optional replay cache | 4, auth flow | [auth.go:338](../../go_gateway/transport/auth.go#L338); live reused-jti Logon rejected with cache on; `TestJWTReplayCache`, key-scoping/pruning tests PASS | confirmed | Off by default; requires jti when enabled, process-local and lost on restart. Not bundle replay or a durable cross-process cache. |
| Manifest has fxp_version, git_sha, proto_schema_sha256 | 7, F2 | [reproducibility.rs:463](../../fxp_core/src/reproducibility.rs#L463); fresh native manifest has all three; [build.rs:20](../../fxp_core/build.rs#L20) supplies build SHA | confirmed | Image digest is not automatic: caller may add it through `FXP_RUN_META_JSON`. The shipped Dockerfile copies no Git metadata and supplies no build-SHA override, so a field's existence does not establish useful container Git provenance. |
| Recording calls sync_data per command | 10, recording speed | [event_log.rs:275](../../fxp_core/src/event_log.rs#L275): append followed by `file.sync_data()` before execution; shutdown sync at :294; [docs/reproducibility.md:54](../reproducibility.md#L54) | confirmed | This confirms the durability policy, not an equal-throughput claim; canonical hashing/trace work adds overhead. No throughput benchmark was substituted for this code check. |
| Run directories 0700 and files 0600 | 10, order data | [reproducibility.rs:438](../../fxp_core/src/reproducibility.rs#L438), [event_log.rs:197](../../fxp_core/src/event_log.rs#L197); actual SIGTERM/SIGINT/SIGKILL bundles: directory **0700**, all present manifest/config/commands/trace/expected/lock files **0600** | confirmed | Unix creation permissions, subject to umask; not encryption, cloud object-store policy, upload authorization or an owner filter. Existing files are not rewritten to repair permissions. |

No tagged capability was disproved wholesale; the recording defaults and SIGINT qualification require spec wording changes. The legend and compound markers above are deliberately audited rather than treating prior `[verified]` labels as evidence.

## Runtime evidence and reproduction

Run from repository root after building/installing dependencies as described in [run-stack-config.md](run-stack-config.md). Audit-only scripts, logs, bundles, videos and generated reports are under ignored `target/sandbox-audit`, `target/bot-repro.*`, and `web/test-results`; they are **not shipped implementation, committed credentials, or required fixtures for this documentation**. Results below preserve the checks and relevant outputs; hashes are examples from these specific fresh recordings.

### Fresh recordings and four gallery bugs

```bash
export FXP_REPRO_PYTHON="$PWD/.venv/bin/python"
for bug in stale-replace fill-cancel-race stp-surprise auction-phase; do
  make repro-demo BUG="$bug"
done
```

| Bug | Exit | Verify output | Report schema/CSP/data check |
| --- | --- | --- | --- |
| stale-replace | 0 | `IDENTICAL events=9 symbols=1` | PASS |
| fill-cancel-race | 0 | `IDENTICAL events=15 symbols=1` | PASS |
| stp-surprise | 0 | `IDENTICAL events=4 symbols=1` | PASS |
| auction-phase | 0 | `IDENTICAL events=16 symbols=1` | PASS |

The stale-replace run observed a 7/10 fill, then rejected a new total of 3 with `Replace quantity not above filled quantity`. Its business digest was `254cc9509c5e4303f0df8f2a5e592670f7d73d6b8fe1513a875f9a1187f28753`; final state was `024e9226523c58acadf0c3ba77017d32a2baceaff92b87d366c484550e3bf6f6`.

### Signals, directory lifecycle, permissions and diff

For each signal, start a fresh recorded core and gateway with a generated >=24-byte secret, use the SDK to authenticate and submit one AUDIT limit order, wait for its ExecutionReport, stop/wait gateway, then send the signal directly to the core PID and wait. Inspect `manifest.json`, Unix modes, and `fxp-replay verify`. This avoids timing a kill against a gallery script that has already finished; the processes were actually live when signaled.

| Check | Observed result |
| --- | --- |
| `kill -TERM "$core_pid"; wait "$core_pid"` | Exit 0; complete true; `IDENTICAL events=1 symbols=1`; verify exit 0 |
| `kill -INT "$core_pid"; wait "$core_pid"` | Exit 0; complete true; `IDENTICAL events=1 symbols=1`; verify exit 0 |
| `kill -KILL "$core_pid"; wait "$core_pid"` | Python subprocess returncode -9 (shell convention 137); complete false; no expected.json; verify exit 2, `invalid: incomplete bundle: replay ran but cannot verify` |
| Restart core with finalized `FXP_RUN_RECORD` path | Exit 1, `File exists (os error 17)` |
| `stat` on run directory and each present file | 0700 directory; 0600 files, including `.recording.lock` |
| `fxp-replay diff <fresh-term-bundle> <fresh-int-bundle> --json` | Exit 1; `different:true`; `first_divergence.global_seq=1`; both A/B have orders=1, fills=0, rejects_by_reason={} |

The SIGTERM recording's verify output was:

```text
IDENTICAL events=1 symbols=1 global=1864699b3aa9908fd496cdd91624e4fcd3379ccfbe6d995a270221975f37e11c state=e8c102d6311aafc4449370caf1a6b9512848c48b3293e92811a9f07ae91bdc4b
```

Diff detected differing admission clocks in the two valid recordings, not fabricated expected digests or tampered files. For a durable checked-in reproduction of the exit contract:

```bash
set +e
fxp_core/target/release/fxp-replay diff \
  fxp_core/tests/bundles/partial-fill fxp_core/tests/bundles/reject --json
result=$?
set -e
[[ "$result" == 1 ]]
```

### Gateway JWT enforcement

Live TCP Logon used random per-test signing material, explicit issuer `audit`, audience `audit-gw`, `FXP_JWT_REPLAY_CACHE=on`, current iat/exp/nbf and a unique jti. Each negative test changed one property and asserted `Client.connect` raised `PermissionError`; the control token authenticated and an order received an ExecutionReport. No tokens or secret values are included here.

| Case | Observed |
| --- | --- |
| Valid HS256/secret/claims | accepted |
| alg none, alg RS256, wrong signing secret | rejected, each |
| Wrong issuer, wrong audience | rejected, each |
| exp in the past, nbf 120 seconds ahead | rejected, each |
| Same jti used twice under same issuer/key | first accepted; second rejected |

Also ran the existing validation tests with explicit output:

```bash
(cd go_gateway && go test -race -count=1 -v ./transport \
  -run 'TestValidateJWT|TestJWTReplayCache|TestReplayCachePrunesExpired|TestReplayKeyFieldsDoNotCollide')
# PASS, ok go_gateway/transport 1.042s
```

`TestValidateJWT` covers missing claims, array audiences, algorithm variants and key rotation as well as these live cases. This is authentication coverage, not proof of an unimplemented hosted workspace authorization layer.

## Docker startup and memory

Host: Linux x86_64, kernel 6.18.57, **5 exposed CPUs**, approximately 33 GiB RAM; nested Docker **28.4.0**, Compose **2.40.3**, **vfs** storage driver, cgroup v2. This is not the spec's hypothetical 4-vCPU production host. Built unmodified `Dockerfile` and `Dockerfile.demo`; used release binaries and UID **10001**, read-only root filesystems, no capabilities, no-new-privileges, PID limit 128, and a private writable tmpfs parent for recording. No CPU/memory quota was imposed. Image IDs from this build:

```text
core/gateway/bridge  sha256:66d6502a0962f4bb44ac22fd8bec054e391f21482e671ce18791ed2bfc5aad2f
simday/demo/bridge   sha256:8064f80123bd759f671b2ade095fe4a9f50f9e7d80652c764bc2628729e74ffc
```

**Definitions:** cold means creation/start of new containers from already built local images, not a cold disk/page cache, an image pull, or image compilation. Warm means stopping and restarting the **same containers**; the recording tmpfs is recreated on start, so the recording path is new. Image pull/build occurred during preparation and is excluded from runtime startup. Failed measurement-harness attempts are excluded: nested host forwarding was unsuitable for this test, so the final probe executed inside the stack's shared loopback namespace, and the non-root probe's execute permission was explicitly set. No product code was changed to make the measurements work.

Timing uses Python `time.monotonic()` from before the first Docker create/start call, with sequential core→gateway→simday startup. Core readiness requires the startup log; gateway readiness additionally requires authenticated Logon plus an order ExecutionReport; simulator readiness requires `All participants connected` and its opening-call stdin gate. Docker exec/log polling overhead is included. RSS is **VmRSS of each actual executable process**, excluding docker-init/daemon/cache, sampled two seconds after readiness while simday waits at the stdin gate (no ongoing simulated order flow). A probe order remains resting in core. It is a single idle point, not peak/cgroup memory, CPU utilization, or an active-load benchmark.

| Run | Participants | Core ready (s cumulative) | Gateway order ACK (s cumulative) | Simday ready (s cumulative) | Core RSS (KiB) | Gateway RSS (KiB) | Simday RSS (KiB) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Cold: fresh containers | 0 | 1.708 | 3.282 | 4.859 | 5344 | 9484 | 6100 |
| Warm: same containers, restart 1 | 0 | 0.125 | 0.291 | 0.403 | 5556 | 9572 | 6132 |
| Warm: same containers, restart 2 | 0 | 0.113 | 0.292 | 0.393 | 5424 | 9576 | 6008 |
| Independent first successful fresh-container measurement | 0 | 1.389 | 3.124 | 3.868 | 5516 | 9652 | 6268 |
| Fresh containers, repeated creation 1 | 0 | 0.675 | 1.698 | 2.219 | 5556 | 9548 | 6132 |
| Fresh containers, repeated creation 2 | 0 | 0.476 | 2.062 | 3.432 | 5516 | 9700 | 6112 |
| Six participants authenticated, held idle | 6 | 0.768 | 2.443 | 4.071 | 5548 | 10788 | 7256 |

The first three runs total about **20.4–20.8 MiB** executable RSS; the six-connected-participant point totals about **23.0 MiB**. A zero-participant simday is an explicit idle baseline, not evidence of a functioning simulated market; the six-participant point establishes authenticated connection setup but remains gated, and trading is checked separately by the native simulator suites. Container creation overhead and unrelated concurrent validation work affect these small samples. **Do not derive 20 active runs/4 vCPU from RSS arithmetic**: sustained journal fsync, CPU, per-client queues, growing books/traces, burst startup, FIX stores, BFF and worker overhead still need an active concurrency sweep and peak/cgroup measurements.

### Reproduce container setup

Build dependencies first (supply the environment's trusted proxy CA only if needed; never disable TLS verification):

```bash
docker build --secret id=proxy_ca,src=/etc/ssl/certs/ca-certificates.crt -t fxp-audit:local .
docker build -f Dockerfile.demo --secret id=proxy_ca,src=/etc/ssl/certs/ca-certificates.crt \
  -t fxp-sandbox-audit:local .
```

The following is the measurement topology; use unique names and generate a fresh `FXP_JWT_SECRET` without logging it. Core has `--network none`; other containers join its namespace, so runtime traffic is localhost-only. `-i` keeps simday's stdin gate open, and opening auction is **not** released for the idle sample.

```bash
export FXP_JWT_SECRET=$(openssl rand -hex 32)
docker run -d --name audit-core --init --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --pids-limit 128 \
  --tmpfs /data/runs:uid=10001,gid=10001,mode=0700 \
  -e FXP_RUN_RECORD=/data/runs/run fxp-audit:local fxp_core
# Wait for core startup (or execute /usr/local/bin/core_healthcheck).
docker run -d --name audit-gateway --init --network container:audit-core --read-only \
  --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 128 \
  -e FXP_JWT_SECRET fxp-audit:local go_gateway
# Use an in-namespace FXP client to require LogonAck and an order acknowledgment.
docker run -di --name audit-simday --init --network container:audit-core --read-only \
  --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 128 \
  -e FXP_JWT_SECRET fxp-sandbox-audit:local \
  simday -symbols 2 -participants 6 -duration 300 -seed 7 -opening-auction-sec 10
# Wait for the connected + stdin-gate log, then sample executable /proc/*/status VmRSS.
# Stop producers before forwarding/core; restart in the reverse order for warm timing.
docker stop audit-simday audit-gateway audit-core
docker rm audit-simday audit-gateway audit-core
```

The audit's temporary Go readiness probe reused existing protobuf/devjwt packages, performed bounded 64-KiB frame reads with deadlines, and was mounted read-only executable for UID 10001. Measurements used no new module dependency. If using stock TCP/WS tools instead, preserve the same authenticated-order readiness boundary; a port-only check is not comparable. The native hand-start recipe in [run-stack-config.md](run-stack-config.md) is the supported engineer workflow without this temporary measurement probe.

## Test results

Results from this audit, not reused onboarding logs. Dependency installation/pulls were preparation; runtime functional checks used localhost (or local Unix Docker control) only. See the final PR body for acceptance pass/fail and any suite limitations.

| Command / suite | Actual result |
| --- | --- |
| `make test` | PASS, exit 0: Rust **260 passed, 1 ignored** across library/integration suites; Go race tests **18 packages passed**, 8 packages have no tests. The ignored large-report test is run separately below. |
| Existing Python `unittest discover` suites | PASS: root tests **24**, CI **3**, Compose **6**, security **1**, release **3**, QuickFIX helper tests **7**, bot/gallery unit tests **8**, build provenance **2**. All returned `OK`. |
| SDK `unittest discover -s sdk/python/tests -v` | **6 unit tests passed**, **1 integration test skipped** because no stack was requested in this invocation. |
| `FXP_REPRO_PYTHON="$PWD/.venv/bin/python" FXP_REPRO_TEST_CLIENT=1 make repro-demo` | PASS, exit 0; the SDK's skipped test ran against the real stack: **1 integration test passed**, plus fresh IDENTICAL replay/report validation. |
| Four `make repro-demo BUG=...` commands | PASS, all four exit 0; outputs recorded above. |
| `bash scripts/e2e.sh` | PASS, exit 0: **76/76 FIX checks**, **29/29 order-type checks**, disconnect, kill/restart, graceful FIX shutdown, core persistence restart/cancel/ExecID checks. |
| `bash scripts/e2e_tls.sh` | PASS, exit 0: TLS/mTLS and core-auth functional checks, certificate reload on 8081/8082/8083, FIX/order/restart and benchmark-smoke checks. No latency/capacity claim is inferred. |
| `bash scripts/repro_bundle_smoke.sh` | PASS: **20/20** independently recorded live simulator runs verify; four sessions/two symbols. |
| `bash scripts/simday_smoke.sh` | PASS: configured random secret and custom iss/aud/kid, trades observed. |
| `bash scripts/demo_api_smoke.sh` | PASS: **1 real-stack integration test**, token→same-origin WS→LogonAck, foreign Origin rejected, public trades, simulator start/status/stop/shutdown. |
| `(cd fix_bridge && go test -race -tags quickfix_fixture -count=1 ./...)` | PASS: all **5 packages**. Fixture remains compiled separately from production binaries. |
| `python3 scripts/quickfix_acceptance.py --source target/quickfix-source --ruby <prepared-ruby-wrapper> --no-build --compat default` | PASS, exit 0: **59 definitions**, **45 strict passes**, **14 allow-listed**, **0 unexpected failures/passes**, **0 runner errors**. |
| Same pinned QuickFIX command, `--compat quickfix` | PASS, exit 0: **59 definitions**, **57 strict passes**, **2 allow-listed**, **0 unexpected failures/passes**, **0 runner errors**. |
| `(cd web && npm test && npm run build && npm run size)` | PASS, exit 0: **14 files / 69 tests**, TypeScript+Vite build; initial **151.3/160 KiB**, deferred **118.3/120 KiB** gzip checks pass. |
| `(cd web && FXP_CONSOLE_CHROMIUM=/usr/bin/chromium npm run e2e)` | PASS, exit 0: **30/30 tests** in **3.0 minutes**, native real stack, existing Chromium, prepared FFmpeg. |
| `cargo test --locked --manifest-path fxp_core/Cargo.toml --lib -- --ignored --exact replay_cli::report::tests::fifty_thousand_business_events_fit_size_and_generation_budget` | PASS: **1 passed**, 248 filtered out; fixture+test completed in **62.31 s**; asserts 50,000 business events / 100,000 rows and existing bounds/budget. |
| `python3 scripts/check_replay_goldens.py` | PASS: all **10** checked-in bundles' independent command/event/digest validation. |
| `python3 scripts/check_run_report.py --goldens target/sandbox-audit/run-reports` | PASS: independent golden JSON/HTML/CSP validation, including hostile-data output. |
| Existing offline `web/scripts/report-smoke.mjs`, temporary ignored copy selecting `/usr/bin/chromium` | **BLOCKED by environment**, exit 1: `page.goto: net::ERR_BLOCKED_BY_ADMINISTRATOR` for the first local `file://.../partial-fill.html`. Browser policy prevented navigation before the assertions; **no report browser pass is claimed**. No policy or browser security setting was disabled. |
| `python3 scripts/demo_compose_check.py` and `bash scripts/demo_resilience_check.sh` on prepared `docker compose ... up --no-build --wait` | PASS: only loopback UI port published, private core/gateway, no secret in checked responses; same page recovers/order-acks after stop/start **5.1s**, restart **1.2s**, killed-core **1.3s**; fresh login passes. |
| `bash scripts/compose_check.sh --no-build` | PASS: default secure TLS/mTLS Compose smoke and three core disruptions recover (**3s**, **4s**, **3s**), including FIX cross. |
| Hand-start shell recipe in [run-stack-config.md](run-stack-config.md) | PASS, executed verbatim after prerequisites: **IDENTICAL events=503 symbols=2**, exit 0. Optional FIX bridge started/stopped; random JWT/core/FIX credentials stayed local. |
| New audit lifecycle/JWT/diff/permissions probes | PASS: live positive/negative cases, actual signals, existing-directory refusal, fresh-bundle diff exit 1, Unix modes; outputs above. Temporary harnesses are ignored, not application changes. |
| `python3 scripts/check_repro_blind.py` | PASS: blind stdout, sealed answer/start time, verified bundle and reveal. |
| `make fmt-check`; console projection and bundle-secret scan; documentation local links/line bounds; `git diff --check` | PASS. Missing rustfmt/PyYAML were installed as existing test prerequisites before rerunning; no dependency declarations changed. |

QuickFIX inputs were fetched **before** its tests at pinned commit `6908dc8c1084eeb7af7f322d35216e9965684489`; Ruby 3.3 ran from a prepared digest-pinned container because this host lacks Ruby. That wrapper used the host network namespace solely for the native test's localhost FIX listener, a read-only runner mount, the task UID, no capabilities, and `--pull=never`; tests did not fetch fixtures or call external services. Allow-listed QuickFIX outcomes are intentionally not counted as strict passes.

The first Compose demo attempt failed because this cloud checkout created public `config/` CSVs with mode 0700/0600, unreadable to container UID 10001. After diagnosing `Permission denied (os error 13)`, only their local read/traverse permissions were temporarily made 0755/0644; Compose/resilience passed and the original 0700/0600 permissions were restored. No Git mode bit, CSV content, application code or container privilege was changed. The browser `file://` restriction remains an **environment limitation**, not a diagnosed application defect; the full offline file-report smoke is outstanding. Performance/nightly benchmarks and online dependency/security scanners are not represented as executed functional tests.


## Spec deltas and follow-ups

- Update the spec's baseline SHA and qualify “every session”: recording must be explicitly enabled by Runner; stock Compose does not enable it.
- Replace “only clean SIGTERM” with successful SIGTERM **or SIGINT** drain/sync; keep SIGKILL/incomplete behavior distinct.
- Treat manifest provenance fields as present, not necessarily fully populated in local Docker builds; image digest injection and build SHA need an explicit packaging contract.
- Existing images are two multi-binary local images, not five published pinned release images; neither packages fxp-replay. No registry publication or public release is asserted by this audit.
- Shared-loopback networking is required for core/gateway. Existing simulator supports a fixed symbol universe and needs caller-controlled opening-call synchronization; template implementation must account for those constraints.
- Preserve explicit JWT defaults, nbf skew, exp equality and iat-presence behavior in token design; the native settings do not enforce hosted maximum duration or workspace policy.
- Measure active recorded stacks, worker peaks and bursts before setting host capacity caps; idle RSS alone is insufficient.
- Audit/upload archive extraction, symlink/path handling, resource containment, retention and anonymized sharing separately before accepting arbitrary bundles in a hosted worker. CLI/report escaping is not that boundary.

These are reports and future work only. No matching-engine logic, application source, tests, dependencies, lockfiles, Dockerfiles or Compose files are modified.
