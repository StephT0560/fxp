// SPDX-License-Identifier: MIT

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Order {
    pub order_id: String,
    pub trader_id: String,
    pub symbol: String,
    pub price: i64, // fixed-point, cents (e.g. 10050 = $100.50)
    pub quantity: u32,
    pub side: OrderSide,
    pub order_type: OrderType,
    pub time_in_force: TimeInForce,
    pub timestamp: u64, // UNIX milliseconds
    // Extended fields from proto v1.1
    pub stop_price: i64,     // required for Stop/StopLimit (0 if not applicable)
    pub expire_date: String, // required for GTD (YYYY-MM-DD, empty if not applicable)
    pub account: String,
    pub client_order_id: String,
    pub stp: Stp,
}

impl Order {
    /// Self-trade prevention scope: the STP group if set, otherwise the owner.
    pub fn stp_key(&self) -> &str {
        if self.stp.group.is_empty() {
            &self.trader_id
        } else {
            &self.stp.group
        }
    }
}

/// Self-trade prevention instruction carried by an order. The aggressing
/// (newest) order's mode governs a match against a resting order with the
/// same [`Order::stp_key`].
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Stp {
    pub mode: StpMode,
    pub group: String,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum StpMode {
    #[default]
    None,
    CancelNewest,
    CancelOldest,
    CancelBoth,
    DecrementAndCancel,
}

impl StpMode {
    /// Parse an `FXP_STP_DEFAULT` value; empty means disabled.
    pub fn parse(value: &str) -> Result<Self, String> {
        match value.trim() {
            "" | "none" => Ok(Self::None),
            "cancel-newest" => Ok(Self::CancelNewest),
            "cancel-oldest" => Ok(Self::CancelOldest),
            "cancel-both" => Ok(Self::CancelBoth),
            "decrement-and-cancel" => Ok(Self::DecrementAndCancel),
            other => Err(format!(
                "invalid STP mode {other:?} (none|cancel-newest|cancel-oldest|cancel-both|decrement-and-cancel)"
            )),
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Self::None => "none",
            Self::CancelNewest => "cancel-newest",
            Self::CancelOldest => "cancel-oldest",
            Self::CancelBoth => "cancel-both",
            Self::DecrementAndCancel => "decrement-and-cancel",
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum OrderSide {
    Buy,
    Sell,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum OrderType {
    Market,
    Limit,
    Stop,      // triggers market order when stop_price is reached
    StopLimit, // triggers limit order when stop_price is reached
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TimeInForce {
    Day,     // expires at end of regular trading session
    GTC,     // Good Till Cancelled
    IOC,     // Immediate or Cancel
    FOK,     // Fill or Kill
    AtOpen,  // Opening auction participation only
    AtClose, // Closing auction participation only (MOC)
    Gtd,     // Good Till Date (see Order.expire_date)
}
