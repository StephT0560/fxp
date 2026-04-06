use std::collections::{HashMap, HashSet};
use std::sync::{Arc, Mutex};
use crate::order_book::market_data::{MarketDataSnapshot, MarketDataIncrementalUpdate};

#[derive(Debug)]
pub struct MarketDataBroadcaster {
    subscribers: Arc<Mutex<HashMap<String, HashSet<u32>>>>, // Symbol → Client IDs
}

impl MarketDataBroadcaster {
    pub fn new() -> Self {
        Self {
            subscribers: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    /// ✅ Client subscribes to a symbol
    pub fn subscribe(&self, client_id: u32, symbol: &str) {
        let mut subs = self.subscribers.lock().unwrap();
        subs.entry(symbol.to_string()).or_insert_with(HashSet::new).insert(client_id);
    }

    /// ✅ Client unsubscribes from a symbol
    pub fn unsubscribe(&self, client_id: u32, symbol: &str) {
        let mut subs = self.subscribers.lock().unwrap();
        if let Some(clients) = subs.get_mut(symbol) {
            clients.remove(&client_id);
            if clients.is_empty() {
                subs.remove(symbol);
            }
        }
    }

    /// ✅ Get list of clients subscribed to a symbol
    pub fn get_subscribers(&self, symbol: &str) -> Vec<u32> {
        let subs = self.subscribers.lock().unwrap();
        subs.get(symbol).cloned().unwrap_or_default().into_iter().collect()
    }

    /// ✅ Broadcast a `MarketDataSnapshot` to subscribers
    pub fn broadcast_snapshot(&self, snapshot: &MarketDataSnapshot) {
        let clients = self.get_subscribers(&snapshot.symbol);
        for client_id in clients {
            println!("📡 Sending snapshot to client {}: {:?}", client_id, snapshot);
        }
    }

    /// ✅ Broadcast an `IncrementalUpdate` to subscribers
    pub fn broadcast_incremental(&self, update: &MarketDataIncrementalUpdate) {
        let clients = self.get_subscribers(&update.symbol);
        for client_id in clients {
            println!("📡 Sending incremental update to client {}: {:?}", client_id, update);
        }
    }
}
