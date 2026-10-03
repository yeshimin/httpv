use bytes::Bytes;
use memmap2::{MmapMut, MmapOptions};
use socket2::SockRef;
use std::{
    collections::HashMap,
    env, fs, io,
    os::unix::{fs::PermissionsExt, net::UnixDatagram as StdUnixDatagram},
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicU64, AtomicUsize, Ordering},
        Arc,
    },
    thread,
    time::{Duration, Instant},
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{lookup_host, TcpListener, UdpSocket, UnixDatagram, UnixListener, UnixStream},
    sync::mpsc,
};

const HEADER_LENGTH: usize = 38;
const RING_HEADER_LENGTH: usize = 64;
const RING_RECORD_SIZE: usize = 2048;
const RING_VERSION: u32 = 1;
const RING_WRITE_OFFSET: usize = 16;
const RING_READ_OFFSET: usize = 24;

#[derive(Default)]
struct Stats {
    received: AtomicU64,
    forwarded: AtomicU64,
    dropped: AtomicU64,
    invalid_frames: AtomicU64,
    forward_errors: AtomicU64,
    native_socket_buffer_bytes: AtomicU64,
    ring_events_received: AtomicU64,
}

fn setting(name: &str, fallback: &str) -> String {
    env::var(name).unwrap_or_else(|_| fallback.to_string())
}

fn valid_phase(code: u8) -> bool {
    match code {
        1..=8 => true,
        _ => false,
    }
}

fn valid_frame(frame: &[u8]) -> bool {
    frame.len() >= HEADER_LENGTH && &frame[0..4] == b"HTVP" && frame[4] == 1 && valid_phase(frame[5])
}

#[cfg(test)]
mod tests {
    use super::valid_phase;

    #[test]
    fn accepts_all_protocol_phases() {
        assert!(valid_phase(1));
        assert!(valid_phase(8));
        assert!(!valid_phase(0));
        assert!(!valid_phase(9));
    }
}

struct Ring {
    map: MmapMut,
    capacity: usize,
}

fn ring_atomic(map: &MmapMut, offset: usize) -> &AtomicU64 {
    // The C module owns this fixed layout. Header offsets are eight-byte aligned.
    unsafe { &*(map.as_ptr().add(offset).cast::<AtomicU64>()) }
}

fn ring_u32(map: &MmapMut, offset: usize) -> u32 {
    u32::from_ne_bytes(map[offset..offset + 4].try_into().unwrap())
}

impl Ring {
    fn open(path: &Path) -> io::Result<Self> {
        let file = fs::OpenOptions::new().read(true).write(true).open(path)?;
        let map = unsafe { MmapOptions::new().map_mut(&file)? };
        if map.len() < RING_HEADER_LENGTH || &map[..4] != b"HTVR" || ring_u32(&map, 4) != RING_VERSION {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "invalid HTTPV ring header"));
        }
        let record_size = ring_u32(&map, 8) as usize;
        let capacity = ring_u32(&map, 12) as usize;
        if record_size != RING_RECORD_SIZE || capacity == 0 || map.len() < RING_HEADER_LENGTH + record_size * capacity {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "invalid HTTPV ring geometry"));
        }
        Ok(Self { map, capacity })
    }

    fn drain(&mut self, senders: &Arc<Vec<mpsc::Sender<Bytes>>>, cursor: &AtomicUsize, stats: &Stats) {
        let write = ring_atomic(&self.map, RING_WRITE_OFFSET).load(Ordering::Acquire);
        let mut read = ring_atomic(&self.map, RING_READ_OFFSET).load(Ordering::Acquire);
        while read < write {
            let offset = RING_HEADER_LENGTH + ((read as usize % self.capacity) * RING_RECORD_SIZE);
            let frame_length = ring_u32(&self.map, offset) as usize;
            if frame_length == 0 || frame_length > RING_RECORD_SIZE - 4 {
                stats.invalid_frames.fetch_add(1, Ordering::Relaxed);
            } else {
                let frame = &self.map[offset + 4..offset + 4 + frame_length];
                if valid_frame(frame) {
                    stats.received.fetch_add(1, Ordering::Relaxed);
                    stats.ring_events_received.fetch_add(1, Ordering::Relaxed);
                    enqueue(Bytes::copy_from_slice(frame), senders, cursor, stats);
                } else {
                    stats.invalid_frames.fetch_add(1, Ordering::Relaxed);
                }
            }
            read += 1;
        }
        ring_atomic(&self.map, RING_READ_OFFSET).store(read, Ordering::Release);
    }
}

