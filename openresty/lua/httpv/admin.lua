local cjson = require "cjson.safe"
local config = require "httpv.config"

local _M = {}

local function json(status, payload)
  ngx.status = status
  ngx.header.content_type = "application/json"
  ngx.say(cjson.encode(payload))
  return ngx.exit(status)
end

function _M.put_config()
  ngx.req.read_body()
  local body = ngx.req.get_body_data()
  local gateway = body and cjson.decode(body)
  local ok, err = config.put(gateway)
  if not ok then
    return json(ngx.HTTP_BAD_REQUEST, { error = err or "invalid config" })
  end
  return json(ngx.HTTP_OK, { status = "applied" })
end

function _M.decide(request_id)
  ngx.req.read_body()
  local body = ngx.req.get_body_data()
  local payload = body and cjson.decode(body)
  local action = payload and payload.action
  if action ~= "allow" and action ~= "block" then
    return json(ngx.HTTP_BAD_REQUEST, { error = "invalid action" })
  end
  local pending_key = "pending:" .. request_id
  if not ngx.shared.httpv_pending:get(pending_key) then
    return json(ngx.HTTP_NOT_FOUND, { error = "request is not pending" })
  end
  ngx.shared.httpv_pending:set("decision:" .. request_id, action, 120)
  return json(ngx.HTTP_OK, { status = "accepted" })
end

function _M.decide_batch()
  ngx.req.read_body()
  local body = ngx.req.get_body_data()
  local payload = body and cjson.decode(body)
  local action = payload and payload.action
  local request_ids = payload and payload.request_ids
  if (action ~= "allow" and action ~= "block") or type(request_ids) ~= "table" then
    return json(ngx.HTTP_BAD_REQUEST, { error = "invalid batch decision" })
  end
  local accepted = 0
  local pending = ngx.shared.httpv_pending
  for _, request_id in ipairs(request_ids) do
    if type(request_id) == "string" and pending:get("pending:" .. request_id) then
      pending:set("decision:" .. request_id, action, 120)
      accepted = accepted + 1
    end
  end
  return json(ngx.HTTP_OK, { accepted = accepted })
end

function _M.block_all_pending()
  local epoch = ngx.shared.httpv_pending:incr("gate_epoch", 1, 0)
  return json(ngx.HTTP_OK, { epoch = epoch })
end

return _M
