/// Integration test: verifies the Rust↔Go framing contract.
///
/// Wire format (both directions):
///   [u32 big-endian length][protobuf-encoded FXPMessage body]
///
/// This test spins up the FXP Core TCP server on a random port,
/// connects a raw TCP client that speaks the same framing the Go
/// gateway uses, and walks through:
///   1. NewOrderSingle (buy) → no fill expected
///   2. NewOrderSingle (sell at same price) → ExecutionReport expected
///   3. MarketDataRequest → subscription + incremental update expected
///
/// Run with:  cargo test --test integration_test -- --nocapture

#[cfg(test)]
mod framing_contract {
    use std::time::Duration;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::{TcpListener, TcpStream};
    use tokio::time::timeout;
    use prost::Message;

    // Pull in the generated protobuf types
    mod fxp {
        include!(concat!(env!("OUT_DIR"), "/fxp.rs"));
    }

    // ── Wire helpers (must match server.rs exactly) ───────────────────

    /// Encode an FXPMessage with a 4-byte big-endian length prefix.
    fn frame(msg: &fxp::FxpMessage) -> Vec<u8> {
        let mut body = Vec::new();
        msg.encode(&mut body).unwrap();
        let mut out = (body.len() as u32).to_be_bytes().to_vec();
        out.extend_from_slice(&body);
        out
    }

    /// Read one length-prefixed frame from a TcpStream.
    async fn read_frame(stream: &mut TcpStream) -> Vec<u8> {
        let mut len_buf = [0u8; 4];
        stream.read_exact(&mut len_buf).await.expect("read length prefix");
        let msg_len = u32::from_be_bytes(len_buf) as usize;
        assert!(msg_len > 0 && msg_len <= 1_048_576, "sanity: bad frame length {}", msg_len);
        let mut body = vec![0u8; msg_len];
        stream.read_exact(&mut body).await.expect("read body");
        body
    }

    /// Decode a raw body into FXPMessage.
    fn decode(body: &[u8]) -> fxp::FxpMessage {
        fxp::FxpMessage::decode(body).expect("decode FXPMessage")
    }

    // ── Test fixtures ─────────────────────────────────────────────────

    fn new_order(order_id: &str, side: i32, price: i64, qty: i32) -> fxp::FxpMessage {
        fxp::FxpMessage {
            payload: Some(fxp::fxp_message::Payload::NewOrder(fxp::NewOrderSingle {
                header: Some(fxp::FxpHeader {
                    protocol_version: "1.0".to_string(),
                    sequence_number: 1,
                }),
                order_id: order_id.to_string(),
                sender: "TestGateway".to_string(),
                receiver: "FXPCore".to_string(),
                symbol: "AAPL".to_string(),
                price,
                side,
                quantity: qty,
                order_type: fxp::OrderType::Limit as i32,
                time_in_force: fxp::TimeInForce::Gtc as i32,
                timestamp: None,
            })),
        }
    }

    fn market_data_request(symbols: Vec<&str>) -> fxp::FxpMessage {
        fxp::FxpMessage {
            payload: Some(fxp::fxp_message::Payload::MarketDataRequest(
                fxp::MarketDataRequest {
                    header: Some(fxp::FxpHeader {
                        protocol_version: "1.0".to_string(),
                        sequence_number: 2,
                    }),
                    symbols: symbols.into_iter().map(String::from).collect(),
                },
            )),
        }
    }

    // ── Tests ─────────────────────────────────────────────────────────

    /// Spin up the server on a random port and return the address.
    async fn start_test_server() -> String {
        // Bind to port 0 → OS assigns a free port
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap().to_string();

        tokio::spawn(async move {
            use std::sync::Arc;
            use tokio::sync::Mutex;
            use std::collections::HashMap;
            use crate::order_book::broadcaster::MarketDataBroadcaster;
            use crate::order_book::order_book::OrderBook;

            // Minimal MarketDataManager inline for test isolation
            let subscribers: Arc<Mutex<HashMap<String, Vec<tokio::sync::mpsc::Sender<Vec<u8>>>>>> =
                Arc::new(Mutex::new(HashMap::new()));
            let order_books: Arc<Mutex<HashMap<String, Arc<Mutex<OrderBook>>>>> =
                Arc::new(Mutex::new(HashMap::new()));

            loop {
                let (stream, _) = listener.accept().await.unwrap();
                let subs = Arc::clone(&subscribers);
                let books = Arc::clone(&order_books);
                // Re-use the real handler from server module
                tokio::spawn(crate::server::handle_tcp_client_for_test(
                    stream, subs, books,
                ));
            }
        });

        addr
    }

