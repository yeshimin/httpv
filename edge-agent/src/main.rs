use bytes::Bytes;
use socket2::SockRef;
use std::{
    env, fs, io,
    os::unix::{fs::PermissionsExt, net::UnixDatagram as StdUnixDatagram},
    path::Path,
    sync::{
        atomic::{AtomicU64, AtomicUsize, Ordering},
        Arc,
    },
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{lookup_host, TcpListener, UdpSocket, UnixDatagram, UnixListener, UnixStream},
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
    native_socket_buffer_bytes: AtomicU64,
}

fn setting(name: &str, fallback: &str) -> String {
    env::var(name).unwrap_or_else(|_| fallback.to_string())
}

fn valid_phase(code: u8) -> bool {
    match code {
        1..=7 => true,
        _ => false,
    }
}

fn valid_frame(frame: &[u8]) -> bool {
    frame.len() >= HEADER_LENGTH && &frame[0..4] == b"HTVP" && frame[4] == 1 && valid_phase(frame[5])
}

fn enqueue(
    frame: Bytes,
    senders: &Arc<Vec<mpsc::Sender<Bytes>>>,
    cursor: &AtomicUsize,
    stats: &Stats,
) {
    let index = cursor.fetch_add(1, Ordering::Relaxed) % senders.len();
    if senders[index].try_send(frame).is_err() {
        stats.dropped.fetch_add(1, Ordering::Relaxed);
    }
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
                "httpv_edge_events_received {}\nhttpv_edge_events_forwarded {}\nhttpv_edge_events_dropped {}\nhttpv_edge_invalid_frames {}\nhttpv_edge_forward_errors {}\nhttpv_edge_native_socket_buffer_bytes {}\n",
                stats.received.load(Ordering::Relaxed),
                stats.forwarded.load(Ordering::Relaxed),
                stats.dropped.load(Ordering::Relaxed),
                stats.invalid_frames.load(Ordering::Relaxed),
                stats.forward_errors.load(Ordering::Relaxed),
                stats.native_socket_buffer_bytes.load(Ordering::Relaxed),
            );
            let response = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: text/plain; version=0.0.4\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(), body
            );
            let _ = stream.write_all(response.as_bytes()).await;
        });
    }
}

async fn receive_stream(
    mut stream: UnixStream,
    senders: Arc<Vec<mpsc::Sender<Bytes>>>,
    cursor: Arc<AtomicUsize>,
    stats: Arc<Stats>,
) {
    loop {
        let mut length = [0_u8; 4];
        if stream.read_exact(&mut length).await.is_err() {
            return;
        }
        let frame_length = u32::from_be_bytes(length) as usize;
        if frame_length == 0 || frame_length > 65_507 {
            stats.invalid_frames.fetch_add(1, Ordering::Relaxed);
            return;
        }
        let mut frame = vec![0_u8; frame_length];
        if stream.read_exact(&mut frame).await.is_err() {
            return;
        }
        stats.received.fetch_add(1, Ordering::Relaxed);
        if !valid_frame(&frame) {
            stats.invalid_frames.fetch_add(1, Ordering::Relaxed);
            continue;
        }
        enqueue(Bytes::from(frame), &senders, &cursor, &stats);
    }
}

async fn receive_datagrams(
    input: Arc<UnixDatagram>,
    senders: Arc<Vec<mpsc::Sender<Bytes>>>,
    cursor: Arc<AtomicUsize>,
    stats: Arc<Stats>,
) {
    let mut frame = vec![0_u8; 65_507];
    loop {
        let size = match input.recv(&mut frame).await {
            Ok(size) => size,
            Err(_) => return,
        };
        stats.received.fetch_add(1, Ordering::Relaxed);
        if !valid_frame(&frame[..size]) {
            stats.invalid_frames.fetch_add(1, Ordering::Relaxed);
            continue;
        }
        enqueue(Bytes::copy_from_slice(&frame[..size]), &senders, &cursor, &stats);
    }
}

fn bind_native_datagram(path: &str, receive_buffer_bytes: usize) -> io::Result<(UnixDatagram, usize)> {
    let input = StdUnixDatagram::bind(path)?;
    let socket = SockRef::from(&input);
    socket.set_recv_buffer_size(receive_buffer_bytes)?;
    let effective_buffer_bytes = socket.recv_buffer_size()?;
    input.set_nonblocking(true)?;
    Ok((UnixDatagram::from_std(input)?, effective_buffer_bytes))
}

#[tokio::main]
async fn main() -> std::io::Result<()> {
    let socket_path = setting("HTTPV_AGENT_SOCKET_PATH", "/run/httpv/edge-agent.sock");
    let native_socket_path = setting("HTTPV_AGENT_NATIVE_SOCKET_PATH", "/run/httpv/native.sock");
    let native_socket_buffer_bytes = setting("HTTPV_AGENT_NATIVE_SOCKET_BUFFER_BYTES", "8388608")
        .parse()
        .unwrap_or(8_388_608);
    let control_address = setting("HTTPV_AGENT_CONTROL_ADDR", "control:9100");
    let metrics_address = setting("HTTPV_AGENT_METRICS_ADDR", "0.0.0.0:9102");
    let queue_capacity = setting("HTTPV_AGENT_QUEUE_CAPACITY", "65536").parse().unwrap_or(65_536);
    let forward_workers = setting("HTTPV_AGENT_FORWARD_WORKERS", "4")
        .parse::<usize>()
        .unwrap_or(4)
        .clamp(1, 32);

    if let Some(parent) = Path::new(&socket_path).parent() {
        fs::create_dir_all(parent)?;
    }
    if Path::new(&socket_path).exists() {
        fs::remove_file(&socket_path)?;
    }
    let input = UnixListener::bind(&socket_path)?;
    fs::set_permissions(&socket_path, fs::Permissions::from_mode(0o666))?;
    if Path::new(&native_socket_path).exists() {
        fs::remove_file(&native_socket_path)?;
    }
    let (native_input, effective_native_socket_buffer_bytes) = bind_native_datagram(
        &native_socket_path,
        native_socket_buffer_bytes,
    )?;
    fs::set_permissions(&native_socket_path, fs::Permissions::from_mode(0o666))?;
    let output = Arc::new(UdpSocket::bind("0.0.0.0:0").await?);
    let control_address = lookup_host(&control_address)
        .await?
        .next()
        .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::AddrNotAvailable, "control address did not resolve"))?;
    let stats = Arc::new(Stats::default());
    stats
        .native_socket_buffer_bytes
        .store(effective_native_socket_buffer_bytes as u64, Ordering::Relaxed);
    let queue_per_worker = (queue_capacity / forward_workers).max(1);
    let mut sender_list = Vec::with_capacity(forward_workers);
    for _ in 0..forward_workers {
        let (sender, mut receiver) = mpsc::channel::<Bytes>(queue_per_worker);
        let forward_stats = Arc::clone(&stats);
        let output = Arc::clone(&output);
        sender_list.push(sender);
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
    }
    let senders = Arc::new(sender_list);
    let forward_cursor = Arc::new(AtomicUsize::new(0));

    tokio::spawn(metrics_server(Arc::clone(&stats), metrics_address));
    tokio::spawn(receive_datagrams(
        Arc::new(native_input),
        Arc::clone(&senders),
        Arc::clone(&forward_cursor),
        Arc::clone(&stats),
    ));

    loop {
        let (stream, _) = input.accept().await?;
        tokio::spawn(receive_stream(
            stream,
            Arc::clone(&senders),
            Arc::clone(&forward_cursor),
            Arc::clone(&stats),
        ));
    }
}
