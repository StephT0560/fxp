# QuickFIX FIX44 acceptance gate

The 59 definitions and official POSIX Ruby runner are pinned to QuickFIX C++
v1.15.1, commit `6908dc8c1084eeb7af7f322d35216e9965684489`. A modified tracked
test tree or different commit is rejected. The Windows-only `atrun` wrapper is
not used; upstream `test/Runner.rb` runs the actual definitions. The upstream
license is preserved in `fix_bridge/codec/spec/LICENSE.quickfix`.

## Results (all 59 definitions per configuration)

| Policy | Strict passes | Allow-listed failures | Unexpected failures / runner errors |
|---|---:|---:|---:|
| Default | 45 | 14 | 0 / 0 |
| `FXP_FIX_COMPAT=quickfix` | 57 | 2 | 0 / 0 |

These are fixture-mode session results, not venue application certification.
The old 28/31 baseline used the venue handler; many definitions failed before
reaching their session assertions because QuickFIX expects its echo application.
Every definition still runs in each configuration; none is silently skipped.

## Test-only adapter and guard

The script builds **a separate `bin/fix_bridge_fixture`** with Go build tag
`quickfix_fixture`. The normal binary, Docker image and release artifacts do not
contain the adapter; they reject either fixture flag/environment opt-in.
The special build additionally requires all of:

- `-test-fixture` and `FXP_FIX_TEST_FIXTURE=quickfix`;
- a literal loopback bind, and actual local/remote loopback TCP addresses;
- no configured listener TLS, required TLS, or core authentication token.

It logs a warning on every startup. The production validation path is unchanged.
The fixture is an isolated **echo application**, never a path to the matching
engine: it injects `38=1` only when OrderQty is absent on NewOrderSingle and
preserves explicit empty/invalid quantities for normal dictionary rejection.
The synthetic quantity is not added to the expected echo. Framing, identity,
time, sequence, dictionary, group and enum checks still run normally. Echoes use
the normal session sender/store, so resend definitions exercise actual replay.
PossResend deduplication belongs to this echo fixture and does not claim new
venue application deduplication. Its seen-ID set is bounded at 4096 entries.

The upstream setup uses `ResetOnLogon=Y`; fixture sessions reset their ephemeral
store on disconnect so separate connections in one definition start fresh while
simultaneous duplicate identities still fail. Production durable sessions retain
their configured reset behavior and cancel-on-disconnect persistence ordering.
The fixture uses a two-second Logon timeout (the runner's disconnect deadline is
ten seconds); production's default remains ten seconds. The fixture emits fixed
`TestReqID=TEST` to match definition 6; real deployments retain unique IDs.

The copied `.def` inputs are changed only to add a generated venue Logon password
when their framing is not explicitly malformed. Input checksums/BodyLength,
sequence numbers, enums, groups, and all expected assertions remain untouched.
The existing Ruby comparator adapter verifies actual framing/checksum, tolerates
non-group field order and optional diagnostic Text, and uses upstream timestamp
patterns. Repeating group order is preserved; it never substitutes message types
or hides extra/missing fields. The new fixture does not add comparator masks.

## Remaining allow-listed entries, by reason

Both configurations:

- **Legacy invalid-tag policy:** `14a_BadField.def`. Undefined tag 999 is rejected;
  the next assertion expects a numeric Reject for tag 0, which the parser treats
  as garbled. Negative tag syntax is also ignored and default user tags >=5000
  remain permitted. Later assertions in that definition are not claimed to pass.
- **Out of scope:** `21_RepeatingGroupSpecifierWithValueOfZero.def`, which expects
  a SecurityDefinition (`35=d`) application echo. FXP returns unsupported-type
  BusinessMessageReject. This does not implement SecurityDefinition.

Default policy only:

- **Explanatory Logon failure vs silent disconnect (7):**
  `1b_DuplicateIdentity`, `1c_InvalidSenderCompID`, `1c_InvalidTargetCompID`,
  `1d_InvalidLogonBadSendingTime`, `1d_InvalidLogonWrongBeginString`,
  `1e_NotLogonMessage`, `AlreadyLoggedOn` (all `.def`).
- **Immediate fatal close vs waiting for peer Logout (4):**
  `2f_PossDupOrigSendingTimeTooHigh`, `2i_BeginStringValueUnexpected`,
  `2k_CompIDDoesNotMatchProfile`, `2o_SendingTimeValueOutOfRange` (all `.def`).
  The required Reject/Logout is sent, but the peer's final Logout can meet TCP
  reset. Timing may produce an unexpected pass; it is reported, not silently
  counted as an allow-listed failure or a guarantee of compatibility.
- **Diagnostic Logout on heartbeat expiry (1):** `6_SendTestRequest.def` now
  reaches its final disconnect assertion; cadence and fixed fixture ID pass.
  The preset closes silently when the peer fails to answer the TestRequest.

Exact reasons live in `expected-failures.json` (default) and
`expected-failures-quickfix.json` (preset). There are no remaining echo-blocked
allow-list reasons. SecurityDefinition, TradeCaptureReport, initiator mode,
pegs and the `35=H`/`35=AE` edge replies are not implemented by this work.

## Running and CI

Install normal build prerequisites plus Ruby 3.3, with ports 8080–8083 free:

```sh
python3 -m unittest discover -s tests/quickfix -v
python3 scripts/quickfix_acceptance.py --compat default
python3 scripts/quickfix_acceptance.py --compat quickfix
```

CI runs both policies in a non-fail-fast matrix, plus normal-build and tagged
fixture tests. `ci-success` depends on the combined matrix result. Each policy
writes logs and `results.json` (pin, policy, fixture, per-definition result and
summary) under `target/quickfix-acceptance/{default,quickfix}/`, uploaded as
separate artifacts. The core/gateway also run, but fixture orders are echoed
locally and never execute there. This gate certifies neither engine matching
nor application validation; production E2E/dictionary/security tests cover those.

Missing/empty allow-list reasons, stale definition names, unexpected failures,
zero tests, missing runner results, timeouts and runner errors fail the gate.
Unexpected passes remain visible in the regression-only summary. Review and
remove resolved entries rather than using the allow-list to conceal errors.
`--source` reuses a verified source checkout; `--no-build` requires current normal
and separate fixture binaries. `--only NAME.def` is diagnostic and marks
`complete_suite=false`; it cannot establish a complete-suite result.
