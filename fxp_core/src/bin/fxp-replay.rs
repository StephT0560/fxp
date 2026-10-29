// SPDX-License-Identifier: MIT
fn main() {
    let args: Result<Vec<_>, _> = std::env::args_os()
        .skip(1)
        .map(|s| s.into_string())
        .collect();
    let args = match args {
        Ok(args) => args,
        Err(_) => {
            eprintln!("invalid: arguments must be UTF-8");
            std::process::exit(2);
        }
    };
    let code = fxp_core::replay_cli::run(
        args,
        &mut std::io::stdout().lock(),
        &mut std::io::stderr().lock(),
    );
    std::process::exit(code);
}
