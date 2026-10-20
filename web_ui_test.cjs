const test = require("node:test");
const assert = require("node:assert/strict");

function loadUI() {
  try {
    return require("./web/ui.js");
  } catch {
    return {};
  }
}

test("presents runner statuses in Chinese with uptime", () => {
  const ui = loadUI();
  assert.equal(typeof ui.statusPresentation, "function");

  const presentation = ui.statusPresentation(
    { status: "running", startedAt: "2026-07-10T10:00:00Z" },
    new Date("2026-07-10T10:01:05Z"),
  );

  assert.deepEqual(presentation, {
    label: "运行中",
    meta: "运行正常 · 已运行 1 分 5 秒",
    tone: "running",
  });
});

test("derives state-aware controls for a dirty running config", () => {
  const ui = loadUI();
  assert.equal(typeof ui.controlState, "function");

  assert.deepEqual(ui.controlState("running", true, false), {
    fieldsDisabled: false,
    saveDisabled: false,
    saveLabel: "应用并重启",
    startDisabled: true,
    stopDisabled: false,
    exitDisabled: false,
  });
});

test("validates TTL", () => {
  const ui = loadUI();
  assert.equal(typeof ui.validateConfig, "function");

  assert.deepEqual(ui.validateConfig({ ttl: 0 }), {
    ttl: "TTL 必须是 1 到 255 之间的整数",
  });
  assert.deepEqual(ui.validateConfig({ ttl: 64 }), {});
});

test("ignores legacy ports because HTTP is detected on every TCP port", () => {
  const ui = loadUI();
  assert.deepEqual(ui.validateConfig({ ttl: 64, ports: "not-a-port" }), {});
});

test("applies a running config by saving before restart", async () => {
  const ui = loadUI();
  assert.equal(typeof ui.applyAndRestart, "function");

  const calls = [];
  const result = await ui.applyAndRestart({
    save: async () => calls.push("save"),
    stop: async () => calls.push("stop"),
    refresh: async () => {
      calls.push("refresh");
      return { status: "idle" };
    },
    pause: async () => calls.push("pause"),
    start: async () => {
      calls.push("start");
      return "started";
    },
  });

  assert.equal(result, "started");
  assert.deepEqual(calls, ["save", "stop", "refresh", "start"]);
});

test("driver diagnostics distinguish missing services and partial failures", () => {
  const ui = loadUI();
  assert.match(ui.formatDiagnostics({services:[],error:""}), /未发现/);
  const partial = ui.formatDiagnostics({services:[],error:"access denied"});
  assert.match(partial, /access denied/);
  assert.doesNotMatch(partial, /未发现/);
  const result = ui.formatDiagnostics({services:[{service:"WinDivert",registeredPath:"D:\\old\\WinDivert64.sys",expectedPath:"D:\\new\\WinDivert64.sys",running:true,message:"注册路径已失效"}]});
  assert.match(result,/WinDivert · 运行中/);
  assert.match(result,/D:\\old/);
  assert.match(result,/注册路径已失效/);
});

test("browser requests carry the page session token", async () => {
  const fs = require("node:fs");
  const vm = require("node:vm");
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, {addEventListener(){},setAttribute(){},dataset:{},value:"",textContent:""});
      return elements.get(id);
    },
    querySelector(selector) {
      assert.equal(selector, 'meta[name="ua3f-token"]');
      return {content:"session-secret"};
    },
  };
  const requests = [];
  const context = vm.createContext({document, console, UA3FUI: loadUI(), window:{setInterval(){}}, fetch:async(url, options)=>{
    requests.push({url,options});
    return {ok:true,json:async()=>({status:"idle",logs:[],ttl:64})};
  }});
  vm.runInContext(fs.readFileSync(require.resolve("./web/app.js"),"utf8"), context);
  await vm.runInContext('fetchJSON("/api/stop", {method:"POST",headers:{"X-Test":"kept"}})',context);
  const request = requests.find(({url}) => url === "/api/stop");
  assert.equal(request.options.headers["X-UA3F-Token"],"session-secret");
  assert.equal(request.options.headers["X-Test"],"kept");
  assert.equal(request.options.method,"POST");
});

test("stop remains available after an error so handle cleanup can be retried", () => {
  assert.equal(loadUI().controlState("error", false, false).stopDisabled, false);
});