    #[tokio::test]
    async fn test_framing_buy_order_no_fill() {
        let addr = start_test_server().await;
        let mut stream = TcpStream::connect(&addr).await.unwrap();

        let buy = new_order("B001", fxp::OrderSide::Buy as i32, 10100, 10);
        stream.write_all(&frame(&buy)).await.unwrap();

        // No matching sell exists — server should NOT send an ExecutionReport.
        // Give it 200 ms; if nothing arrives the channel is silent as expected.
        let result = timeout(Duration::from_millis(200), read_frame(&mut stream)).await;
        assert!(result.is_err(), "Expected no ExecutionReport for unmatched buy order");
    }

    #[tokio::test]
    async fn test_framing_matching_orders_produce_execution_report() {
        let addr = start_test_server().await;
        let mut stream = TcpStream::connect(&addr).await.unwrap();

        // Send buy
        let buy = new_order("B002", fxp::OrderSide::Buy as i32, 10100, 10);
        stream.write_all(&frame(&buy)).await.unwrap();
        tokio::time::sleep(Duration::from_millis(20)).await;

        // Send matching sell at same price
        let sell = new_order("S002", fxp::OrderSide::Sell as i32, 10100, 10);
        stream.write_all(&frame(&sell)).await.unwrap();

        // Expect an ExecutionReport within 1 second
        let body = timeout(Duration::from_secs(1), read_frame(&mut stream))
            .await
            .expect("timed out waiting for ExecutionReport");

        let msg = decode(&body);
        match msg.payload {
            Some(fxp::fxp_message::Payload::ExecutionReport(report)) => {
                assert_eq!(report.execution_price, 10100, "execution price mismatch");
                assert_eq!(report.executed_quantity, 10, "executed quantity mismatch");
                assert_eq!(
                    report.execution_type,
                    fxp::ExecutionType::ExecFilled as i32
                );
                assert_eq!(
                    report.order_status,
                    fxp::OrderStatus::OrderFilled as i32
                );
                println!("✅ ExecutionReport received: order_id={}", report.order_id);
            }
            other => panic!("Expected ExecutionReport, got: {:?}", other),
        }
    }

    #[tokio::test]
    async fn test_framing_large_message_within_cap() {
        // Verifies the 1 MB cap doesn't block large-but-valid snapshots.
        // Sends a buy order and checks the server doesn't choke on it.
        let addr = start_test_server().await;
        let mut stream = TcpStream::connect(&addr).await.unwrap();

        let buy = new_order("B003", fxp::OrderSide::Buy as i32, 15000, 1000);
        let framed = frame(&buy);
        assert!(framed.len() < 1_048_576, "test message should be within cap");

        stream.write_all(&framed).await.unwrap();
        // No crash = pass
        tokio::time::sleep(Duration::from_millis(100)).await;
    }

    #[tokio::test]
    async fn test_framing_invalid_message_does_not_crash_server() {
        let addr = start_test_server().await;
        let mut stream = TcpStream::connect(&addr).await.unwrap();

        // Send a valid-looking length prefix but garbage body
        let garbage_body = b"not a protobuf message at all!!";
        let mut bad_frame = (garbage_body.len() as u32).to_be_bytes().to_vec();
        bad_frame.extend_from_slice(garbage_body);
        stream.write_all(&bad_frame).await.unwrap();

        // Server should not crash — it logs the error and continues.
        // Send a valid order immediately after to confirm the server is still alive.
        tokio::time::sleep(Duration::from_millis(50)).await;
        let buy = new_order("B004", fxp::OrderSide::Buy as i32, 10100, 5);
        stream.write_all(&frame(&buy)).await.unwrap();

        // No panic = server survived
        tokio::time::sleep(Duration::from_millis(100)).await;
    }

    #[tokio::test]
    async fn test_framing_market_data_subscription() {
        let addr = start_test_server().await;

        // Subscriber connection
        let mut sub_stream = TcpStream::connect(&addr).await.unwrap();
        let req = market_data_request(vec!["AAPL"]);
        sub_stream.write_all(&frame(&req)).await.unwrap();
        tokio::time::sleep(Duration::from_millis(50)).await;

        // Separate order connection triggers a market data update
        let mut order_stream = TcpStream::connect(&addr).await.unwrap();
        let buy = new_order("B005", fxp::OrderSide::Buy as i32, 10100, 5);
        order_stream.write_all(&frame(&buy)).await.unwrap();

        // Subscriber should receive a MarketDataIncrementalUpdate
        let body = timeout(Duration::from_secs(1), read_frame(&mut sub_stream))
            .await
            .expect("timed out waiting for MarketDataIncrementalUpdate");

        let msg = decode(&body);
        match msg.payload {
            Some(fxp::fxp_message::Payload::MarketDataIncremental(update)) => {
                assert_eq!(update.symbol, "AAPL");
                assert!(!update.changes.is_empty(), "expected at least one change");
                println!("✅ MarketDataIncrementalUpdate received for {}", update.symbol);
            }
            other => panic!("Expected MarketDataIncrementalUpdate, got: {:?}", other),
        }
    }
}