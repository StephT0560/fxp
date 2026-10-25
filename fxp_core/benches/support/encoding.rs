// SPDX-License-Identifier: MIT
//! Testable benchmark encoders, not production FIX/SBE implementations.
#[derive(Clone, Copy)]
pub struct NewOrderInput<'a> {
    pub seq: u64,
    pub order_id: &'a str,
    pub sender: &'a str,
    pub receiver: &'a str,
    pub symbol: &'a str,
    pub price: i64,
    pub quantity: i32,
    pub seconds: i64,
    pub nanos: i32,
    pub transact_time: &'a str,
}
pub const TRANSACT_SECONDS: i64 = 1_705_320_000; // 2024-01-15T12:00:00Z
pub const SBE_SIZE: usize = 95; // Includes DAY TIF and timestamp, unlike the old 82B subset.

/// Two reusable buffers, matching the body/frame construction of a FIX writer.
/// Decimal conversion uses stack scratch space; no format!, floating point or
/// per-field heap allocation. Capacity growth is excluded by warming each buffer.
pub struct FixBuffer {
    body: Vec<u8>,
    frame: Vec<u8>,
}
impl FixBuffer {
    pub fn new() -> Self {
        Self {
            body: Vec::with_capacity(1024),
            frame: Vec::with_capacity(1024),
        }
    }
    pub fn clear(&mut self) {
        self.body.clear();
        self.frame.clear()
    }
    pub fn field(&mut self, tag: &[u8], value: &[u8]) {
        self.body.extend_from_slice(tag);
        self.body.push(b'=');
        self.body.extend_from_slice(value);
        self.body.push(1)
    }
    pub fn integer(&mut self, tag: &[u8], value: i64) {
        let mut digits = [0u8; 20];
        self.field(tag, decimal(value, &mut digits))
    }
    pub fn unsigned_integer(&mut self, tag: &[u8], value: u64) {
        let mut digits = [0u8; 20];
        self.field(tag, unsigned_decimal(value, &mut digits))
    }
    pub fn price(&mut self, tag: &[u8], value: i64) {
        let mut digits = [0u8; 32];
        let mut scratch = [0u8; 20];
        let magnitude = value.unsigned_abs();
        let mut n = 0;
        if value < 0 {
            digits[n] = b'-';
            n += 1
        };
        let whole = unsigned_decimal(magnitude / 100, &mut scratch);
        digits[n..n + whole.len()].copy_from_slice(whole);
        n += whole.len();
        digits[n] = b'.';
        digits[n + 1] = b'0' + (magnitude % 100 / 10) as u8;
        digits[n + 2] = b'0' + (magnitude % 10) as u8;
        self.field(tag, &digits[..n + 3])
    }
    pub fn finish(&mut self) -> &[u8] {
        self.frame.clear();
        self.frame.extend_from_slice(b"8=FIX.4.4\x019=");
        let mut scratch = [0u8; 20];
        self.frame
            .extend_from_slice(unsigned_decimal(self.body.len() as u64, &mut scratch));
        self.frame.push(1);
        self.frame.extend_from_slice(&self.body);
        let checksum = self.frame.iter().fold(0u8, |a, &b| a.wrapping_add(b));
        self.frame.extend_from_slice(b"10=");
        self.frame.extend_from_slice(&[
            b'0' + checksum / 100,
            b'0' + checksum / 10 % 10,
            b'0' + checksum % 10,
            1,
        ]);
        &self.frame
    }
    pub fn new_order(&mut self, i: &NewOrderInput<'_>) -> &[u8] {
        self.clear();
        self.field(b"35", b"D");
        self.unsigned_integer(b"34", i.seq);
        self.field(b"49", i.sender.as_bytes());
        self.field(b"56", i.receiver.as_bytes());
        self.field(b"11", i.order_id.as_bytes());
        self.field(b"55", i.symbol.as_bytes());
        self.field(b"54", b"1");
        self.integer(b"38", i.quantity as i64);
        self.field(b"40", b"2");
        self.price(b"44", i.price);
        self.field(b"59", b"0");
        self.field(b"60", i.transact_time.as_bytes());
        self.finish()
    }
}
fn unsigned_decimal(mut value: u64, scratch: &mut [u8; 20]) -> &[u8] {
    let mut at = scratch.len();
    loop {
        at -= 1;
        scratch[at] = b'0' + (value % 10) as u8;
        value /= 10;
        if value == 0 {
            break;
        }
    }
    &scratch[at..]
}
fn decimal(value: i64, scratch: &mut [u8; 20]) -> &[u8] {
    let negative = value < 0;
    let mut magnitude = value.unsigned_abs();
    let mut at = scratch.len();
    loop {
        at -= 1;
        scratch[at] = b'0' + (magnitude % 10) as u8;
        magnitude /= 10;
        if magnitude == 0 {
            break;
        }
    }
    if negative {
        at -= 1;
        scratch[at] = b'-'
    };
    &scratch[at..]
}