fn receive_rings(directory: String, senders: Arc<Vec<mpsc::Sender<Bytes>>>, cursor: Arc<AtomicUsize>, stats: Arc<Stats>) {
    let directory = PathBuf::from(directory);
    let mut rings: HashMap<PathBuf, Ring> = HashMap::new();
    let mut last_discovery = Instant::now() - Duration::from_secs(1);
    loop {
        if last_discovery.elapsed() >= Duration::from_secs(1) {
            if let Ok(entries) = fs::read_dir(&directory) {
                for entry in entries.flatten() {
                    let path = entry.path();
                    let is_ring = path.file_name().and_then(|name| name.to_str()).is_some_and(|name| name.starts_with("telemetry.ring."));
                    if is_ring && !rings.contains_key(&path) {
                        if let Ok(ring) = Ring::open(&path) {
                            rings.insert(path, ring);
                        }
                    }
                }
            }
            last_discovery = Instant::now();
        }
        for ring in rings.values_mut() {
            ring.drain(&senders, &cursor, &stats);
        }
        thread::sleep(Duration::from_millis(1));
    }
}

fn enqueue(
    frame: Bytes,
    senders: &Arc<Vec<mpsc::Sender<Bytes>>>,
    cursor: &AtomicUsize,
    stats: &Stats,
) {
    let index = frame_shard(&frame, senders.len())
        .unwrap_or_else(|| cursor.fetch_add(1, Ordering::Relaxed) % senders.len());
    if senders[index].try_send(frame).is_err() {
        stats.dropped.fetch_add(1, Ordering::Relaxed);
    }
}

fn frame_shard(frame: &[u8], shards: usize) -> Option<usize> {
    if shards == 0 || frame.len() < HEADER_LENGTH + 2 || !valid_frame(frame) {
        return None;
    }
    let request_id_length = usize::from(u16::from_be_bytes([frame[HEADER_LENGTH], frame[HEADER_LENGTH + 1]]));
    let start = HEADER_LENGTH + 2;
    let end = start.checked_add(request_id_length)?;
    if end > frame.len() {
        return None;
    }
    let hash = frame[start..end].iter().fold(2_166_136_261_u64, |hash, byte| {
        (hash ^ u64::from(*byte)).wrapping_mul(16_777_619)
    });
    Some(hash as usize % shards)
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
                "httpv_edge_events_received {}\nhttpv_edge_events_forwarded {}\nhttpv_edge_events_dropped {}\nhttpv_edge_invalid_frames {}\nhttpv_edge_forward_errors {}\nhttpv_edge_native_socket_buffer_bytes {}\nhttpv_edge_ring_events_received {}\n",
                stats.received.load(Ordering::Relaxed),
                stats.forwarded.load(Ordering::Relaxed),
                stats.dropped.load(Ordering::Relaxed),
                stats.invalid_frames.load(Ordering::Relaxed),
                stats.forward_errors.load(Ordering::Relaxed),
                stats.native_socket_buffer_bytes.load(Ordering::Relaxed),
                stats.ring_events_received.load(Ordering::Relaxed),
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
    let ring_directory = setting("HTTPV_AGENT_RING_DIRECTORY", "/run/httpv");
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
        // The OpenResty worker runs as an unprivileged user and creates one
        // per-worker ring file in this dedicated local runtime directory.
        fs::set_permissions(parent, fs::Permissions::from_mode(0o777))?;
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
    let ring_senders = Arc::clone(&senders);
    let ring_cursor = Arc::clone(&forward_cursor);
    let ring_stats = Arc::clone(&stats);
    thread::Builder::new()
        .name("httpv-ring-reader".to_string())
        .spawn(move || receive_rings(ring_directory, ring_senders, ring_cursor, ring_stats))?;
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
