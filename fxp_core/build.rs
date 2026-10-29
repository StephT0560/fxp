// SPDX-License-Identifier: MIT

fn main() {
    println!("cargo:rerun-if-env-changed=FXP_BUILD_GIT_SHA");
    println!("cargo:rerun-if-changed=../.git/HEAD");
    println!("cargo:rerun-if-changed=../.git/refs");
    let sha = std::env::var("FXP_BUILD_GIT_SHA")
        .ok()
        .or_else(|| {
            std::process::Command::new("git")
                .args(["rev-parse", "HEAD"])
                .output()
                .ok()
                .filter(|o| o.status.success())
                .and_then(|o| String::from_utf8(o.stdout).ok())
        })
        .unwrap_or_else(|| "unknown".into());
    println!("cargo:rustc-env=FXP_BUILD_GIT_SHA={}", sha.trim());
    prost_build::Config::new()
        .compile_protos(&["proto/fxp.proto"], &["proto"])
        .unwrap();
}
