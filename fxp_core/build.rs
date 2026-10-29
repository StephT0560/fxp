// SPDX-License-Identifier: MIT

fn git_output(args: &[&str]) -> Option<String> {
    std::process::Command::new("git")
        .args(args)
        .output()
        .ok()
        .filter(|o| o.status.success())
        .and_then(|o| String::from_utf8(o.stdout).ok())
        .map(|s| s.trim().to_string())
}

fn main() {
    println!("cargo:rerun-if-env-changed=FXP_BUILD_GIT_SHA");
    // Explicit rerun directives replace Cargo's default whole-package scan.
    println!("cargo:rerun-if-changed=proto");
    // Linked worktrees have a .git file; Git resolves HEAD to the worktree's
    // metadata and shared refs/packed-refs to the common repository directory.
    if std::path::Path::new("../.git").is_file() {
        println!("cargo:rerun-if-changed=../.git");
    }
    for name in ["HEAD", "refs", "packed-refs"] {
        if let Some(path) = git_output(&["rev-parse", "--path-format=absolute", "--git-path", name])
            && std::path::Path::new(&path).exists()
        {
            println!("cargo:rerun-if-changed={path}");
        }
    }
    let sha = std::env::var("FXP_BUILD_GIT_SHA")
        .ok()
        .or_else(|| git_output(&["rev-parse", "HEAD"]))
        .unwrap_or_else(|| "unknown".into());
    println!("cargo:rustc-env=FXP_BUILD_GIT_SHA={}", sha.trim());
    prost_build::Config::new()
        .compile_protos(&["proto/fxp.proto"], &["proto"])
        .unwrap();
}
