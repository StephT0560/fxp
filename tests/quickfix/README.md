# QuickFIX FIX44 acceptance gate

The definitions and Linux runner come from QuickFIX C++ v1.15.1, commit
`6908dc8c1084eeb7af7f322d35216e9965684489`. The script fetches that exact
commit over the existing Git HTTPS route and rejects a modified tracked test
tree. It copies fixtures into ignored `target/quickfix-acceptance/runner`.
The upstream Windows-only `test/atrun` process wrapper cannot run on Linux;
this uses the same suite's official POSIX entrypoint, `test/Runner.rb`, as
invoked by upstream `test/runat.sh`. No alternate FIX engine replaces FXP.

This product includes software developed by quickfixengine.org
(http://www.quickfixengine.org/). The upstream QuickFIX license is preserved
in [LICENSE.quickfix](../../fix_bridge/codec/spec/LICENSE.quickfix).

## Adapter scope

The only input change adds the venue's generated Logon password to otherwise
unframed Logon definitions. Explicit invalid BodyLength/checksum logons remain
untouched. No assertions, required business fields, enums, sequence numbers or
group counts are rewritten. The Ruby adapter runs the upstream reader,
reflector and comparator. It permits reordered non-group fields, normalizes
free-form diagnostic Text (requiring nonempty Text if the fixture requires
it), and permits optional diagnostic Text on Logout. Repeating-field bodies
are never reordered. Actual BodyLength and checksum are independently checked
instead of comparing fixture constants with different timestamp precisions.

These adaptations matter: raw QuickFIX fixtures assume an unauthenticated
application echo server and specific optional formatting choices. FXP remains
an authenticated matching venue. `expected-failures.json` lists each remaining
incompatibility with its reason. The entire definition still runs; it is never
silently skipped. Some definitions fail on their application echo before
reaching the session probe named in the filename; no claim is made that those
later probes executed.

## Gate and results

All 59 definitions are run on every PR in `quickfix-acceptance`. Unexpected
failures, stale/empty allow-list entries, missing runner results, zero-test
runs and process timeouts fail the gate. An improvement in an allow-listed
case is reported as an unexpected pass without failing a regression-only gate.
A runner failure cannot be hidden by the allow-list. Tests exercise these gate
rules. Each definition runs against a fresh bridge/store; the core and gateway
run for the suite. Backend state is not claimed to be isolated per definition.

Observed full local run: **28 passed, 31 failed and explicitly allow-listed,
0 unexpected failures, 0 runner errors (59 definitions)**. This is a regression
baseline, not FIX certification. Full results and individual diagnostics are
written to `target/quickfix-acceptance/results.json` and `*.runner.log` and
uploaded by CI. Revisit the allow-list when adding session buffering, routed
sessions, error-Logout handshakes, or broader FIX application coverage.
