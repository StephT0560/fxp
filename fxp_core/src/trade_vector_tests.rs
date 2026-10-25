use crate::fxp;
use prost::Message;
use prost_types::Timestamp;
use std::fs;
use std::path::PathBuf;

fn expected_trade_report() -> fxp::TradeCaptureReport {
    fxp::TradeCaptureReport {
        header: Some(fxp::FxpHeader {
            protocol_version: "2.5".to_string(),
            sequence_number: 0,
        }),
        trade_id: "EURUSD:7".to_string(),
        buy_order_id: String::new(),
        sell_order_id: String::new(),
        symbol: "EURUSD".to_string(),
        trade_price: 108_250,
        trade_quantity: 30,
        timestamp: Some(Timestamp {
            seconds: 1_700_000_000,
            nanos: 123_000_000,
        }),
        aggressor_side: fxp::OrderSide::Sell as i32,
        seq: 7,
        is_auction: true,
    }
}

fn decode_hex(input: &str) -> Vec<u8> {
    let input = input.trim();
    input
        .as_bytes()
        .as_chunks::<2>()
        .0
        .iter()
        .map(|pair| {
            let pair = std::str::from_utf8(pair).expect("hex vector is ASCII");
            u8::from_str_radix(pair, 16).expect("hex vector contains valid bytes")
        })
        .collect()
}

fn vector_path(name: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../tests/vectors")
        .join(name)
}

#[test]
fn go_generated_trade_vector_round_trips_with_prost() {
    let bytes = decode_hex(include_str!(
        "../../tests/vectors/trade_capture_report_v2_5.hex"
    ));
    let expected = expected_trade_report();
    assert_eq!(expected.encode_to_vec(), bytes);
    assert_eq!(
        fxp::TradeCaptureReport::decode(bytes.as_slice()).unwrap(),
        expected
    );
}

#[test]
fn trade_vector_generate() {
    if std::env::var("FXP_UPDATE_VECTORS").as_deref() != Ok("1") {
        return;
    }
    let bytes = expected_trade_report().encode_to_vec();
    let hex = bytes
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect::<String>();
    fs::write(
        vector_path("trade_capture_report_v2_5.rust.hex"),
        format!("{hex}\n"),
    )
    .unwrap();
}
