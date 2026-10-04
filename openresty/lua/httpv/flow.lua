local config = require "httpv.config"
local details = require "httpv.details"
local metrics = require "httpv.metrics"
local publisher = require "httpv.publisher"
local routing = require "httpv.routing"

local _M = {}

local function now_ms()
  return math.floor(ngx.now() * 1000)
end

local function request_id()
  return table.concat({
    ngx.var.connection or "0",
    ngx.var.connection_requests or "0",
    tostring(now_ms()),
    tostring(math.random(100000, 999999))
  }, "-")
end

local function matches(subject, method, host, path)
  if not subject.enabled then
    return false
  end
  local match = subject.match or {}
  if match.host and match.host ~= "" and match.host ~= host then
    return false
  end
  if match.method and match.method ~= "" and match.method ~= method then
    return false
  end
  if match.path_prefix and match.path_prefix ~= "" and string.sub(path, 1, #match.path_prefix) ~= match.path_prefix then
    return false
  end
  return true
end

local function match_subject(gateway, method, host, path)
  for _, subject in ipairs(gateway.subjects or {}) do
    if matches(subject, method, host, path) then
      return subject
    end
  end
  return nil
end

local function match_route(gateway, method, host, path)
  for _, route in ipairs(gateway.routes or {}) do
    if matches(route, method, host, path) then
      return route
    end
  end
  return nil
end

local function matches_rule(rule, subject_id, method, path, client_ip)
  if rule.action ~= "block" then
    return false
  end
  if rule.subject_id and rule.subject_id ~= "" and rule.subject_id ~= subject_id then
    return false
  end
  if rule.method and rule.method ~= "" and rule.method ~= method then
    return false
  end
  if rule.path_prefix and rule.path_prefix ~= "" and string.sub(path, 1, #rule.path_prefix) ~= rule.path_prefix then
    return false
  end
  if rule.client_ip and rule.client_ip ~= "" and rule.client_ip ~= client_ip then
    return false
  end
  return true
end

local function emit(ctx, phase, extra)
  if not ctx.display then
    return
  end
  local event = {
    phase = phase,
    request_id = ctx.request_id,
    subject_id = ctx.subject_id,
    method = ctx.method,
    path = ctx.path,
    client_ip = ctx.client_ip,
    request_bytes = ctx.request_bytes,
    timestamp_ms = now_ms()
  }
  for key, value in pairs(extra or {}) do
    event[key] = value
  end
  publisher.emit(event)
end

local function reconcile_pending_count(dict, count_key, subject_id)
  local count = 0
  local prefix = "pending:" .. subject_id .. ":"
  for _, key in ipairs(dict:get_keys(0)) do
    if string.sub(key, 1, #prefix) == prefix then
      count = count + 1
    end
  end
  dict:set(count_key, count)
  return count
end

local function reserve_pending_slot(dict, subject_id, maximum)
  local count_key = "pending_count:" .. subject_id
  local current = dict:incr(count_key, 1, 0)
  if not current then
    return false, "storage", count_key
  end
  if maximum <= 0 or current <= maximum then
    return true, nil, count_key
  end
  dict:incr(count_key, -1, 0)
  local actual = reconcile_pending_count(dict, count_key, subject_id)
  if actual < maximum then
    current = dict:incr(count_key, 1, 0)
    if current and current <= maximum then
      return true, nil, count_key
    end
    if current then
      dict:incr(count_key, -1, 0)
    end
  end
  return false, "overflow", count_key
end

local function release_pending_slot(dict, count_key)
  local current = dict:incr(count_key, -1, 0) or 0
  if current < 0 then
    dict:set(count_key, 0)
  end
end

local function overflow_decision(ctx, gate, reason)
  local action
  if reason == "storage" then
    metrics.increment("gate_storage_failure_total")
    action = gate.failure_action == "block" and "block" or "allow"
  else
    metrics.increment("gate_overflow_total")
    action = gate.overflow_action
    if action ~= "allow" and action ~= "reject" then
      action = "block"
    end
    metrics.increment("gate_overflow_" .. action .. "_total")
  end
  local status = 0
  if action == "block" then
    status = ngx.HTTP_FORBIDDEN
  elseif action == "reject" then
    status = ngx.HTTP_SERVICE_UNAVAILABLE
  end
  emit(ctx, "request_overflow", { action = action, status = status })
  return action
end

local function resolve_pending(ctx, subject)
  local gate = subject.gate or {}
  if not gate.enabled then
    return "allow"
  end

  local dict = ngx.shared.httpv_pending
  local pending_key = "pending:" .. subject.id .. ":" .. ctx.request_id
  local pending_index_key = "pending_index:" .. ctx.request_id
  local decision_key = "decision:" .. ctx.request_id
  local gate_epoch = dict:get("gate_epoch") or 0
  local timeout_ms = tonumber(gate.timeout_ms) or 0
  local maximum = tonumber(gate.max_pending) or 0
  local reserved, reserve_error, count_key = reserve_pending_slot(dict, subject.id, maximum)
  if not reserved then
    return overflow_decision(ctx, gate, reserve_error)
  end
  local pending_ttl = timeout_ms > 0 and math.max(1, math.ceil(timeout_ms / 1000) + 5) or 0
  local stored = dict:set(pending_key, "1", pending_ttl)
  if not stored then
    release_pending_slot(dict, count_key)
    return overflow_decision(ctx, gate, "storage")
  end
  local indexed = dict:set(pending_index_key, pending_key, pending_ttl)
  if not indexed then
    dict:delete(pending_key)
    release_pending_slot(dict, count_key)
    return overflow_decision(ctx, gate, "storage")
  end

  local active = true
  local function cleanup_pending()
    if not active then
      return false
    end
    active = false
    dict:delete(pending_key)
    dict:delete(pending_index_key)
    dict:delete(decision_key)
    release_pending_slot(dict, count_key)
    metrics.pending_exit()
    return true
  end

  metrics.pending_enter()
  emit(ctx, "request_pending")

  local started = ngx.now()
  ngx.on_abort(function()
    if cleanup_pending() then
      metrics.increment("gate_client_abort_total")
      details.abort(ctx)
      emit(ctx, "request_aborted", {
        action = "abort",
        status = 499,
        duration_ms = math.max(0, now_ms() - ctx.started_at_ms)
      })
    end
  end)

  while true do
    if (dict:get("gate_epoch") or 0) ~= gate_epoch then
      cleanup_pending()
      emit(ctx, "request_decided", { action = "block" })
      return "block"
    end
    local decision = dict:get(decision_key)
    if decision == "allow" or decision == "block" then
      cleanup_pending()
      emit(ctx, "request_decided", { action = decision })
      return decision
    end
    if timeout_ms > 0 and (ngx.now() - started) * 1000 >= timeout_ms then
      cleanup_pending()
      local action = gate.timeout_action == "allow" and "allow" or "block"
      metrics.increment("gate_timeout_" .. action .. "_total")
      emit(ctx, "request_timeout", { action = action })
      return action
    end
    ngx.sleep(0.025)
  end
end

function _M.access()
  if ngx.var.httpv_native_fast_path == "1" then
    return
  end
  local method = ngx.req.get_method()
  local path = ngx.var.uri
  local host = ngx.var.host or ""
  local client_ip = ngx.var.remote_addr or ""
  local gateway = config.get()
  local route = match_route(gateway, method, host, path)
  if route then
    local target, route_ctx = routing.select(gateway, route)
    if target then
      ngx.var.httpv_upstream_url = target.url
      ngx.var.httpv_upstream_host = target.host_header or ""
      ngx.ctx.httpv_route = route_ctx
    end
  end
  local subject = match_subject(gateway, method, host, path)
  if not subject then
    return
  end

  local id = request_id()
  ngx.var.httpv_request_id = id
  ngx.var.httpv_subject_id = subject.id
  ngx.var.httpv_native_response_telemetry = subject.display ~= false and "1" or "0"
  local ctx = {
    request_id = id,
    subject_id = subject.id,
    display = subject.display ~= false,
    method = method,
    path = path,
    client_ip = client_ip,
    request_bytes = tonumber(ngx.var.content_length) or 0,
    started_at_ms = now_ms()
  }
  ngx.ctx.httpv = ctx
  details.begin(ctx, subject)
  metrics.increment("managed_requests_total")
  emit(ctx, "request_started")

  for _, rule in ipairs(gateway.rules or {}) do
    if matches_rule(rule, subject.id, method, path, client_ip) then
      metrics.increment("blocked_requests_total")
      emit(ctx, "request_blocked", { action = "block", status = ngx.HTTP_FORBIDDEN })
      ngx.exit(ngx.HTTP_FORBIDDEN)
    end
  end

  local gate_action = resolve_pending(ctx, subject)
  if gate_action == "block" or gate_action == "reject" then
    local status = gate_action == "reject" and ngx.HTTP_SERVICE_UNAVAILABLE or ngx.HTTP_FORBIDDEN
    metrics.increment("blocked_requests_total")
    emit(ctx, "request_blocked", { action = gate_action, status = status })
    ngx.exit(status)
  end
end

function _M.response_started()
  if ngx.var.httpv_native_fast_path == "1" then
    return
  end
  local ctx = ngx.ctx.httpv
  if not ctx then
    return
  end
  details.response_started(ctx)
  emit(ctx, "response_started", { status = ngx.status })
end

function _M.response_body()
  if ngx.var.httpv_native_fast_path == "1" then
    return
  end
  details.response_body(ngx.ctx.httpv, ngx.arg[1])
end

function _M.response_finished()
  routing.finish(ngx.ctx.httpv_route, ngx.var.upstream_status)
  if ngx.var.httpv_native_fast_path == "1" then
    return
  end
  local ctx = ngx.ctx.httpv
  if not ctx then
    return
  end
  local status = tonumber(ngx.var.status) or ngx.status
  local duration_ms = math.max(0, now_ms() - ctx.started_at_ms)
  details.finish(ctx, status, duration_ms)
  metrics.increment("responses_total")
  emit(ctx, "response_finished", {
    status = status,
    response_bytes = tonumber(ngx.var.body_bytes_sent) or 0,
    duration_ms = duration_ms
  })
end

return _M
