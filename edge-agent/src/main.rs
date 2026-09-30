use bytes::Bytes;
use std::{env, sync::{Arc, atomic::{AtomicU64, Ordering}}};
use tokio::{io::{AsyncReadExt, AsyncWriteExt}, net::{TcpListener, UdpSocket}, sync::mpsc};

#[derive(Default)]
struct Stats {
    received: AtomicU64,
    forwarded: AtomicU64,
    dropped: AtomicU64,
    forward_errors: AtomicU64,
}

fn setting(name: &str, fallback: &str) -> String {
    env::var(name).unwrap_or_else(|_| fallback.to_string())
}

async fn metrics_server(stats: Arc<Stats>, address: String) -> std::io::Result<()> {
    let listener = TcpListener::bind(address).await?;
    loop {
        let (mut stream, _) = listener.accept().await?;
        let stats = Arc::clone(&stats);
        tokio::spawn(async move {
            let mut request = [0_u8; 1024];
            let _ = stream.read(&mut request).await;
            let body = format!(
                "httpv_edge_events_received {}\nhttpv_edge_events_forwarded {}\nhttpv_edge_events_dropped {}\nhttpv_edge_forward_errors {}\n",
                stats.received.load(Ordering::Relaxed),
                stats.forwarded.load(Ordering::Relaxed),
                stats.dropped.load(Ordering::Relaxed),
                stats.forward_errors.load(Ordering::Relaxed),
            );
            let response = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: text/plain; version=0.0.4\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(), body
            );
            let _ = stream.write_all(response.as_bytes()).await;
        });
    }
}

#[tokio::main]
async fn main() -> std::io::Result<()> {
    let input_address = setting("HTTPV_AGENT_INPUT_ADDR", "0.0.0.0:9101");
    let control_address = setting("HTTPV_AGENT_CONTROL_ADDR", "control:9100");
    let metrics_address = setting("HTTPV_AGENT_METRICS_ADDR", "0.0.0.0:9102");
    let queue_capacity = setting("HTTPV_AGENT_QUEUE_CAPACITY", "65536").parse().unwrap_or(65_536);

    let input = UdpSocket::bind(&input_address).await?;
    let output = UdpSocket::bind("0.0.0.0:0").await?;
    let stats = Arc::new(Stats::default());
    let (sender, mut receiver) = mpsc::channel::<Bytes>(queue_capacity);

    tokio::spawn(metrics_server(Arc::clone(&stats), metrics_address));

    let forward_stats = Arc::clone(&stats);
    tokio::spawn(async move {
        while let Some(event) = receiver.recv().await {
            match output.send_to(&event, &control_address).await {
                Ok(_) => { forward_stats.forwarded.fetch_add(1, Ordering::Relaxed); }
                Err(_) => { forward_stats.forward_errors.fetch_add(1, Ordering::Relaxed); }
            }
        }
    });

    let mut buffer = vec![0_u8; 65_507];
    loop {
        let (length, _) = input.recv_from(&mut buffer).await?;
        stats.received.fetch_add(1, Ordering::Relaxed);
        if sender.try_send(Bytes::copy_from_slice(&buffer[..length])).is_err() {
            stats.dropped.fetch_add(1, Ordering::Relaxed);
        }
    }
}
