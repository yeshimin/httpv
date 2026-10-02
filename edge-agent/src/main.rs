use bytes::Bytes;
use serde_json::json;
use std::{
    env, fs,
    os::unix::fs::PermissionsExt,
    path::Path,
    sync::{
        atomic::{AtomicU64, Ordering},
        Arc,
    },
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{lookup_host, TcpListener, UdpSocket, UnixListener, UnixStream},
    sync::mpsc,
};

const HEADER_LENGTH: usize = 38;

#[derive(Default)]
struct Stats {
    received: AtomicU64,
    forwarded: AtomicU64,
    dropped: AtomicU64,
    invalid_frames: AtomicU64,
    forward_errors: AtomicU64,
}

fn setting(name: &str, fallback: &str) -> String {
    env::var(name).unwrap_or_else(|_| fallback.to_string())
}

fn u16(input: &[u8]) -> u16 {
    u16::from_be_bytes([input[0], input[1]])
}

fn u32(input: &[u8]) -> u32 {
    u32::from_be_bytes([input[0], input[1], input[2], input[3]])
}

fn u64(input: &[u8]) -> u64 {
    u64::from_be_bytes([input[0], input[1], input[2], input[3], input[4], input[5], input[6], input[7]])
}

fn take_text(frame: &[u8], offset: &mut usize) -> Option<String> {
    if *offset + 2 > frame.len() {
        return None;
    }
    let length = usize::from(u16(&frame[*offset..*offset + 2]));
    *offset += 2;
    if *offset + length > frame.len() {
        return None;
    }
    let value = std::str::from_utf8(&frame[*offset..*offset + length]).ok()?.to_owned();
    *offset += length;
    Some(value)
}

fn phase_name(code: u8) -> Option<&'static str> {
    match code {
        1 => Some("request_started"),
        2 => Some("request_pending"),
        3 => Some("request_decided"),
        4 => Some("request_timeout"),
        5 => Some("request_blocked"),
        6 => Some("response_started"),
        7 => Some("response_finished"),
        _ => None,
    }
}

fn decode_frame(frame: &[u8]) -> Option<Vec<u8>> {
    if frame.len() < HEADER_LENGTH || &frame[0..4] != b"HTVP" || frame[4] != 1 {
        return None;
    }
    let phase = phase_name(frame[5])?;
    let timestamp_ms = u64(&frame[8..16]);
    let request_bytes = u64(&frame[16..24]);
    let response_bytes = u64(&frame[24..32]);
    let status = u16(&frame[32..34]);
    let duration_ms = u32(&frame[34..38]);
    let mut offset = HEADER_LENGTH;
    let request_id = take_text(frame, &mut offset)?;
    let subject_id = take_text(frame, &mut offset)?;
    let method = take_text(frame, &mut offset)?;
    let path = take_text(frame, &mut offset)?;
    let client_ip = take_text(frame, &mut offset)?;
    let action = take_text(frame, &mut offset)?;

    Some(
        serde_json::to_vec(&json!({
            "phase": phase,
            "request_id": request_id,
            "subject_id": subject_id,
            "timestamp_ms": timestamp_ms,
            "method": method,
            "path": path,
            "client_ip": client_ip,
            "request_bytes": request_bytes,
            "response_bytes": response_bytes,
            "status": status,
            "duration_ms": duration_ms,
            "action": action
        }))
        .ok()?,
    )
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
                "httpv_edge_events_received {}\nhttpv_edge_events_forwarded {}\nhttpv_edge_events_dropped {}\nhttpv_edge_invalid_frames {}\nhttpv_edge_forward_errors {}\n",
                stats.received.load(Ordering::Relaxed),
                stats.forwarded.load(Ordering::Relaxed),
                stats.dropped.load(Ordering::Relaxed),
                stats.invalid_frames.load(Ordering::Relaxed),
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

async fn receive_stream(mut stream: UnixStream, sender: mpsc::Sender<Bytes>, stats: Arc<Stats>) {
    loop {
        let mut length = [0_u8; 4];
        if stream.read_exact(&mut length).await.is_err() {
            return;
        }
        let frame_length = u32(&length) as usize;
        if frame_length == 0 || frame_length > 65_507 {
            stats.invalid_frames.fetch_add(1, Ordering::Relaxed);
            return;
        }
        let mut frame = vec![0_u8; frame_length];
        if stream.read_exact(&mut frame).await.is_err() {
            return;
        }
        stats.received.fetch_add(1, Ordering::Relaxed);
        let Some(event) = decode_frame(&frame) else {
            stats.invalid_frames.fetch_add(1, Ordering::Relaxed);
            continue;
        };
        if sender.try_send(Bytes::from(event)).is_err() {
            stats.dropped.fetch_add(1, Ordering::Relaxed);
        }
    }
}

#[tokio::main]
async fn main() -> std::io::Result<()> {
    let socket_path = setting("HTTPV_AGENT_SOCKET_PATH", "/run/httpv/edge-agent.sock");
    let control_address = setting("HTTPV_AGENT_CONTROL_ADDR", "control:9100");
    let metrics_address = setting("HTTPV_AGENT_METRICS_ADDR", "0.0.0.0:9102");
    let queue_capacity = setting("HTTPV_AGENT_QUEUE_CAPACITY", "65536").parse().unwrap_or(65_536);

    if let Some(parent) = Path::new(&socket_path).parent() {
        fs::create_dir_all(parent)?;
    }
    if Path::new(&socket_path).exists() {
        fs::remove_file(&socket_path)?;
    }
    let input = UnixListener::bind(&socket_path)?;
    fs::set_permissions(&socket_path, fs::Permissions::from_mode(0o666))?;
    let output = UdpSocket::bind("0.0.0.0:0").await?;
    let control_address = lookup_host(&control_address)
        .await?
        .next()
        .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::AddrNotAvailable, "control address did not resolve"))?;
    let stats = Arc::new(Stats::default());
    let (sender, mut receiver) = mpsc::channel::<Bytes>(queue_capacity);

    tokio::spawn(metrics_server(Arc::clone(&stats), metrics_address));

    let forward_stats = Arc::clone(&stats);
    tokio::spawn(async move {
        while let Some(event) = receiver.recv().await {
            match output.send_to(&event, control_address).await {
                Ok(_) => {
                    forward_stats.forwarded.fetch_add(1, Ordering::Relaxed);
                }
                Err(_) => {
                    forward_stats.forward_errors.fetch_add(1, Ordering::Relaxed);
                }
            }
        }
    });

    loop {
        let (stream, _) = input.accept().await?;
        tokio::spawn(receive_stream(stream, sender.clone(), Arc::clone(&stats)));
    }
}
