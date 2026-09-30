local cjson = require "cjson.safe"

local _M = {}

local default_config = {
  subjects = {
    {
      id = "demo-api",
      name = "Demo API",
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
  if type(config) ~= "table" or type(config.subjects) ~= "table" or type(config.rules) ~= "table" then
    return nil, "invalid configuration"
  end
  local encoded = cjson.encode(config)
  if not encoded then
    return nil, "configuration encoding failed"
  end
  return ngx.shared.httpv_config:set("gateway_config", encoded)
end

return _M
