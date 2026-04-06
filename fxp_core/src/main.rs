use fxp_core::server;

#[tokio::main]
async fn main() {
    // Start the FXP TCP server
    server::start_server().await;
}
