// you-as-bee web UI. Vanilla JavaScript, no build step.
//
// The browser authenticates with an HttpOnly session cookie set by the server;
// JavaScript never holds or sends a bearer token. State-changing requests carry
// the X-YAB-CSRF header so a cross-site request cannot drive them.
(function () {
  "use strict";

  var body = document.body;
  var MODE = body.getAttribute("data-yab-mode") || "agent";
  var API = body.getAttribute("data-yab-api") || "/v1";
  var CSRF = "X-YAB-CSRF";

  function url(path) {
    return API.replace(/\/$/, "") + path;
  }

  function handleStatus(res) {
    if (res.status === 401 || res.status === 403) {
      window.location.href = "/ui/login";
      throw new Error("unauthenticated");
    }
    if (!res.ok) {
      throw new Error("request failed: " + res.status);
    }
    return res;
  }

  function get(path) {
    return fetch(url(path), { credentials: "same-origin" })
      .then(handleStatus)
      .then(function (r) { return r.json(); });
  }

  function post(path, payload) {
    var headers = {};
    headers[CSRF] = "1";
    var opts = { method: "POST", credentials: "same-origin", headers: headers };
    if (payload !== undefined) {
      headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(payload);
    }
    return fetch(url(path), opts)
      .then(handleStatus)
      .then(function (r) { return r.json().catch(function () { return {}; }); });
  }

  // --- adapters -----------------------------------------------------------
  // Each adapter exposes the same surface so the rendering code below is
  // mode-agnostic. The agent adapter talks straight to yabd's management API.
  var adapters = {
    agent: {
      info: function () { return get("/info"); },
      devices: function () { return get("/devices").then(function (r) { return r.devices || []; }); },
      logs: function (after, limit) {
        return get("/logs?after=" + encodeURIComponent(after) + "&limit=" + encodeURIComponent(limit));
      },
      exportDevice: function (pin, force) {
        return post("/devices/" + encodeURIComponent(pin) + "/export" + (force ? "?force=true" : ""));
      },
      unexportDevice: function (pin) {
        return post("/devices/" + encodeURIComponent(pin) + "/unexport");
      },
      resetDevice: function (pin) {
        return post("/devices/" + encodeURIComponent(pin) + "/reset");
      },
      events: function () { return new EventSource(url("/events")); }
    },
    // The Windows client will expose its own surface in a later milestone; the
    // shell only needs the adapter to exist so the shared assets are complete.
    client: {
      info: function () { return Promise.resolve({}); },
      devices: function () { return Promise.resolve([]); },
      logs: function () { return Promise.resolve({ entries: [], next: 0 }); },
      exportDevice: function () { return Promise.resolve({}); },
      unexportDevice: function () { return Promise.resolve({}); },
      resetDevice: function () { return Promise.resolve({}); },
      events: function () { return null; }
    }
  };

  var api = adapters[MODE] || adapters.agent;

  // --- rendering ----------------------------------------------------------

  var STATE_CLASS = {
    exported: "badge-ok",
    in_use: "badge-ok",
    unexported: "badge-idle",
    absent: "badge-idle",
    error: "badge-error"
  };

  function text(tag, value) {
    var el = document.createElement(tag);
    el.textContent = value == null ? "" : String(value);
    return el;
  }

  function badge(state) {
    var el = text("span", state || "unknown");
    el.className = "badge " + (STATE_CLASS[state] || "badge-unknown");
    return el;
  }

  function definitionList(pairs) {
    var dl = document.createElement("dl");
    pairs.forEach(function (pair) {
      dl.appendChild(text("dt", pair[0]));
      dl.appendChild(text("dd", pair[1] === "" || pair[1] == null ? "-" : pair[1]));
    });
    return dl;
  }

  function renderInfo(info) {
    var host = document.getElementById("info");
    host.textContent = "";
    host.appendChild(text("dt", "Version"));
    host.appendChild(text("dd", info.version));
    host.appendChild(text("dt", "Hostname"));
    host.appendChild(text("dd", info.hostname));
    host.appendChild(text("dt", "Uptime"));
    host.appendChild(text("dd", info.uptime_sec + "s"));
    host.appendChild(text("dt", "usbipd"));
    host.appendChild(text("dd", info.usbipd_up ? "up" : "down"));
  }

  function actionButton(label, run) {
    var b = text("button", label);
    b.type = "button";
    b.addEventListener("click", function () {
      b.disabled = true;
      Promise.resolve()
        .then(run)
        .then(refresh)
        .catch(function (err) { console.error(err); })
        .then(function () { b.disabled = false; });
    });
    return b;
  }

  function renderDevices(devices) {
    var host = document.getElementById("devices");
    host.textContent = "";
    if (!devices.length) {
      host.appendChild(text("p", "No configured devices."));
      return;
    }
    devices.forEach(function (dev) {
      var card = document.createElement("div");
      card.className = "device";

      var head = document.createElement("div");
      head.className = "device-head";
      head.appendChild(text("strong", dev.pin));
      head.appendChild(badge(dev.state));

      var actions = document.createElement("div");
      actions.className = "device-actions";
      actions.appendChild(actionButton("Export", function () { return api.exportDevice(dev.pin, false); }));
      actions.appendChild(actionButton("Force", function () { return api.exportDevice(dev.pin, true); }));
      actions.appendChild(actionButton("Unexport", function () { return api.unexportDevice(dev.pin); }));
      actions.appendChild(actionButton("Reset", function () { return api.resetDevice(dev.pin); }));
      head.appendChild(actions);
      card.appendChild(head);

      card.appendChild(definitionList([
        ["busid", dev.busid],
        ["vid:pid", dev.vid && dev.pid ? dev.vid + ":" + dev.pid : ""],
        ["product", dev.product],
        ["driver", dev.driver],
        ["present", dev.present ? "yes" : "no"],
        ["mode", dev.mode],
        ["last error", dev.last_error]
      ]));
      host.appendChild(card);
    });
  }

  var logsAfter = 0;

  function renderLogs(resp) {
    var host = document.getElementById("logs");
    (resp.entries || []).forEach(function (entry) {
      var line = document.createElement("div");
      line.className = "log-line";
      var lvl = text("span", entry.level);
      lvl.className = "lvl lvl-" + entry.level;
      line.appendChild(lvl);
      line.appendChild(text("span", entry.msg));
      if (entry.attrs) {
        var parts = Object.keys(entry.attrs).map(function (k) { return k + "=" + entry.attrs[k]; });
        if (parts.length) {
          line.appendChild(text("span", parts.join(" ")));
        }
      }
      host.appendChild(line);
      logsAfter = entry.seq;
    });
    while (host.childNodes.length > 500) {
      host.removeChild(host.firstChild);
    }
    host.scrollTop = host.scrollHeight;
  }

  // --- wiring -------------------------------------------------------------

  function refresh() {
    return Promise.all([api.info(), api.devices()])
      .then(function (results) {
        renderInfo(results[0] || {});
        renderDevices(results[1] || []);
        setConnection("ok", "connected");
      })
      .catch(function () { setConnection("error", "offline"); });
  }

  function setConnection(cls, label) {
    var el = document.getElementById("connection");
    if (!el) { return; }
    el.className = "badge badge-" + cls;
    el.textContent = label;
  }

  function pollLogs() {
    api.logs(logsAfter, 200)
      .then(function (resp) { renderLogs(resp); })
      .catch(function () {});
  }

  function subscribe() {
    var stream = api.events();
    if (!stream) { return; }
    stream.addEventListener("device_added", refresh);
    stream.addEventListener("device_removed", refresh);
    stream.addEventListener("state_changed", refresh);
    stream.onopen = function () { setConnection("ok", "live"); };
    stream.onerror = function () { setConnection("warn", "reconnecting"); };
  }

  var logout = document.getElementById("logout");
  if (logout) {
    logout.addEventListener("click", function () {
      var headers = {};
      headers[CSRF] = "1";
      fetch("/ui/logout", { method: "POST", credentials: "same-origin", headers: headers })
        .then(function () { window.location.href = "/ui/login"; });
    });
  }

  refresh();
  subscribe();
  pollLogs();
  setInterval(pollLogs, 3000);
})();
