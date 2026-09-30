local cjson = require "cjson.safe"

local _M = {}

local queue = {}
local queue_head = 1
local queue_tail = 0
local draining = false

local function send(event)
  local payload = cjson.encode(event)
  if not payload then
    return
  end

  local sock = ngx.socket.udp()
  sock:settimeout(20)
  local ok = sock:setpeername("edge-agent", 9101)
  if not ok then
    return
  end
  sock:send(payload)
  sock:close()
end

local function drain(premature)
  if premature then
    return
  end
  while queue_head <= queue_tail do
    local event = queue[queue_head]
    queue[queue_head] = nil
    queue_head = queue_head + 1
    send(event)
  end
  queue = {}
  queue_head = 1
  queue_tail = 0
  draining = false
end

function _M.emit(event)
  event.timestamp_ms = event.timestamp_ms or math.floor(ngx.now() * 1000)
  queue_tail = queue_tail + 1
  queue[queue_tail] = event
  if not draining then
    draining = true
    -- A single timer moves network I/O off the request path while preserving
    -- the order in which this worker observed each request state.
    local ok = ngx.timer.at(0, drain)
    if not ok then
      draining = false
    end
  end
end

return _M
