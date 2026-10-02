local _M = {}

local metric_names = {
  "managed_requests_total",
  "blocked_requests_total",
  "responses_total",
  "gate_pending_current",
  "gate_pending_peak"
}

function _M.increment(name, amount)
  ngx.shared.httpv_metrics:incr(name, amount or 1, 0)
end

function _M.pending_enter()
  local dict = ngx.shared.httpv_metrics
  local current = dict:incr("gate_pending_current", 1, 0) or 0
  local peak = dict:get("gate_pending_peak") or 0
  if current > peak then
    dict:set("gate_pending_peak", current)
  end
end

function _M.pending_exit()
  local dict = ngx.shared.httpv_metrics
  local current = dict:incr("gate_pending_current", -1, 0) or 0
  if current < 0 then
    dict:set("gate_pending_current", 0)
  end
end

function _M.render()
  ngx.header.content_type = "text/plain; version=0.0.4"
  local dict = ngx.shared.httpv_metrics
  for _, name in ipairs(metric_names) do
    ngx.say("httpv_openresty_", name, " ", dict:get(name) or 0)
  end
end

return _M
