local metrics = require "httpv.metrics"

local _M = {}

local function find_upstream(gateway, upstream_id)
  for _, upstream in ipairs(gateway.upstreams or {}) do
    if upstream.id == upstream_id then
      return upstream
    end
  end
  return nil
end

local function targets(upstream)
  if type(upstream.targets) == "table" and #upstream.targets > 0 then
    return upstream.targets
  end
  return {
    { id = upstream.id .. "-1", url = upstream.url, host_header = upstream.host_header, weight = 1 }
  }
end

local function is_healthy(state, upstream_id, target)
  local unhealthy_until = state:get("unhealthy:" .. upstream_id .. ":" .. target.id) or 0
  return unhealthy_until <= ngx.now()
end

local function choose_weighted(candidates, sequence)
  local total = 0
  for _, target in ipairs(candidates) do
    total = total + math.max(1, tonumber(target.weight) or 1)
  end
  if total == 0 then
    return nil
  end
  local slot = (sequence - 1) % total
  for _, target in ipairs(candidates) do
    slot = slot - math.max(1, tonumber(target.weight) or 1)
    if slot < 0 then
      return target
    end
  end
  return candidates[1]
end

function _M.select(gateway, route)
  local upstream = find_upstream(gateway, route.upstream_id)
  if not upstream then
    return nil
  end
  local state = ngx.shared.httpv_upstream_state
  local all_targets = targets(upstream)
  local healthy = {}
  for _, target in ipairs(all_targets) do
    if is_healthy(state, upstream.id, target) then
      healthy[#healthy + 1] = target
    else
      metrics.increment("upstream_failover_skips_total")
    end
  end
  if #healthy == 0 then
    healthy = all_targets
  end
  local sequence = state:incr("rr:" .. upstream.id, 1, 0) or 1
  local target = choose_weighted(healthy, sequence)
  if not target then
    return nil
  end
  return target, {
    upstream_id = upstream.id,
    target_id = target.id,
    max_failures = math.max(1, tonumber(upstream.max_failures) or 1),
    failure_timeout_ms = math.max(100, tonumber(upstream.failure_timeout_ms) or 10000)
  }
end

function _M.finish(route_ctx, upstream_status)
  if not route_ctx or not upstream_status or upstream_status == "" then
    return
  end
  local status = tonumber(string.match(upstream_status, "(%d%d%d)$"))
  if not status then
    return
  end
  local state = ngx.shared.httpv_upstream_state
  local key = route_ctx.upstream_id .. ":" .. route_ctx.target_id
  if status == 502 or status == 503 or status == 504 then
    metrics.increment("upstream_failures_total")
    local failures = state:incr("failures:" .. key, 1, 0) or 1
    if failures >= route_ctx.max_failures then
      state:set("failures:" .. key, 0)
      state:set("unhealthy:" .. key, ngx.now() + route_ctx.failure_timeout_ms / 1000)
    end
    return
  end
  state:set("failures:" .. key, 0)
  state:delete("unhealthy:" .. key)
end

return _M
