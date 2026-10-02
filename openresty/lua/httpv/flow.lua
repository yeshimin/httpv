local config = require "httpv.config"
local metrics = require "httpv.metrics"
local publisher = require "httpv.publisher"

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

local function matches_rule(rule, method, path, client_ip)
  if rule.action ~= "block" then
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

local function resolve_pending(ctx, subject)
  local gate = subject.gate or {}
  if not gate.enabled then
    return "allow"
  end

  local dict = ngx.shared.httpv_pending
  local pending_key = "pending:" .. ctx.request_id
  local decision_key = "decision:" .. ctx.request_id
  local gate_epoch = dict:get("gate_epoch") or 0
  dict:set(pending_key, "1")
  metrics.pending_enter()
  emit(ctx, "request_pending")

  local started = ngx.now()
  local timeout_ms = tonumber(gate.timeout_ms) or 0
  while true do
    if (dict:get("gate_epoch") or 0) ~= gate_epoch then
      dict:delete(pending_key)
      dict:delete(decision_key)
      metrics.pending_exit()
      emit(ctx, "request_decided", { action = "block" })
      return "block"
    end
    local decision = dict:get(decision_key)
    if decision == "allow" or decision == "block" then
      dict:delete(decision_key)
      dict:delete(pending_key)
      metrics.pending_exit()
      emit(ctx, "request_decided", { action = decision })
      return decision
    end
    if timeout_ms > 0 and (ngx.now() - started) * 1000 >= timeout_ms then
      dict:delete(pending_key)
      metrics.pending_exit()
      local action = gate.timeout_action == "allow" and "allow" or "block"
      emit(ctx, "request_timeout", { action = action })
      return action
    end
    ngx.sleep(0.025)
  end
end

function _M.access()
  local method = ngx.req.get_method()
  local path = ngx.var.uri
  local host = ngx.var.host or ""
  local client_ip = ngx.var.remote_addr or ""
  local subject = match_subject(config.get(), method, host, path)
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
  metrics.increment("managed_requests_total")
  emit(ctx, "request_started")

  local gateway = config.get()
  for _, rule in ipairs(gateway.rules or {}) do
    if matches_rule(rule, method, path, client_ip) then
      metrics.increment("blocked_requests_total")
      emit(ctx, "request_blocked", { action = "block", status = ngx.HTTP_FORBIDDEN })
      ngx.exit(ngx.HTTP_FORBIDDEN)
    end
  end

  if resolve_pending(ctx, subject) == "block" then
    metrics.increment("blocked_requests_total")
    emit(ctx, "request_blocked", { action = "block", status = ngx.HTTP_FORBIDDEN })
    ngx.exit(ngx.HTTP_FORBIDDEN)
  end
end

function _M.response_started()
  local ctx = ngx.ctx.httpv
  if not ctx then
    return
  end
  emit(ctx, "response_started", { status = ngx.status })
end

function _M.response_finished()
  local ctx = ngx.ctx.httpv
  if not ctx then
    return
  end
  metrics.increment("responses_total")
  if ngx.var.httpv_native_response_telemetry == "1" then
    return
  end
  emit(ctx, "response_finished", {
    status = tonumber(ngx.var.status) or ngx.status,
    response_bytes = tonumber(ngx.var.body_bytes_sent) or 0,
    duration_ms = math.max(0, now_ms() - ctx.started_at_ms)
  })
end

return _M
