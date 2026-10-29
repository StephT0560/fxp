// SPDX-License-Identifier: MIT
use std::path::PathBuf;
use std::process::Command;
fn cli(args: &[&str]) -> std::process::Output {
    Command::new(env!("CARGO_BIN_EXE_fxp-replay"))
        .args(args)
        .output()
        .unwrap()
}
fn golden(name: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/bundles")
        .join(name)
}
#[test]
fn golden_bundles_verify_and_timeline_snapshots() {
    for name in [
        "partial-fill",
        "stp",
        "reject",
        "expiry",
        "auction",
        "iceberg",
        "mass-cancel",
        "stop",
        "replace",
        "disconnect",
    ] {
        let dir = golden(name);
        let verify = cli(&["verify", dir.to_str().unwrap()]);
        assert_eq!(
            verify.status.code(),
            Some(0),
            "{name}: {}",
            String::from_utf8_lossy(&verify.stderr)
        );
        let target = match name {
            "reject" => "BAD",
            "expiry" => "DAY",
            "iceberg" => "ICE",
            "mass-cancel" | "disconnect" => "B1",
            "stop" => "STOP",
            _ => "B",
        };
        let explained = cli(&["explain", dir.to_str().unwrap(), "--order", target]);
        assert_eq!(explained.status.code(), Some(0));
        assert_eq!(
            String::from_utf8(explained.stdout).unwrap(),
            std::fs::read_to_string(dir.join("explain.text")).unwrap(),
            "{name}/explain"
        );
        for format in ["text", "json"] {
            let mut args = vec!["timeline", dir.to_str().unwrap()];
            if format == "json" {
                args.push("--json");
            }
            let out = cli(&args);
            assert!(out.status.success());
            assert_eq!(
                String::from_utf8(out.stdout).unwrap(),
                std::fs::read_to_string(dir.join(format!("timeline.{format}"))).unwrap(),
                "{name}/{format}"
            );
        }
    }
}
#[test]
fn usage_is_invalid_not_divergence() {
    for args in [
        vec![],
        vec!["verify"],
        vec!["timeline", "missing", "--wat"],
        vec!["explain", "missing"],
        vec!["export", "missing"],
        vec!["verify", "missing"],
    ] {
        assert_eq!(cli(&args).status.code(), Some(2));
    }
}

