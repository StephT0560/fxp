// SPDX-License-Identifier: MIT

use std::io::IsTerminal;

use fxp_core::server;
#[cfg(feature = "dhat")]
#[global_allocator]
static ALLOCATOR: dhat::Alloc = dhat::Alloc;
use tracing_subscriber::EnvFilter;

#[tokio::main]
async fn main() {
    #[cfg(feature = "dhat")]
    let _heap_profiler = dhat::Profiler::builder()
        .file_name(std::env::var("FXP_DHAT_FILE").unwrap_or_else(|_| "dhat-heap.json".into()))
        .build();
    // RUST_LOG controls verbosity (e.g. `RUST_LOG=fxp_core=debug`); default is info.
    tracing_subscriber::fmt()
        .with_env_filter(
            EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")),
        )
        .with_writer(std::io::stderr)
        .with_ansi(std::io::stderr().is_terminal())
        .init();
    fxp_core::diagnostics::init().expect("diagnostic trace file");
    if let Err(error) = server::start_server(shutdown_signal()).await {
        tracing::error!("{error}");
        std::process::exit(1);
    }
    fxp_core::diagnostics::flush();
}

async fn shutdown_signal() {
    #[cfg(unix)]
    {
        let mut terminate =
            tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
                .expect("register SIGTERM handler");
        tokio::select! {
            result = tokio::signal::ctrl_c() => {
                if let Err(error) = result {
                    tracing::error!("failed to listen for SIGINT: {error}");
                }
            }
            _ = terminate.recv() => tracing::info!("received SIGTERM"),
        }
    }
    #[cfg(not(unix))]
    if let Err(error) = tokio::signal::ctrl_c().await {
        tracing::error!("failed to listen for shutdown signal: {error}");
    }
}
