(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }
  root.UA3FUI = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  function formatDuration(startedAt, now = new Date()) {
    const started = new Date(startedAt);
    if (Number.isNaN(started.getTime())) {
      return "0 秒";
    }

    const totalSeconds = Math.max(0, Math.floor((now.getTime() - started.getTime()) / 1000));
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;
    const parts = [];

    if (hours > 0) parts.push(`${hours} 小时`);
    if (minutes > 0) parts.push(`${minutes} 分`);
    if (seconds > 0 || parts.length === 0) parts.push(`${seconds} 秒`);
    return parts.join(" ");
  }

  function statusPresentation(snapshot, now = new Date()) {
    const presentations = {
      idle: { label: "已停止", meta: "等待启动", tone: "idle" },
      starting: { label: "启动中", meta: "正在启动改写...", tone: "starting" },
      stopping: { label: "停止中", meta: "正在停止改写...", tone: "stopping" },
      error: {
        label: "运行异常",
        meta: snapshot.lastError || "请查看实时日志",
        tone: "error",
      },
    };

    if (snapshot.status === "running") {
      let meta = `运行正常 · 已运行 ${formatDuration(snapshot.startedAt, now)}`;
      if (snapshot.configPending) {
        if (snapshot.runningConfig) meta += ` · 当前 TTL ${snapshot.runningConfig.ttl}`;
        meta += " · 已保存新配置，点击“应用并重启”后生效";
      }
      return {
        label: "运行中",
        meta,
        tone: "running",
      };
    }

    return presentations[snapshot.status] || {
      label: snapshot.status || "未知状态",
      meta: snapshot.lastError || "状态未知",
      tone: "idle",
    };
  }

  function controlState(status, dirty, pending) {
    const busy = pending || status === "starting" || status === "stopping";
    const running = status === "running";
    return {
      fieldsDisabled: busy,
      saveDisabled: busy || (running && !dirty),
      saveLabel: running ? (dirty ? "应用并重启" : "配置已生效") : "保存配置",
      startDisabled: busy || running,
      stopDisabled: busy || (!running && status !== "error"),
      exitDisabled: busy,
    };
  }

  function validateConfig(config) {
    const errors = {};
    if (!Number.isInteger(config.ttl) || config.ttl < 1 || config.ttl > 255) {
      errors.ttl = "TTL 必须是 1 到 255 之间的整数";
    }

    return errors;
  }

  async function applyAndRestart(actions) {
    await actions.save();
    await actions.stop();

    for (let attempt = 0; attempt < 50; attempt += 1) {
      const snapshot = await actions.refresh();
      if (snapshot.status === "idle") {
        return actions.start();
      }
      if (snapshot.status === "error") {
        throw new Error(snapshot.lastError || "停止改写失败");
      }
      await actions.pause();
    }

    throw new Error("等待停止超时，请查看实时日志");
  }

  function formatDiagnostics(result) {
    const lines = [];
    if (result.error) lines.push(`诊断未完成：${result.error}`);
    for (const service of result.services || []) {
      lines.push(`${service.service} · ${service.running ? "运行中" : "未运行"}`,
        `注册路径：${service.registeredPath}`, `当前路径：${service.expectedPath}`, service.message, "");
    }
    if (!result.error && !result.services?.length) lines.push("未发现已注册的 WinDivert 服务。首次启动前属于正常情况。");
    return lines.join("\n");
  }

  return { applyAndRestart, controlState, formatDiagnostics, formatDuration, statusPresentation, validateConfig };
});