struct Temp(PathBuf);
impl Temp {
    fn copy(name: &str) -> Self {
        static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../work/replay-cli-tests")
            .join(format!(
                "{}-{}",
                std::process::id(),
                NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
            ));
        std::fs::create_dir_all(&dir).unwrap();
        for entry in std::fs::read_dir(golden(name)).unwrap() {
            let entry = entry.unwrap();
            if entry.file_type().unwrap().is_file() {
                std::fs::copy(entry.path(), dir.join(entry.file_name())).unwrap();
            }
        }
        Self(dir)
    }
    fn path(&self) -> &str {
        self.0.to_str().unwrap()
    }
    fn edit(&self, script: &str) {
        assert!(
            Command::new("python3")
                .args(["-c", script, self.path()])
                .status()
                .unwrap()
                .success()
        );
    }
}
impl Drop for Temp {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}
#[test]
fn filters_aliases_explain_export_and_diff() {
    let fixture = golden("partial-fill");
    let path = fixture.to_str().unwrap();
    let timeline = cli(&["timeline", path, "--order", "B"]);
    let s = String::from_utf8(timeline.stdout).unwrap();
    assert!(s.contains("accepted"));
    assert!(s.contains("role=resting counterparty=anon:"));
    assert!(!s.contains("counterparty=S"));
    assert!(!s.contains("TEST S owner="));
    let scoped = String::from_utf8(
        cli(&[
            "timeline",
            path,
            "--owner",
            "alice",
            "--client-order-id",
            "c-B",
        ])
        .stdout,
    )
    .unwrap();
    assert!(scoped.contains("counterparty=S"));
    assert!(!scoped.contains("owner=bob"));
    let range = String::from_utf8(
        cli(&[
            "timeline", path, "--symbol", "TEST", "--from", "2", "--to", "2",
        ])
        .stdout,
    )
    .unwrap();
    assert!(!range.contains("1:0"));
    assert!(range.contains("2:0"));
    assert_eq!(
        String::from_utf8(cli(&["timeline", path, "--symbol", "OTHER"]).stdout).unwrap(),
        "timeline source=recorded verified=true\n"
    );
    let replaced = golden("replace");
    let p = replaced.to_str().unwrap();
    let aliases = String::from_utf8(cli(&["timeline", p, "--order", "B"]).stdout).unwrap();
    assert!(aliases.contains("TEST B2"));
    assert!(aliases.contains("rejected cause=negative replace fields"));
    assert!(aliases.contains("canceled cause=user"));
    let explain = cli(&["explain", p, "--order", "B"]);
    let explanation = String::from_utf8(explain.stdout).unwrap();
    assert!(explanation.contains("replace rejected: negative replace fields"));
    assert!(explanation.contains("canceled: user"));
    assert!(explanation.lines().count() < 8);
    let exported = cli(&["export", path, "--jsonl", "--order", "B"]);
    assert!(exported.status.success());
    let mut parser=Command::new("python3").args(["-c","import json,sys; rows=[json.loads(l) for l in sys.stdin]; assert rows; assert all(r['order_id']=='B' for r in rows); assert all(not r['data'].get('counterparty') or r['data']['counterparty'].startswith('anon:') for r in rows)"]).stdin(std::process::Stdio::piped()).spawn().unwrap();
    use std::io::Write;
    parser
        .stdin
        .take()
        .unwrap()
        .write_all(&exported.stdout)
        .unwrap();
    assert!(parser.wait().unwrap().success());
    assert_eq!(cli(&["diff", path, path]).status.code(), Some(0));
    let other = golden("reject");
    let diff = cli(&["diff", path, other.to_str().unwrap(), "--json"]);
    assert_eq!(diff.status.code(), Some(1));
    let d = String::from_utf8(diff.stdout).unwrap();
    assert!(d.contains("rejects_by_reason"));
    assert!(d.contains("first_divergence"));
    assert!(d.contains("global_seq"));
}
#[test]
fn negative_bundles_and_exit_codes() {
    let tampered = Temp::copy("partial-fill");
    tampered.edit("import sys,pathlib; p=pathlib.Path(sys.argv[1])/'commands.log'; b=bytearray(p.read_bytes()); b[-1]^=1; p.write_bytes(b)");
    assert_eq!(cli(&["verify", tampered.path()]).status.code(), Some(2));
    let version = Temp::copy("partial-fill");
    version.edit("import sys,pathlib,json; p=pathlib.Path(sys.argv[1])/'manifest.json'; j=json.loads(p.read_text()); j['bundle_format']='fxp-run-v999'; p.write_text(json.dumps(j))");
    assert_eq!(cli(&["verify", version.path()]).status.code(), Some(2));
    let partial_metadata = Temp::copy("partial-fill");
    partial_metadata.edit("import sys,pathlib,json; p=pathlib.Path(sys.argv[1])/'manifest.json'; j=json.loads(p.read_text()); j.pop('trace_version'); p.write_text(json.dumps(j))");
    assert_eq!(
        cli(&["verify", partial_metadata.path()]).status.code(),
        Some(2)
    );
    let trace = Temp::copy("partial-fill");
    trace.edit("import sys,pathlib; p=pathlib.Path(sys.argv[1])/'events.jsonl'; p.write_text(p.read_text()+'\\n')");
    assert_eq!(cli(&["verify", trace.path()]).status.code(), Some(2));
    let incomplete = Temp::copy("partial-fill");
    incomplete.edit("import sys,pathlib,json; p=pathlib.Path(sys.argv[1])/'manifest.json'; j=json.loads(p.read_text()); j['complete']=False; p.write_text(json.dumps(j))");
    assert_eq!(cli(&["verify", incomplete.path()]).status.code(), Some(2));
    let prefix = cli(&["timeline", incomplete.path()]);
    assert_eq!(prefix.status.code(), Some(0));
    assert!(String::from_utf8_lossy(&prefix.stderr).contains("unverified"));
    let legacy = Temp::copy("partial-fill");
    legacy.edit("import sys,pathlib,json; p=pathlib.Path(sys.argv[1])/'manifest.json'; j=json.loads(p.read_text()); j.pop('trace_version'); j.pop('trace_sha256'); p.write_text(json.dumps(j))");
    assert_eq!(cli(&["verify", legacy.path()]).status.code(), Some(0));
    assert!(
        String::from_utf8_lossy(&cli(&["explain", legacy.path(), "--order", "B"]).stdout)
            .contains("source=replayed verified=true")
    );
    legacy.edit("import sys,pathlib,json; p=pathlib.Path(sys.argv[1])/'expected.json'; j=json.loads(p.read_text()); j['global']['sha256']='0'*64; p.write_text(json.dumps(j))");
    let divergent = cli(&["verify", legacy.path()]);
    assert_eq!(divergent.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&divergent.stdout).contains("first event unavailable"));
}
#[test]
fn divergence_reports_exact_command_and_expected_actual_event() {
    let dir = Temp::copy("partial-fill");
    dir.edit(r#"import sys,pathlib,json,hashlib
p=pathlib.Path(sys.argv[1]); rows=[json.loads(l) for l in (p/'events.jsonl').read_text().splitlines()]
r=next(r for r in rows if r['kind']=='fill'); r['data']['quantity']+=1
b=bytearray.fromhex(r['canonical_hex']); i=b.index(bytes([0x40,4]),9); b[i+1]=5; r['canonical_hex']=b.hex()
s=''.join(json.dumps(r,separators=(',',':'),sort_keys=True)+'\n' for r in rows); (p/'events.jsonl').write_text(s)
m=json.loads((p/'manifest.json').read_text()); m['trace_sha256']=hashlib.sha256(s.encode()).hexdigest(); (p/'manifest.json').write_text(json.dumps(m))
e=json.loads((p/'expected.json').read_text()); all_bytes=b'fxp-business-v1\0'+b''.join(bytes.fromhex(r['canonical_hex']) for r in rows if r['canonical_hex']); e['global']['sha256']=hashlib.sha256(all_bytes).hexdigest(); e['symbols']['TEST']['sha256']=e['global']['sha256']; (p/'expected.json').write_text(json.dumps(e))
"#);
    let result = cli(&["verify", dir.path()]);
    assert_eq!(result.status.code(), Some(1));
    let output = String::from_utf8(result.stdout).unwrap();
    assert!(output.contains("\"global_seq\":2"));
    assert!(output.contains("\"symbol\":\"TEST\""));
    assert!(output.contains("expected_event"));
    assert!(output.contains("actual_event"));
    assert!(output.contains("\"input\""));
    assert!(output.contains("\"quantity\":5"));
    assert!(output.contains("\"quantity\":4"));
}

#[cfg(unix)]
#[test]
fn non_utf8_arguments_are_invalid_instead_of_panicking() {
    use std::os::unix::ffi::OsStringExt;
    let result = Command::new(env!("CARGO_BIN_EXE_fxp-replay"))
        .arg("verify")
        .arg(std::ffi::OsString::from_vec(vec![b'x', 255]))
        .output()
        .unwrap();
    assert_eq!(result.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&result.stderr).contains("UTF-8"));
}

#[test]
fn diff_reports_final_state_hashes_when_events_match() {
    let changed = Temp::copy("partial-fill");
    changed.edit("import sys,pathlib,json; p=pathlib.Path(sys.argv[1])/'expected.json'; j=json.loads(p.read_text()); j['final_state_sha256']='0'*64; p.write_text(json.dumps(j))");
    let original = golden("partial-fill");
    let out = cli(&["diff", original.to_str().unwrap(), changed.path(), "--json"]);
    assert_eq!(out.status.code(), Some(1));
    let s = String::from_utf8(out.stdout).unwrap();
    assert!(s.contains("expected_summary"));
    assert!(s.contains("actual_summary"));
    assert!(s.contains(&format!("\"final_state_sha256\":\"{}\"", "0".repeat(64))));
    assert!(s.contains("no event-level difference"));
}
