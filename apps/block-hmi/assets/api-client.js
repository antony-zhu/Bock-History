(function (window) {
  "use strict";

  var config = window.HMI_CONFIG || {};
  var configuredTimeoutMs = Number(config.requestTimeoutMs);
  var timeoutMs = Number.isFinite(configuredTimeoutMs) && configuredTimeoutMs > 0
    ? configuredTimeoutMs
    : 12000;

  function defaultAPIBase() {
    var path = window.location.pathname || "/";
    if (path.charAt(path.length - 1) !== "/") {
      path = path.slice(0, path.lastIndexOf("/") + 1);
    }
    return path + "api/v1/";
  }

  var apiBase = String(config.apiBase || defaultAPIBase());
  if (apiBase.charAt(apiBase.length - 1) !== "/") apiBase += "/";

  function requestID() {
    return "hmi-" + Date.now().toString(36) + "-" + Math.random().toString(36).slice(2, 10);
  }

  function APIError(message, status, code, fields) {
    this.name = "APIError";
    this.message = message || "后台请求失败";
    this.status = status || 0;
    this.code = code || "request_failed";
    this.fields = fields || null;
  }
  APIError.prototype = Object.create(Error.prototype);
  APIError.prototype.constructor = APIError;

  function parseResponse(response) {
    return response.text().then(function (text) {
      var payload = {};
      if (text) {
        try {
          payload = JSON.parse(text);
        } catch (error) {
          throw new APIError("后台返回了无法识别的数据", response.status, "invalid_json");
        }
      }
      if (!response.ok) {
        var detail = payload && payload.error ? payload.error : {};
        throw new APIError(
          detail.message || ("后台请求失败（HTTP " + response.status + "）"),
          response.status,
          detail.code,
          detail.fields
        );
      }
      return payload;
    });
  }

  function request(path, options) {
    var requestOptions = options || {};
    var headers = {
      Accept: "application/json",
      "X-Request-ID": requestOptions.requestId || requestID()
    };
    var method = requestOptions.method || "GET";
    var idempotencyKey = requestOptions.idempotencyKey;
    var controller = new window.AbortController();
    var timedOut = false;

    if (requestOptions.operator) headers["X-Operator"] = requestOptions.operator;
    if (requestOptions.body !== undefined) headers["Content-Type"] = "application/json";
    if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;

    var timer = window.setTimeout(function () {
      timedOut = true;
      controller.abort();
    }, timeoutMs);

    return window.fetch(apiBase + String(path).replace(/^\/+/, ""), {
      method: method,
      credentials: "same-origin",
      cache: "no-store",
      headers: headers,
      body: requestOptions.body === undefined ? undefined : JSON.stringify(requestOptions.body),
      signal: controller.signal
    }).then(parseResponse).catch(function (error) {
      if (error instanceof APIError) throw error;
      if (timedOut || (error && error.name === "AbortError")) {
        throw new APIError(
          method === "GET" || method === "HEAD"
            ? "后台响应超时"
            : "后台响应超时，操作结果未知",
          0,
          "timeout"
        );
      }
      throw new APIError("无法连接后台服务", 0, "network_error");
    }).then(function (payload) {
      window.clearTimeout(timer);
      return payload;
    }, function (error) {
      window.clearTimeout(timer);
      throw error;
    });
  }

  function write(path, method, body, context) {
    var options = context || {};
    return request(path, {
      method: method,
      body: body,
      operator: options.operator,
      requestId: options.requestId,
      idempotencyKey: options.idempotencyKey || requestID()
    });
  }

  window.HMIBackend = {
    APIError: APIError,
    getState: function () {
      return request("state");
    },
    updateSettings: function (settings, context) {
      return write("settings", "PUT", settings, context);
    },
    sendCommand: function (command, payload, context) {
      var body = { command: command };
      Object.keys(payload || {}).forEach(function (key) {
        body[key] = payload[key];
      });
      return write("commands", "POST", body, context);
    },
    acknowledgeAlarm: function (alarmId, context) {
      var options = context || {};
      var body = {};
      if (options.expectedRevision !== undefined) body.expectedRevision = options.expectedRevision;
      return write("alarms/" + encodeURIComponent(alarmId) + "/ack", "POST", body, options);
    },
    getAudit: function (options) {
      var query = options || {};
      var parts = [];
      if (query.limit) parts.push("limit=" + encodeURIComponent(query.limit));
      if (query.beforeId) parts.push("beforeId=" + encodeURIComponent(query.beforeId));
      return request("audit" + (parts.length ? "?" + parts.join("&") : ""));
    },
    getBase: function () {
      return apiBase;
    }
  };
})(window);
