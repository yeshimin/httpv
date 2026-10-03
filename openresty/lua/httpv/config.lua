local cjson = require "cjson.safe"

local _M = {}

local default_upstream_url = os.getenv("HTTPV_UPSTREAM_URL") or "http://demo:9000"
local default_upstream_host = os.getenv("HTTPV_UPSTREAM_HOST") or "demo:9000"

local default_config = {
  upstreams = {
    { id = "local-app", name = "Local application", url = default_upstream_url, host_header = default_upstream_host }
  },
  routes = {
    { id = "default-route", name = "Default route", enabled = true, match = { path_prefix = "/" }, upstream_id = "local-app" }
  },
  subjects = {
    {
      id = "default",
      name = "Default traffic",
      enabled = true,
      display = true,
      match = { path_prefix = "/" },
      gate = { enabled = false, timeout_ms = 30000, timeout_action = "block" }
    }
  },
  rules = {}
}

function _M.install_default()
  local dict = ngx.shared.httpv_config
  if not dict:get("gateway_config") then
    assert(dict:set("gateway_config", cjson.encode(default_config)))
  end
end

function _M.get()
  local encoded = ngx.shared.httpv_config:get("gateway_config")
  if not encoded then
    return default_config
  end
  return cjson.decode(encoded) or default_config
end

function _M.put(config)
  if type(config) ~= "table"
    or type(config.upstreams) ~= "table"
    or type(config.routes) ~= "table"
    or type(config.subjects) ~= "table"
    or type(config.rules) ~= "table" then
    return nil, "invalid configuration"
  end
  local encoded = cjson.encode(config)
  if not encoded then
    return nil, "configuration encoding failed"
  end
  return ngx.shared.httpv_config:set("gateway_config", encoded)
end

return _M
