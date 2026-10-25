// SPDX-License-Identifier: MIT
#[path = "../benches/support/encoding.rs"]
mod encoding;
use encoding::*;

fn input() -> NewOrderInput<'static> {
    NewOrderInput {
        seq: 1_048_576,
        order_id: "ORD-2024-01-15-000042",
        sender: "GOLDMANSACHS01",
        receiver: "NASDAQOPTIONS",
        symbol: "AAPL",
        price: 18542,
        quantity: 500,
        seconds: encoding::TRANSACT_SECONDS,
        nanos: 0,
        transact_time: "20240115-12:00:00.000",
    }
}
#[test]
fn reused_fix_buffer_has_correct_body_length_checksum_and_dynamic_fields() {
    let mut buffer = FixBuffer::new();
    for (seq, price, qty) in [(1, 18542, 500), (9999999, 300000, 1), (0, 1, 2147483647)] {
        let i = NewOrderInput {
            seq,
            price,
            quantity: qty,
            ..input()
        };
        let frame = buffer.new_order(&i);
        assert_eq!(frame, encode_fix_format(&i));
        let text = std::str::from_utf8(frame).unwrap();
        let parts: Vec<_> = text.split('\x01').collect();
        let body_start = frame
            .iter()
            .enumerate()
            .filter(|(_, c)| **c == 1)
            .nth(1)
            .unwrap()
            .0
            + 1;
        let trailer = frame.len() - 7;
        assert_eq!(
            parts[1]
                .strip_prefix("9=")
                .unwrap()
                .parse::<usize>()
                .unwrap(),
            trailer - body_start
        );
        assert_eq!(
            parts[parts.len() - 2]
                .strip_prefix("10=")
                .unwrap()
                .parse::<u8>()
                .unwrap(),
            frame[..trailer].iter().fold(0u8, |a, &b| a.wrapping_add(b))
        );
        assert!(parts.contains(&format!("34={seq}").as_str()));
        assert!(parts.contains(&format!("38={qty}").as_str()));
        assert_eq!(text.matches("8=FIX.4.4").count(), 1);
    }
}
#[test]
fn sbe_roundtrip_preserves_strings_numbers_and_timestamp_and_checks_layout() {
    let input = input();
    let bytes = encode_sbe(&input).unwrap();
    let decoded = decode_sbe_owned(&bytes).unwrap();
    assert_eq!(decoded.order_id, input.order_id);
    assert_eq!(decoded.sender, input.sender);
    assert_eq!(decoded.receiver, input.receiver);
    assert_eq!(decoded.symbol, input.symbol);
    assert_eq!(
        (decoded.seq, decoded.price, decoded.quantity),
        (input.seq, input.price, input.quantity)
    );
    assert_eq!(
        (decoded.seconds, decoded.nanos),
        (input.seconds, input.nanos)
    );
    let mut bad = bytes;
    bad[2] = 99;
    assert!(decode_sbe_owned(&bad).is_err());
    assert!(
        encode_sbe(&NewOrderInput {
            symbol: "TOOLONG",
            ..input
        })
        .is_err()
    );
}
