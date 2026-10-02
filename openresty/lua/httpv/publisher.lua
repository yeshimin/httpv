local metrics = require "httpv.metrics"

local _M = {}

local phase_codes = {
  request_started = 1,
  request_pending = 2,
  request_decided = 3,
  request_timeout = 4,
  request_blocked = 5,
  response_started = 6,
  response_finished = 7
}

local queue = {}
local queue_head = 1
local queue_tail = 0
local started = false

local function u16(value)
  value = math.max(0, math.min(65535, tonumber(value) or 0))
  return string.char(math.floor(value / 256), value % 256)
end

local function u32(value)
  value = math.max(0, tonumber(value) or 0)
  return string.char(
    math.floor(value / 16777216) % 256,
    math.floor(value / 65536) % 256,
    math.floor(value / 256) % 256,
    value % 256
  )
end

local function u64(value)
  value = math.max(0, tonumber(value) or 0)
  local high = math.floor(value / 4294967296)
  local low = value % 4294967296
  return u32(high) .. u32(low)
end

local function text(value)
  value = tostring(value or "")
  if #value > 65535 then
    value = string.sub(value, 1, 65535)
  end
  return u16(#value) .. value
end

local function encode(event)
  local phase = phase_codes[event.phase] or 0
  if phase == 0 then
    return nil
  end
  local frame = "HTVP"
    .. string.char(1, phase)
    .. u16(0)
    .. u64(event.timestamp_ms)
    .. u64(event.request_bytes)
    .. u64(event.response_bytes)
    .. u16(event.status)
    .. u32(event.duration_ms)
    .. text(event.request_id)
    .. text(event.subject_id)
    .. text(event.method)
    .. text(event.path)
    .. text(event.client_ip)
    .. text(event.action)
  return u32(#frame) .. frame
end

local function acquire_socket()
  local sock = ngx.socket.tcp()
  sock:settimeout(5)
  local ok = sock:connect("unix:/run/httpv/edge-agent.sock")
  if not ok then
    sock:close()
    return nil
  end
  return sock
end

local function send_batch(payload, count)
  local sock = acquire_socket()
  if not sock then
    metrics.increment("telemetry_events_dropped_total", count)
    return
  end
  local bytes = sock:send(payload)
  if not bytes or bytes ~= #payload then
    sock:close()
    metrics.increment("telemetry_events_dropped_total", count)
    return
  end
  sock:setkeepalive(60000, 16)
  metrics.increment("telemetry_events_sent_total", count)
end

local function drain(premature)
  if premature then
    return
  end
  if queue_head > queue_tail then
    return
  end
  local frames = {}
  local count = 0
  local size = 0
  while queue_head <= queue_tail and count < 256 and size < 60 * 1024 do
    local event = queue[queue_head]
    queue[queue_head] = nil
    queue_head = queue_head + 1
    local frame = encode(event)
    if frame then
      frames[#frames + 1] = frame
      count = count + 1
      size = size + #frame
    else
      metrics.increment("telemetry_events_dropped_total")
    end
  end
  if count > 0 then
    send_batch(table.concat(frames), count)
  end
  if queue_head > queue_tail then
    queue = {}
    queue_head = 1
    queue_tail = 0
  end
end

function _M.start()
  if started then
    return
  end
  started = true
  local ok = ngx.timer.every(0.005, drain)
  if not ok then
    started = false
  end
end

function _M.emit(event)
  event.timestamp_ms = event.timestamp_ms or math.floor(ngx.now() * 1000)
  queue_tail = queue_tail + 1
  queue[queue_tail] = event
  metrics.increment("telemetry_events_enqueued_total")
end

return _M
