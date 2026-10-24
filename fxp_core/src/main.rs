// SPDX-License-Identifier: MIT

use fxp_core::server;
use tracing_subscriber::EnvFilter;

#[tokio::main]
async fn main() {
    // RUST_LOG controls verbosity (e.g. `RUST_LOG=fxp_core=debug`); default is info.
    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")),
        )
        .with_writer(std::io::stderr)
        .init();
    // Start the FXP TCP server
    server::start_server().await;
}