pub fn encode_sbe(i: &NewOrderInput<'_>) -> Result<[u8; SBE_SIZE], &'static str> {
    let mut b = [0u8; SBE_SIZE];
    b[..2].copy_from_slice(&((SBE_SIZE - 8) as u16).to_le_bytes());
    b[2..4].copy_from_slice(&14u16.to_le_bytes());
    b[4..6].copy_from_slice(&1u16.to_le_bytes());
    b[6..8].copy_from_slice(&1u16.to_le_bytes());
    b[8..16].copy_from_slice(&i.seq.to_le_bytes());
    for (range, value) in [
        (16..37, i.order_id),
        (37..51, i.sender),
        (51..64, i.receiver),
        (64..68, i.symbol),
    ] {
        let dst = &mut b[range];
        if value.len() > dst.len() {
            return Err("SBE fixture fixed-width string overflow");
        };
        dst[..value.len()].copy_from_slice(value.as_bytes())
    }
    b[68..76].copy_from_slice(&i.price.to_le_bytes());
    b[76..80].copy_from_slice(&i.quantity.to_le_bytes());
    b[80] = 0;
    b[81] = 2;
    b[82] = 0;
    b[83..91].copy_from_slice(&i.seconds.to_le_bytes());
    b[91..95].copy_from_slice(&i.nanos.to_le_bytes());
    Ok(b)
}
#[derive(Debug, PartialEq, Eq)]
pub struct OwnedOrder {
    pub seq: u64,
    pub order_id: String,
    pub sender: String,
    pub receiver: String,
    pub symbol: String,
    pub price: i64,
    pub quantity: i32,
    pub seconds: i64,
    pub nanos: i32,
    pub side: u8,
    pub order_type: u8,
    pub tif: u8,
}
pub fn decode_sbe_owned(b: &[u8; SBE_SIZE]) -> Result<OwnedOrder, &'static str> {
    if b[..8] != [87, 0, 14, 0, 1, 0, 1, 0] {
        return Err("bad SBE fixture header");
    }
    fn text(b: &[u8]) -> Result<String, &'static str> {
        let end = b.iter().position(|&c| c == 0).unwrap_or(b.len());
        std::str::from_utf8(&b[..end])
            .map(str::to_owned)
            .map_err(|_| "invalid SBE UTF-8")
    }
    Ok(OwnedOrder {
        seq: u64::from_le_bytes(b[8..16].try_into().unwrap()),
        order_id: text(&b[16..37])?,
        sender: text(&b[37..51])?,
        receiver: text(&b[51..64])?,
        symbol: text(&b[64..68])?,
        price: i64::from_le_bytes(b[68..76].try_into().unwrap()),
        quantity: i32::from_le_bytes(b[76..80].try_into().unwrap()),
        seconds: i64::from_le_bytes(b[83..91].try_into().unwrap()),
        nanos: i32::from_le_bytes(b[91..95].try_into().unwrap()),
        side: b[80],
        order_type: b[81],
        tif: b[82],
    })
}

/// Allocating reference, retained to make the cost of formatting visible.
pub fn encode_fix_format(i: &NewOrderInput<'_>) -> Vec<u8> {
    let body = format!(
        "35=D\x0134={}\x0149={}\x0156={}\x0111={}\x0155={}\x0154=1\x0138={}\x0140=2\x0144={:.2}\x0159=0\x0160={}\x01",
        i.seq,
        i.sender,
        i.receiver,
        i.order_id,
        i.symbol,
        i.quantity,
        i.price as f64 / 100.0,
        i.transact_time
    );
    let full = format!("8=FIX.4.4\x019={}\x01{}", body.len(), body);
    let cs = full.bytes().fold(0u8, u8::wrapping_add);
    format!("{}10={:03}\x01", full, cs).into_bytes()
}
