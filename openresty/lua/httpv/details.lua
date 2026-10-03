local cjson = require "cjson.safe"
local metrics = require "httpv.metrics"

local _M = {}

local always_redact = {
  authorization = true,
  cookie = true,
  ["set-cookie"] = true,
  ["proxy-authorization"] = true,
  ["x-api-key"] = true
}

local function lower(value)
  return string.lower(tostring(value or ""))
end

local function allowed_content_type(policy, content_type)
  content_type = lower(content_type)
  if content_type == "" then
    return false
  end
  for _, allowed in ipairs(policy.content_types or {}) do
    allowed = lower(allowed)
    if allowed ~= "" and string.sub(content_type, 1, #allowed) == allowed then
      return true
    end
  end
  return false
end

local function redaction_set(policy)
  local result = {}
  for name in pairs(always_redact) do
    result[name] = true
  end
  for _, name in ipairs(policy.redact_headers or {}) do
    result[lower(name)] = true
  end
  return result
end

local function sanitize_headers(headers, policy)
  local result = {}
  local redacted = redaction_set(policy)
  for name, value in pairs(headers or {}) do
    if redacted[lower(name)] then
      result[name] = "[REDACTED]"
    elseif type(value) == "table" then
      local values = {}
      for _, item in ipairs(value) do
        values[#values + 1] = tostring(item)
      end
      result[name] = values
    else
      result[name] = tostring(value)
    end
  end
  return result
end

local function truncate(value, maximum)
  if not value then
    return nil, false
  end
  if #value <= maximum then
    return value, false
  end
  return string.sub(value, 1, maximum), true
end

local function persist(ctx)
  if not ctx.detail then
    return
  end
  local encoded = cjson.encode(ctx.detail)
  if not encoded then
    metrics.increment("details_dropped_total")
    return
  end
  local ok = ngx.shared.httpv_details:set("detail:" .. ctx.request_id, encoded, ctx.detail_ttl)
  if not ok then
    metrics.increment("details_dropped_total")
  end
end

function _M.begin(ctx, subject)
  local policy = subject.capture or {}
  if not policy.enabled then
    return
  end
  local sample_rate = tonumber(policy.sample_rate) or 1
  if sample_rate < 1 and math.random() > sample_rate then
    return
  end
  local maximum = math.max(1, tonumber(policy.max_body_bytes) or 16384)
  ctx.detail_policy = policy
  ctx.detail_ttl = math.max(1, tonumber(policy.ttl_seconds) or 60)
  ctx.detail = {
    request_id = ctx.request_id,
    subject_id = ctx.subject_id,
    captured_at = math.floor(ngx.now() * 1000),
    request = {},
    response = {}
  }
  local request_headers = ngx.req.get_headers(100)
  local request_content_type = request_headers["content-type"] or request_headers["Content-Type"] or ""
  ctx.detail.request.content_type = tostring(request_content_type)
  if policy.request_headers then
    ctx.detail.request.headers = sanitize_headers(request_headers, policy)
  end
  if policy.request_body and allowed_content_type(policy, request_content_type) then
    ngx.req.read_body()
    local body = ngx.req.get_body_data()
    if body then
      ctx.detail.request.body, ctx.detail.request.truncated = truncate(body, maximum)
      metrics.increment("detail_bytes_captured_total", #(ctx.detail.request.body or ""))
    elseif ngx.req.get_body_file() then
      ctx.detail.request.body_unavailable = "buffered_to_file"
    end
  end
  metrics.increment("details_captured_total")
  persist(ctx)
end

function _M.response_started(ctx)
  if not ctx or not ctx.detail then
    return
  end
  local policy = ctx.detail_policy
  local response_headers = ngx.resp.get_headers(100)
  local content_type = response_headers["content-type"] or response_headers["Content-Type"] or ngx.header.content_type or ""
  ctx.detail.response.content_type = tostring(content_type)
  ctx.detail_capture_response_body = policy.response_body and allowed_content_type(policy, content_type)
  if policy.response_headers then
    ctx.detail.response.headers = sanitize_headers(response_headers, policy)
  end
  persist(ctx)
end

function _M.response_body(ctx, chunk)
  if not ctx or not ctx.detail or not ctx.detail_capture_response_body or not chunk or chunk == "" then
    return
  end
  local maximum = math.max(1, tonumber(ctx.detail_policy.max_body_bytes) or 16384)
  local current = ctx.detail_response_body_bytes or 0
  if current >= maximum then
    ctx.detail.response.truncated = true
    return
  end
  local remaining = maximum - current
  local captured, truncated = truncate(chunk, remaining)
  ctx.detail_response_body_parts = ctx.detail_response_body_parts or {}
  ctx.detail_response_body_parts[#ctx.detail_response_body_parts + 1] = captured
  ctx.detail_response_body_bytes = current + #captured
  if truncated then
    ctx.detail.response.truncated = true
  end
end

function _M.finish(ctx, status, duration_ms)
  if not ctx or not ctx.detail then
    return
  end
  if ctx.detail_response_body_parts then
    ctx.detail.response.body = table.concat(ctx.detail_response_body_parts)
    metrics.increment("detail_bytes_captured_total", #(ctx.detail.response.body or ""))
  end
  ctx.detail.response.status = status
  ctx.detail.duration_ms = duration_ms
  ctx.detail.finished_at = math.floor(ngx.now() * 1000)
  persist(ctx)
end

function _M.abort(ctx)
  _M.finish(ctx, 499, math.max(0, math.floor(ngx.now() * 1000) - (ctx.started_at_ms or 0)))
end

function _M.get(request_id)
  local encoded = ngx.shared.httpv_details:get("detail:" .. request_id)
  if not encoded then
    return nil
  end
  return cjson.decode(encoded)
end

return _M
