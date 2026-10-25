// SPDX-License-Identifier: MIT
//! Optional gateway-to-core connection authentication. No end-user identity mapping.
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;

pub(crate) const CORE_LISTEN_ADDR: &str = "127.0.0.1:8080";
pub(crate) const MAGIC: &[u8; 8] = b"FXPAUTH1";
pub(crate) const MAX_TOKEN: usize = 1024;
#[derive(Clone)]
pub(crate) struct CoreAuth {
    token: Option<Arc<[u8]>>,
}
impl CoreAuth {
    pub(crate) fn new(token: Option<String>) -> Result<Self, &'static str> {
        let token = token.filter(|s| !s.is_empty());
        if token.as_ref().is_some_and(|s| s.len() > MAX_TOKEN) {
            return Err("FXP_CORE_AUTH_TOKEN exceeds 1024 bytes");
        };
        Ok(Self {
            token: token.map(|s| Arc::from(s.into_bytes())),
        })
    }
    pub(crate) fn from_env() -> Result<Self, &'static str> {
        match std::env::var("FXP_CORE_AUTH_TOKEN") {
            Ok(token) => Self::new(Some(token)),
            Err(std::env::VarError::NotPresent) => Self::new(None),
            Err(_) => Err("FXP_CORE_AUTH_TOKEN must be valid UTF-8"),
        }
    }
    pub(crate) fn enabled(&self) -> bool {
        self.token.is_some()
    }
    pub(crate) async fn authenticate(&self, stream: &mut TcpStream) -> std::io::Result<()> {
        let Some(expected) = &self.token else {
            return Ok(());
        };
        let denied = || {
            std::io::Error::new(
                std::io::ErrorKind::PermissionDenied,
                "core authentication failed",
            )
        };
        let mut magic = [0u8; 8];
        stream.read_exact(&mut magic).await.map_err(|_| denied())?;
        if &magic != MAGIC {
            return Err(denied());
        };
        let size = stream.read_u16().await.map_err(|_| denied())? as usize;
        if size == 0 || size > MAX_TOKEN {
            return Err(denied());
        };
        let mut token = vec![0u8; size];
        stream.read_exact(&mut token).await.map_err(|_| denied())?;
        if !constant_time_eq::constant_time_eq(expected, &token) {
            return Err(denied());
        };
        stream.write_all(&[1]).await.map_err(|_| denied())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use tokio::net::TcpListener;
    #[tokio::test]
    async fn required_token_rejects_wrong_and_accepts_valid_without_echo() {
        for provided in ["wrong-private-value", "correct-private-value"] {
            let auth = CoreAuth::new(Some("correct-private-value".into())).unwrap();
            let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
            let mut client = TcpStream::connect(listener.local_addr().unwrap())
                .await
                .unwrap();
            let (mut server, _) = listener.accept().await.unwrap();
            let task = tokio::spawn(async move {
                let result = auth.authenticate(&mut server).await;
                assert!(!format!("{result:?}").contains("private-value"));
                result.is_ok()
            });
            client.write_all(MAGIC).await.unwrap();
            client.write_u16(provided.len() as u16).await.unwrap();
            client.write_all(provided.as_bytes()).await.unwrap();
            let mut ack = [0u8; 1];
            let accepted = client.read_exact(&mut ack).await.is_ok();
            assert_eq!(accepted, provided == "correct-private-value");
            if accepted {
                assert_eq!(ack, [1])
            };
            assert_eq!(task.await.unwrap(), accepted);
        }
    }
    #[tokio::test]
    async fn ordinary_fxp_header_is_rejected_before_sender_can_be_read() {
        let auth = CoreAuth::new(Some("not-logged".into())).unwrap();
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let mut client = TcpStream::connect(listener.local_addr().unwrap())
            .await
            .unwrap();
        let (mut server, _) = listener.accept().await.unwrap();
        client
            .write_all(b"\x00\x00\x00\x20fake-sender")
            .await
            .unwrap();
        assert_eq!(
            auth.authenticate(&mut server).await.unwrap_err().kind(),
            std::io::ErrorKind::PermissionDenied
        );
    }
    #[test]
    fn default_endpoint_is_always_loopback_and_token_validation_never_echoes() {
        assert!(
            CORE_LISTEN_ADDR
                .parse::<std::net::SocketAddr>()
                .unwrap()
                .ip()
                .is_loopback()
        );
        let error = CoreAuth::new(Some("s".repeat(MAX_TOKEN + 1)))
            .err()
            .unwrap();
        assert_eq!(error, "FXP_CORE_AUTH_TOKEN exceeds 1024 bytes")
    }
}
