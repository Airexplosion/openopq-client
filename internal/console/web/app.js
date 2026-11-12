"use strict";

const $ = (id) => document.getElementById(id);
const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));
const NS = "http://www.w3.org/2000/svg";

const PHASES = {
  restoring: { label: "恢复登录", tone: "busy" },
  idle: { label: "未创建", tone: "idle" },
  ready: { label: "待扫码", tone: "ready" },
  requesting: { label: "获取二维码", tone: "busy" },
  waiting: { label: "等待扫码", tone: "warn" },
  authenticating: { label: "正在认证", tone: "busy" },
  securing: { label: "建立安全会话", tone: "busy" },
  registering: { label: "正在上线", tone: "busy" },
  online: { label: "在线", tone: "online" },
  reconnecting: { label: "正在重连", tone: "busy" },
  offline: { label: "已离线", tone: "muted" },
  cancelled: { label: "已取消", tone: "muted" },
  error: { label: "登录失败", tone: "danger" },
};

const BUSY_PHASES = new Set(["restoring", "requesting", "waiting", "authenticating", "securing", "registering"]);
const LEVELS = new Set(["DEBUG", "INFO", "WARN", "ERROR"]);
const MAX_LOGS = 700;
const MAX_HISTORY = 20;
const MAX_WS_EVENTS = 80;
const SAMPLE_LIMIT = 24;
const POLL_INTERVAL = 2500;
const SIGNER_INTERVAL = 15000;
const SECRET_MIN_LENGTH = 16;
const AUTH_TIMEOUT = 10000;
const SECURITY_TIMEOUT = 15000;

const VIEW_META = {
  overview: {
    crumb: "运行概览",
    title: "运行概览",
    subtitle: "当前会话、网关连接与签名状态的实时概览。",
  },
  accounts: {
    crumb: "账号与状态",
    title: "账号与状态",
    subtitle: "每个 QQ 的登录状态、运行数据与设备档案彼此独立。",
  },
  settings: {
    crumb: "设置",
    title: "设置",
    subtitle: "为当前账号配置协议入口，并在同一页管理实例级的控制台与 API 访问安全。",
  },
  api: {
    crumb: "API 测试",
    title: "API 测试",
    subtitle: "使用真实传输入口发送协议请求，并查看响应、延迟与本地历史。",
  },
  logs: {
    crumb: "实时日志",
    title: "实时日志",
    subtitle: "当前会话的独立日志缓冲，支持筛选、搜索、暂停与导出。",
  },
  debug: {
    crumb: "Debug 诊断",
    title: "Debug 诊断",
    subtitle: "只读诊断：健康检查、传输快照与客户端 / 网关进程内存。",
  },
};

const SERVICE_DEFS = [
  {
    key: "opq_http",
    group: "OPQ",
    label: "OPQ · HTTP",
    short: "HTTP",
    kind: "http",
    icon: "i-link",
    pathLabel: "/v1/LuaApiCaller",
  },
  {
    key: "opq_ws",
    group: "OPQ",
    label: "OPQ · WebSocket",
    short: "WS",
    kind: "ws",
    icon: "i-wifi",
    pathLabel: "/ws",
  },
  {
    key: "onebot_http",
    group: "OneBot v11",
    label: "OneBot · HTTP",
    short: "HTTP",
    kind: "http",
    icon: "i-braces",
    pathLabel: "/onebot/v11/",
  },
  {
    key: "onebot_ws",
    group: "OneBot v11",
    label: "OneBot · WebSocket",
    short: "WS",
    kind: "ws",
    icon: "i-wifi",
    pathLabel: "/onebot/v11/ws",
  },
];

SERVICE_DEFS.push({key:"onebot12_http",group:"OneBot v12",label:"OneBot v12 · HTTP",short:"HTTP",kind:"http",icon:"i-braces",pathLabel:"/onebot/v12/"},{key:"onebot12_ws",group:"OneBot v12",label:"OneBot v12 · WebSocket",short:"WS",kind:"ws",icon:"i-wifi",pathLabel:"/onebot/v12/"});

const SERVICE_BY_KEY = Object.fromEntries(SERVICE_DEFS.map((definition) => [definition.key, definition]));

const OPQ_CLUSTER_BODY = JSON.stringify({ CgiCmd: "ClusterInfo", CgiRequest: {} }, null, 2);
const OPQ_GROUPS_BODY = JSON.stringify({ CgiCmd: "GetGroupLists", CgiRequest: {} }, null, 2);

const API_TEMPLATES = {
  opq: [
    {
      id: "opq-status",
      label: "状态 / 登录信息 · ClusterInfo",
      hint: "读取网关内的 QQ 账号、在线状态与实例版本。OPQ 的登录信息随 ClusterInfo 返回。",
      body: OPQ_CLUSTER_BODY,
      action: "",
    },
    {
      id: "opq-groups",
      label: "群列表 · GetGroupLists",
      hint: "获取当前账号已加入的群列表。该请求为只读。",
      body: OPQ_GROUPS_BODY,
      action: "",
    },
    {
      id: "opq-version",
      label: "版本信息 · ClusterInfo",
      hint: "ClusterInfo 的响应包含 openopq 版本与 Go 运行信息。",
      body: OPQ_CLUSTER_BODY,
      action: "",
    },
    {
      id: "custom",
      label: "自定义 JSON · 手动编写",
      hint: "自定义内容会先确认再发送。请确认 CgiCmd 与 CgiRequest 的语义。",
      body: "",
      action: "",
    },
  ],
  onebot: [
    {id:"onebot-capabilities",label:"能力清单 · get_onebot_capabilities",hint:"显示实现状态；未实现的 QQ 能力会明确返回失败，不会伪造数据。",body:"{}",action:"get_onebot_capabilities"},
    {id:"onebot-requests",label:"待处理群请求 · get_group_system_msg",hint:"只读查询当前账号的群申请和邀请，返回可用于审批的 flag。",body:"{}",action:"get_group_system_msg"},
    {id:"onebot-friend-approve",label:"好友请求审批 · set_friend_add_request",hint:"填写当前账号 request 事件的 flag。approve 为 false 表示拒绝；执行前确认。",body:JSON.stringify({flag:"填写本账号的请求标记",approve:true,remark:""},null,2),action:"set_friend_add_request"},
    {id:"onebot-group-approve",label:"加群请求审批 · set_group_add_request",hint:"flag 和 sub_type 必须与当前账号的原始请求一致，支持 add / invite。",body:JSON.stringify({flag:"填写本账号的请求标记",sub_type:"add",approve:true,reason:""},null,2),action:"set_group_add_request"},
    {id:"onebot-forward",label:"合并转发 · get_forward_msg",hint:"填写消息 forward 段的 id，读取节点和按原顺序排列的内容。",body:JSON.stringify({id:"填写合并转发ID"},null,2),action:"get_forward_msg"},
    {id:"onebot-image",label:"下载图片 · get_image",hint:"填写本账号收到的 image 段中的 file，返回本机缓存路径。",body:JSON.stringify({file:"填写收到的图片文件名"},null,2),action:"get_image"},
    {id:"onebot-record",label:"语音转换 · get_record",hint:"填写本账号收到的 record 文件名；支持 wav / mp3 / amr / wma / m4a / spx / ogg / flac。",body:JSON.stringify({file:"填写收到的语音文件名",out_format:"wav"},null,2),action:"get_record"},
    {id:"onebot-clean",label:"清理媒体缓存 · clean_cache",hint:"只清理当前账号的媒体文件和近期 OneBot 消息索引，保留登录与签名状态。",body:"{}",action:"clean_cache"},
    {id:"onebot-friends",label:"好友列表 · get_friend_list",hint:"从当前 QQ 会话分页查询好友。",body:"{}",action:"get_friend_list"},
    {id:"onebot-members",label:"群成员列表 · get_group_member_list",hint:"填写已加入群的 group_id；只读查询。",body:JSON.stringify({group_id:123456},null,2),action:"get_group_member_list"},
    {id:"onebot-message",label:"消息详情 · get_msg",hint:"使用本账号近期事件或发送结果的 message_id；最多保留 1024 条/1 小时。",body:JSON.stringify({message_id:1},null,2),action:"get_msg"},
    {id:"onebot-delete",label:"撤回消息 · delete_msg",hint:"使用本账号的 message_id，不需要 GUID 或额外群号。执行前会确认。",body:JSON.stringify({message_id:1},null,2),action:"delete_msg"},
    {
      id: "onebot-status",
      label: "状态信息 · get_status",
      hint: "OneBot v11 标准状态查询，返回 online / good。",
      body: "{}",
      action: "get_status",
    },
    {
      id: "onebot-login",
      label: "登录信息 · get_login_info",
      hint: "读取当前账号的 UIN 与昵称。",
      body: "{}",
      action: "get_login_info",
    },
    {
      id: "onebot-groups",
      label: "群列表 · get_group_list",
      hint: "读取当前账号的群列表，返回 group_id、group_name 与 member_count。",
      body: "{}",
      action: "get_group_list",
    },
    {
      id: "onebot-version",
      label: "版本信息 · get_version_info",
      hint: "读取 OneBot 协议版本与 openopq 应用版本。",
      body: "{}",
      action: "get_version_info",
    },
    {
      id: "custom",
      label: "自定义 JSON · 手动编写",
      hint: "自定义动作会先确认。根信封模式需要包含 action 字段。",
      body: "{}",
      action: "custom_action",
    },
  ],
};

const DANGEROUS_ACTION = /(send|MessageSvc\.PbSendMsg|delete_msg|set_group|kick|ban|leave|recall|poke|mute|upload|dismiss|approve|reject|friend_)/i;
const SENSITIVE_KEY = /(guid|qr|qrcode|token|secret|password|passwd|authorization|cookie|credential|api[-_]?key)/i;

const app = {
  view: "overview",
  sessionID: "",
  sessions: [],
  profiles: [],
  status: null,
  statusError: "",
  profilesError: "",
  transports: null,
  transportsError: "",
  transportsAt: 0,
  transportsStale: false,
  signer: null,
  signerFetchError: "",
  signerAt: 0,
  signerBusy: false,
  lastSync: 0,
  polling: false,
  pollSeq: 0,
  pollAbort: null,
  epoch: 0,
  sessionMeta: new Map(),
  logs: new Map(),
  logsPaused: false,
  logLevel: "ALL",
  logSearch: "",
  drafts: new Map(),
  history: new Map(),
  transportHistory: new Map(),
  settingsBusy: false,
  settingsError: "",
  settingsSaveToken: 0,
  localLoginError: "",
  qrKey: "",
  sessionDockSig: "",
  accountsSig: "",
  profilesSig: "",
  playgrounds: new Map(),
  playgroundAbort: null,
  wsConnection: null,
  debug: null,
  debugTab: "status",
  debugText: "",
  debugBusy: false,
  diagAbort: null,
  confirmResolve: null,
  addBusy: false,
  toastTimer: 0,
  booted: false,
  connectionKind: "pending",
  authenticated: false,
  authChecked: false,
  authRequired: true,
  authGeneration: 0,
  authLoggingOut: false,
  loginBusy: false,
  authStatusBusy: false,
  httpControllers: new Set(),
  schedulesInstalled: false,
  security: null,
  securityLoaded: false,
  securityLoading: false,
  securitySaving: false,
  securityError: "",
  securityAt: 0,
  securityDraft: null,
  securityDirty: false,
  securitySaveToken: 0,
};

class ApiError extends Error {
  constructor(message, status = 0, payload = null) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.payload = payload;
  }
}

function isAbort(error) {
  return error && (error.name === "AbortError" || error.code === 20);
}

function icon(name, className = "") {
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  if (className) svg.setAttribute("class", className);
  const use = document.createElementNS(NS, "use");
  use.setAttribute("href", "#" + name);
  svg.append(use);
  return svg;
}

function el(tag, className = "", text = undefined) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function svgEl(tag, attrs = {}) {
  const node = document.createElementNS(NS, tag);
  for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, String(value));
  return node;
}

function currentSession() {
  return app.sessions.find((session) => session.id === app.sessionID) || null;
}

function currentAccount() {
  return app.status?.account || currentSession()?.account || "";
}

function currentProtocols() {
  const value = app.status || currentSession() || {};
  return [value.opq ? "OPQ" : "", value.onebot ? "OneBot v11" : "", value.onebot12 ? "OneBot v12" : ""].filter(Boolean);
}

function accountSource() {
  return app.status || currentSession() || {};
}

function safeNumber(value) {
  if (value === null || value === undefined || value === "") return null;
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
}

function numberOrDash(value) {
  const number = safeNumber(value);
  return number === null ? "—" : number.toLocaleString("zh-CN");
}

function bytesLabel(value) {
  const number = safeNumber(value);
  if (number === null) return "—";
  if (number < 1024) return `${number} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let size = number;
  let unit = -1;
  do {
    size /= 1024;
    unit += 1;
  } while (size >= 1024 && unit < units.length - 1);
  return `${size >= 100 ? size.toFixed(0) : size.toFixed(1)} ${units[unit]}`;
}

function durationLabel(seconds) {
  const value = safeNumber(seconds);
  if (value === null) return "—";
  const total = Math.max(0, Math.floor(value));
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  const clock = [hours, minutes, secs].map((part) => String(part).padStart(2, "0")).join(":");
  return days > 0 ? `${days}天 ${clock}` : clock;
}

function clockLabel(value) {
  if (!value) return "—";
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

function dateTimeLabel(value) {
  if (!value) return "—";
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

function durationMsLabel(value) {
  const number = safeNumber(value);
  if (number === null) return "—";
  if (number < 1000) return `${Math.round(number)} ms`;
  return `${(number / 1000).toFixed(number < 10000 ? 2 : 1)} s`;
}

function phaseInfo(session, options = {}) {
  const phase = session?.phase || "idle";
  const base = PHASES[phase] || { label: phase || "未知", tone: "idle" };
  if (session?.online && (phase === "online" || !options.keepPhase)) {
    return { label: "在线", tone: "online" };
  }
  return base;
}

function setPhaseBadge(node, session, options = {}) {
  if (!node) return;
  const info = phaseInfo(session, options);
  node.className = "phase-badge tone-" + info.tone;
  node.textContent = options.label || info.label;
}

function setStatusDot(node, kind) {
  if (!node) return;
  node.className = "status-dot" + (kind ? " is-" + kind : "");
}

function sessionShort(id) {
  if (!id) return "—";
  return id.length > 12 ? `${id.slice(0, 8)}…${id.slice(-3)}` : id;
}

function sessionTitle(session) {
  if (!session) return "等待会话";
  return session.account ? `QQ ${session.account}` : "待扫码会话";
}

function truncateText(text, limit) {
  if (typeof text !== "string") return "";
  return text.length > limit ? `${text.slice(0, limit)}\n…（已截断）` : text;
}

function friendlyError(error, fallback = "请求失败") {
  if (isAbort(error)) return "请求已取消";
  if (error instanceof ApiError) return error.message || fallback;
  if (error && typeof error.message === "string" && error.message) return error.message;
  return fallback;
}

function authActive(generation) {
  return app.authenticated && !app.authLoggingOut && app.authGeneration === generation;
}

function showToast(message, tone = "info") {
  const node = $("toast");
  if (!node) return;
  window.clearTimeout(app.toastTimer);
  node.textContent = message;
  node.className = "toast" + (tone === "error" ? " is-error" : tone === "success" ? " is-success" : "");
  node.hidden = false;
  app.toastTimer = window.setTimeout(() => {
    node.hidden = true;
  }, 3600);
}

async function copyText(text, successMessage = "已复制到剪贴板") {
  const value = String(text ?? "").trim();
  if (!value || value === "—") {
    showToast("没有可复制的内容", "error");
    return false;
  }
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(value);
    } else {
      const helper = el("textarea", "clipboard-helper");
      helper.value = value;
      helper.setAttribute("readonly", "");
      document.body.append(helper);
      helper.select();
      const copied = document.execCommand("copy");
      helper.remove();
      if (!copied) throw new Error("copy failed");
    }
    showToast(successMessage, "success");
    return true;
  } catch {
    showToast("复制不可用，请手动选择文本", "error");
    return false;
  }
}

async function controlApi(path, options = {}) {
  const {
    method = "GET",
    body,
    session = app.sessionID,
    scoped = true,
    signal,
    timeout = 15000,
  } = options;

  if (!app.authenticated) throw new ApiError("需要先登录控制台", 401);
  const authGen = app.authGeneration;
  const url = new URL("/console/api/" + path, location.origin);
  if (scoped) {
    if (!session) throw new ApiError("没有可用会话", 0);
    url.searchParams.set("session", session);
  }

  const controller = new AbortController();
  app.httpControllers.add(controller);
  let timedOut = false;
  const timer = window.setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeout);
  const forwardAbort = () => controller.abort();
  if (signal) {
    if (signal.aborted) controller.abort();
    else signal.addEventListener("abort", forwardAbort, { once: true });
  }

  try {
    const response = await fetch(url, {
      method,
      cache: "no-store",
      credentials: "same-origin",
      signal: controller.signal,
      headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const text = await response.text();
    let payload = null;
    if (text) {
      try {
        payload = JSON.parse(text);
      } catch {
        if (response.ok) throw new ApiError("控制台返回了无法解析的响应", response.status);
      }
    }
    if (response.status === 401) {
      const message = payload && typeof payload.error === "string"
        ? payload.error
        : "控制台会话已失效，请重新登录";
      const error = new ApiError(message, 401, payload);
      if (app.authenticated && authGen === app.authGeneration) {
        lockConsole("控制台会话已失效，请重新输入控制台密钥。", { reason: "expired" });
      }
      throw error;
    }
    if (!response.ok) {
      const message = payload && typeof payload.error === "string" ? payload.error : `请求失败（HTTP ${response.status}）`;
      throw new ApiError(message, response.status, payload);
    }
    if (!app.authenticated || authGen !== app.authGeneration) {
      throw new ApiError("控制台已锁定", 401);
    }
    return payload;
  } catch (error) {
    if (error instanceof ApiError) throw error;
    if (isAbort(error)) {
      if (timedOut) throw new ApiError("本地控制台响应超时", 0);
      throw error;
    }
    throw new ApiError("无法连接本地控制台，请确认进程仍在运行", 0);
  } finally {
    window.clearTimeout(timer);
    app.httpControllers.delete(controller);
    if (signal) signal.removeEventListener("abort", forwardAbort);
  }
}

async function publicCapture(path, options = {}) {
  const {
    method = "GET",
    body,
    timeout = AUTH_TIMEOUT,
  } = options;
  const controller = new AbortController();
  let timedOut = false;
  const timer = window.setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeout);
  try {
    const response = await fetch(new URL("/console/api/" + path, location.origin), {
      method,
      cache: "no-store",
      credentials: "same-origin",
      signal: controller.signal,
      headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const text = await response.text();
    let payload = null;
    if (text) {
      try {
        payload = JSON.parse(text);
      } catch {
        if (response.ok) throw new ApiError("控制台返回了无法解析的响应", response.status);
      }
    }
    if (!response.ok) {
      const message = payload && typeof payload.error === "string" ? payload.error : `请求失败（HTTP ${response.status}）`;
      const error = new ApiError(message, response.status, payload);
      error.retryAfter = response.headers.get("Retry-After") || "";
      throw error;
    }
    return payload;
  } catch (error) {
    if (error instanceof ApiError) throw error;
    if (isAbort(error)) {
      if (timedOut) throw new ApiError("控制台认证请求超时", 0);
      throw error;
    }
    throw new ApiError("无法连接控制台认证接口", 0);
  } finally {
    window.clearTimeout(timer);
  }
}

function setAuthVisibility(mode, options = {}) {
  const gate = $("auth-gate");
  const shell = $("app-shell");
  if (!gate || !shell) return;
  gate.hidden = false;
  shell.hidden = true;
  shell.inert = true;
  shell.setAttribute("aria-hidden", "true");
  document.body.classList.add("is-auth-locked");
  if (mode === "error" || (options.reason && options.reason !== "logout")) {
    gate.dataset.gateStage = "unlock";
  } else if (options.reason === "logout" || !gate.dataset.gateStage || gate.dataset.gateStage === "showcase") {
    gate.dataset.gateStage = "hero";
  }
  const loading = $("auth-loading");
  const login = $("auth-login");
  const notice = $("auth-notice");
  if (notice) {
    const text = options.notice || "";
    notice.textContent = text;
    notice.hidden = !text;
  }
  if (mode === "login") {
    if (loading) loading.hidden = true;
    if (login) login.hidden = false;
    setAuthError("");
    const key = $("console-key");
    if (key) {
      key.disabled = false;
      key.removeAttribute("aria-invalid");
      if (gate.dataset.gateStage === "unlock") {
        window.requestAnimationFrame(() => key.focus());
      }
    }
    const submit = $("console-login-submit");
    if (submit) submit.disabled = false;
    if ($("console-login-submit-text")) $("console-login-submit-text").textContent = "登录控制台";
    return;
  }
  if (login) login.hidden = true;
  if (loading) loading.hidden = false;
  const spinner = $("auth-status-spinner");
  const title = $("auth-status-title");
  const copy = $("auth-status-copy");
  const error = $("auth-status-error");
  const retry = $("auth-retry");
  const failed = mode === "error";
  if (spinner) spinner.hidden = failed;
  if (title) title.textContent = failed ? "无法验证登录状态" : "正在确认登录状态";
  if (copy) {
    copy.textContent = failed
      ? "无法连接控制台认证接口。请确认进程仍在运行，然后重试。"
      : "仅检查控制台访问权限，不会读取账号或运行数据。";
  }
  if (error) {
    error.textContent = failed ? options.message || "认证状态读取失败" : "";
    error.hidden = !failed;
  }
  if (retry) retry.hidden = !failed;
}

function setAuthError(message) {
  const node = $("console-login-error");
  if (!node) return;
  node.textContent = message || "";
  node.hidden = !message;
  const key = $("console-key");
  if (key) {
    if (message) key.setAttribute("aria-invalid", "true");
    else key.removeAttribute("aria-invalid");
  }
}

function showLoginSurface(options = {}) {
  setAuthVisibility("login", options);
  setAuthError("");
}

function clearLoginBusy() {
  app.loginBusy = false;
  const submit = $("console-login-submit");
  if (submit) submit.disabled = false;
  if ($("console-login-submit-text")) $("console-login-submit-text").textContent = "登录控制台";
}

async function checkAuthStatus() {
  if (app.authStatusBusy) return;
  app.authStatusBusy = true;
  setAuthVisibility("loading");
  try {
    const status = await publicCapture("auth/status", { timeout: AUTH_TIMEOUT });
    if (!status || typeof status !== "object" || typeof status.required !== "boolean" || typeof status.authenticated !== "boolean") {
      throw new ApiError("认证状态响应格式不正确", 200);
    }
    app.authChecked = true;
    app.authRequired = status.required;
    if (status.authenticated || !status.required) {
      unlockConsole({ initial: true });
    } else {
      showLoginSurface({ notice: "" });
    }
  } catch (error) {
    app.authChecked = false;
    setAuthVisibility("error", { message: friendlyError(error, "无法读取控制台登录状态") });
  } finally {
    app.authStatusBusy = false;
  }
}

async function submitConsoleLogin(event) {
  event.preventDefault();
  if (app.loginBusy) return;
  const input = $("console-key");
  const key = input ? input.value : "";
  if (key.length < SECRET_MIN_LENGTH) {
    setAuthError(`控制台密钥至少需要 ${SECRET_MIN_LENGTH} 个字符`);
    if (input) input.focus();
    return;
  }
  if (key !== key.trim()) {
    setAuthError("控制台密钥首尾不能包含空格");
    if (input) input.focus();
    return;
  }
  app.loginBusy = true;
  setAuthError("");
  const submit = $("console-login-submit");
  if (submit) submit.disabled = true;
  if ($("console-login-submit-text")) $("console-login-submit-text").textContent = "正在登录…";
  try {
    const value = await publicCapture("auth/login", {
      method: "POST",
      body: { key },
      timeout: AUTH_TIMEOUT,
    });
    if (!value || value.authenticated !== true) {
      throw new ApiError("认证未成功，请重试", 401);
    }
    if (input) input.value = "";
    app.authChecked = true;
    app.authRequired = true;
    unlockConsole({ initial: false, fromLogin: true });
  } catch (error) {
    if (input) {
      input.focus();
      input.select();
    }
    if (error instanceof ApiError && error.status === 401) {
      setAuthError("控制台密钥不正确，请重新输入。");
    } else if (error instanceof ApiError && error.status === 429) {
      setAuthError("登录尝试次数过多，请稍后再试。");
    } else {
      setAuthError(friendlyError(error, "登录失败，请稍后重试"));
    }
  } finally {
    clearLoginBusy();
  }
}

function resetPrivateState() {
  resetOneBotSettings();resetOneBot12Settings();
  app.sessionID = "";
  app.sessions = [];
  app.profiles = [];
  app.status = null;
  app.statusError = "";
  app.profilesError = "";
  app.transports = null;
  app.transportsError = "";
  app.transportsAt = 0;
  app.transportsStale = false;
  app.signer = null;
  app.signerFetchError = "";
  app.signerAt = 0;
  app.signerBusy = false;
  app.lastSync = 0;
  app.polling = false;
  app.pollSeq += 1;
  app.pollAbort = null;
  app.epoch += 1;
  app.sessionMeta.clear();
  app.logs.clear();
  app.logsPaused = false;
  app.logLevel = "ALL";
  app.logSearch = "";
  app.drafts.clear();
  app.history.clear();
  app.transportHistory.clear();
  app.settingsBusy = false;
  app.settingsError = "";
  app.settingsSaveToken += 1;
  app.localLoginError = "";
  app.qrKey = "";
  app.sessionDockSig = "";
  app.accountsSig = "";
  app.profilesSig = "";
  app.playgrounds.clear();
  app.playgroundAbort = null;
  app.wsConnection = null;
  app.debug = null;
  app.debugTab = "status";
  app.debugText = "";
  app.debugBusy = false;
  app.diagAbort = null;
  app.addBusy = false;
  app.security = null;
  app.securityLoaded = false;
  app.securityLoading = false;
  app.securitySaving = false;
  app.securityError = "";
  app.securityAt = 0;
  app.securityDraft = null;
  app.securityDirty = false;
  app.securitySaveToken += 1;
  app.httpControllers.clear();
  app.securityDraft = null;
  if (app.confirmResolve) settleConfirm(false);
  for (const id of ["add-account-dialog", "confirm-dialog"]) {
    const dialog = $(id);
    if (dialog && dialog.open) dialog.close();
  }
  const url = new URL(location.href);
  url.searchParams.delete("session");
  history.replaceState(null, "", url);
  const key = $("console-key");
  if (key) key.value = "";
  resetPasswordReveal("toggle-console-key", "console-key");
  const securitySecrets = ["security-console-key", "security-http-token", "security-ws-token"];
  for (const id of securitySecrets) {
    const node = $(id);
    if (node) {
      node.value = "";
      node.type = "password";
      node.disabled = false;
    }
  }
  for (const id of ["security-console-key", "security-http-token", "security-ws-token"]) {
    const toggle = $("toggle-" + id);
    if (toggle) {
      toggle.setAttribute("aria-pressed", "false");
      toggle.setAttribute("aria-label", toggle.getAttribute("aria-label")?.replace("隐藏", "显示") || "显示凭据");
      toggle.replaceChildren(icon("i-eye"));
    }
  }
  for (const id of ["qq-account", "new-account", "api-body", "log-search"]) {
    const node = $(id);
    if (node) node.value = "";
  }
  const saved = $("saved-accounts");
  if (saved) saved.replaceChildren();
  for (const id of ["sessions-grid", "profiles-grid"]) {
    const node = $(id);
    if (node) node.replaceChildren();
  }
  $$(".log-filter").forEach((button) => {
    const selected = button.dataset.level === "ALL";
    button.classList.toggle("selected", selected);
    button.setAttribute("aria-pressed", selected ? "true" : "false");
  });
  window.clearTimeout(app.toastTimer);
  const toast = $("toast");
  if (toast) toast.hidden = true;
}

function abortAllPrivateRequests() {
  for (const controller of app.httpControllers) {
    try {
      controller.abort();
    } catch {
      // ignore
    }
  }
  app.httpControllers.clear();
  if (app.pollAbort) {
    try {
      app.pollAbort.abort();
    } catch {
      // ignore
    }
  }
  if (app.diagAbort) {
    try {
      app.diagAbort.abort();
    } catch {
      // ignore
    }
  }
  if (app.playgroundAbort) {
    try {
      app.playgroundAbort.abort();
    } catch {
      // ignore
    }
  }
}

function lockConsole(message, options = {}) {
  if (!app.authenticated && options.reason !== "logout" && options.reason !== "reauth") return;
  app.authenticated = false;
  app.booted = false;
  app.authLoggingOut = false;
  app.authGeneration += 1;
  app.epoch += 1;
  app.pollSeq += 1;
  closeMobileNav();
  if (app.wsConnection) {
    try {
      app.wsConnection.intentional = true;
      app.wsConnection.socket.close(1000, "console locked");
    } catch {
      // ignore close failures while locking
    }
  }
  abortAllPrivateRequests();
  resetPrivateState();
  renderInitialState();
  setConnection("pending", "等待登录");
  const shell = $("app-shell");
  if (shell) {
    shell.hidden = true;
    shell.inert = true;
    shell.setAttribute("aria-hidden", "true");
  }
  setAuthVisibility("login", {
    reason: options.reason || "",
    notice: message || (app.authRequired
      ? "控制台会话已失效，请重新输入控制台密钥。"
      : "控制台已锁定。"),
  });
  if (app.authRequired) {
    const logout = $("logout-button");
    if (logout) logout.hidden = false;
  }
}

function unlockConsole(options = {}) {
  const shell = $("app-shell");
  const gate = $("auth-gate");
  if (!shell || !gate) return;
  app.authenticated = true;
  app.authChecked = true;
  app.authLoggingOut = false;
  app.authGeneration += 1;
  app.epoch += 1;
  app.booted = true;
  app.lastSync = 0;
  shell.hidden = false;
  shell.inert = false;
  shell.removeAttribute("aria-hidden");
  gate.hidden = true;
  gate.dataset.gateStage = "hero";
  document.body.classList.remove("is-auth-locked");
  closeMobileNav();
  const logout = $("logout-button");
  if (logout) {
    logout.hidden = !app.authRequired;
    logout.disabled = false;
  }
  setAuthError("");
  const input = $("console-key");
  if (input) input.value = "";
  resetPasswordReveal("toggle-console-key", "console-key");
  renderInitialState();
  setViewFromHash();
  const active = document.activeElement;
  if (active && typeof active.blur === "function" && gate.contains(active)) active.blur();
  bootstrap();
}

async function logoutConsole() {
  if (app.authLoggingOut) return;
  app.authLoggingOut = true;
  const button = $("logout-button");
  if (button) button.disabled = true;
  try {
    await controlApi("auth/logout", { method: "POST", body: {}, scoped: false, timeout: 8000 });
    lockConsole("已安全退出控制台。QQ 连接未受影响。", { reason: "logout" });
    showToast("已退出控制台，QQ 连接未受影响", "success");
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      lockConsole("控制台会话已失效，请重新输入控制台密钥。", { reason: "expired" });
      return;
    }
    app.authLoggingOut = false;
    if (button) button.disabled = false;
    showToast(friendlyError(error, "退出控制台失败，请重试"), "error");
  }
}

async function consoleSessionLost() {
  if (!app.authenticated) return false;
  try {
    const status = await publicCapture("auth/status", { timeout: 5000 });
    if (status && status.required === true && status.authenticated === false) {
      lockConsole("控制台会话已失效，请重新输入控制台密钥。", { reason: "expired" });
      return true;
    }
  } catch {
    // Treat authentication status lookup failures as inconclusive.
  }
  return false;
}

async function fetchSessions(signal) {
  const value = await controlApi("sessions", { scoped: false, signal, timeout: 10000 });
  if (!Array.isArray(value)) throw new ApiError("会话列表格式不正确", 200);
  return value;
}

async function fetchProfiles(signal) {
  const value = await controlApi("accounts", { scoped: false, signal, timeout: 10000 });
  if (!Array.isArray(value)) throw new ApiError("设备档案格式不正确", 200);
  return value;
}

function syncUrl() {
  const url = new URL(location.href);
  if (app.sessionID) url.searchParams.set("session", app.sessionID);
  else url.searchParams.delete("session");
  url.hash = app.view;
  history.replaceState(null, "", url);
}

function setConnection(kind, text) {
  app.connectionKind = kind;
  const node = $("connection");
  if (node) {
    node.className = "conn-state is-" + kind;
    setStatusDot(node.querySelector(".status-dot"), kind === "ok" ? "online" : kind === "warn" ? "busy" : kind === "offline" ? "error" : "warn");
  }
  $("connection-text").textContent = text;
  const sidebarText = {
    ok: "已连接",
    warn: "部分可用",
    offline: "连接中断",
    pending: "连接中",
  }[kind] || "连接中";
  $("sidebar-core-text").textContent = sidebarText;
  setStatusDot($("sidebar-core-dot"), kind === "ok" ? "online" : kind === "warn" ? "busy" : kind === "offline" ? "error" : "warn");
  setStatusDot($("service-dot"), kind === "ok" ? "online" : kind === "warn" ? "busy" : kind === "offline" ? "error" : "warn");
}

function showStale(message) {
  $("stale-text").textContent = message;
  $("stale-banner").hidden = false;
}

function hideStale() {
  $("stale-banner").hidden = true;
}

function updateSyncLabel() {
  const node = $("last-sync");
  if (!node) return;
  node.textContent = app.lastSync ? `最后同步 ${clockLabel(app.lastSync)}` : "等待同步";
}

function tickClock() {
  const now = new Date();
  const clock = $("clock");
  const today = $("today");
  if (clock) clock.textContent = clockLabel(now);
  if (today) {
    today.textContent = new Intl.DateTimeFormat("zh-CN", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    })
      .format(now)
      .replaceAll("/", ".");
  }
  updateQrCountdown();
}

function setView(view, options = {}) {
  const next = Object.hasOwn(VIEW_META, view) ? view : "overview";
  app.view = next;
  document.body.dataset.currentView = next;
  $$("[data-views]").forEach((section) => {
    section.hidden = !section.dataset.views.split(/\s+/).includes(next);
  });
  $$("[data-nav]").forEach((node) => {
    const active = node.dataset.nav === next;
    if (node.classList.contains("nav-item")) {
      node.classList.toggle("active", active);
      if (active) node.setAttribute("aria-current", "page");
      else node.removeAttribute("aria-current");
    }
  });
  const meta = VIEW_META[next];
  $("crumb").textContent = meta.crumb;
  $("page-title").textContent = meta.title;
  $("page-subtitle").textContent = meta.subtitle;
  document.dispatchEvent(new CustomEvent("openopq:view", { detail: { view: next } }));

  if (options.updateUrl !== false) syncUrl();
  if (!app.booted) return;

  if (next === "accounts") renderAccounts({ force: true });
  if (next === "settings") {
    loadOneBotSettings();loadOneBot12Settings();
    renderSettings({ force: true });
    renderSecurity();
    if (!app.transports && !app.transportsError) poll();
    if (!app.security && !app.securityLoading && !app.securityError) loadSecurity({ silent: true });
  }
  if (next === "api") renderPlayground();
  if (next === "logs") paintLogs(true);
  if (next === "debug" && (!app.debug || app.debug.sessionID !== app.sessionID) && !app.debugBusy) {
    refreshDiagnostics();
  }
  closeMobileNav();
  if (options.scroll !== false) window.scrollTo(0, 0);
}

function setViewFromHash() {
  const view = location.hash.replace(/^#/, "");
  setView(Object.hasOwn(VIEW_META, view) ? view : "overview", { updateUrl: false, scroll: false });
}

function bindNavigation() {
  $$("[data-nav]").forEach((node) => {
    node.addEventListener("click", () => {
      setView(node.dataset.nav);
      closeMobileNav();
    });
  });
  $("header-settings-button").addEventListener("click", () => setView("settings"));
  window.addEventListener("hashchange", () => {
    const view = location.hash.replace(/^#/, "");
    if (!Object.hasOwn(VIEW_META, view) || view === app.view) return;
    setView(view, { updateUrl: false });
  });
}

function openMobileNav() {
  const sidebar = $("sidebar");
  const backdrop = $("mobile-nav-backdrop");
  const toggle = $("mobile-nav-toggle");
  if (!sidebar || window.innerWidth > 860) return;
  sidebar.classList.add("is-open");
  if (backdrop) backdrop.hidden = false;
  if (toggle) toggle.setAttribute("aria-expanded", "true");
  document.body.classList.add("mobile-nav-open");
}

function closeMobileNav() {
  const sidebar = $("sidebar");
  const backdrop = $("mobile-nav-backdrop");
  const toggle = $("mobile-nav-toggle");
  if (sidebar) sidebar.classList.remove("is-open");
  if (backdrop) backdrop.hidden = true;
  if (toggle) toggle.setAttribute("aria-expanded", "false");
  document.body.classList.remove("mobile-nav-open");
}

function bindMobileNavigation() {
  const toggle = $("mobile-nav-toggle");
  const close = $("mobile-nav-close");
  const backdrop = $("mobile-nav-backdrop");
  if (toggle) {
    toggle.addEventListener("click", () => {
      const sidebar = $("sidebar");
      if (sidebar?.classList.contains("is-open")) closeMobileNav();
      else openMobileNav();
    });
  }
  if (close) close.addEventListener("click", closeMobileNav);
  if (backdrop) backdrop.addEventListener("click", closeMobileNav);
  window.addEventListener("keydown", (event) => {
    if (event.key === "Escape") closeMobileNav();
  });
  window.addEventListener("resize", () => {
    if (window.innerWidth > 860) closeMobileNav();
  });
}

function bindAuthEvents() {
  $("console-login-form").addEventListener("submit", submitConsoleLogin);
  $("auth-retry").addEventListener("click", () => checkAuthStatus());
  bindPasswordReveal("toggle-console-key", "console-key");
  const logout = $("logout-button");
  if (logout) logout.addEventListener("click", logoutConsole);
  const key = $("console-key");
  if (key) {
    key.addEventListener("input", () => {
      if (key.getAttribute("aria-invalid")) {
        key.removeAttribute("aria-invalid");
        setAuthError("");
      }
    });
  }
  renderSecurity();
}

function bindCopyButtons() {
  $("copy-session").addEventListener("click", () => copyText(app.sessionID, "会话 ID 已复制"));
  $("copy-guid").addEventListener("click", () => copyText(app.status?.guid || currentSession()?.guid || "", "设备 GUID 已复制"));
  $("copy-settings-guid").addEventListener("click", () => copyText(app.status?.guid || currentSession()?.guid || "", "设备 GUID 已复制"));
  $("copy-command").addEventListener("click", () => copyText($("headless-command").textContent.replace(/\n\s+/g, " "), "无头启动命令已复制"));
  $("copy-request-url").addEventListener("click", () => {
    const target = getPlaygroundTarget();
    copyText(target.actualURL, "实际调用地址已复制");
  });
  $("copy-api-endpoint").addEventListener("click", () => {
    const target = getPlaygroundTarget();
    copyText(target.externalURL, "外部端点已复制");
  });
  $("copy-ws-external").addEventListener("click", () => {
    const state = getPlaygroundState();
    const target = getWsTarget(state.wsEndpoint || "opq_ws");
    copyText(target.externalURL, "外部 WebSocket 地址已复制（不含 Token）");
  });
  $("copy-response").addEventListener("click", () => {
    const state = getPlaygroundState();
    copyText(state?.result?.responseText || "", "响应已复制");
  });
  $("copy-debug-json").addEventListener("click", () => copyText(app.debugText, "调试 JSON 已复制"));
}

function bindHeaderActions() {
  $$('[data-action="add-account"]').forEach((node) => node.addEventListener("click", openAddAccountDialog));
  $("account-settings-button").addEventListener("click", () => setView("settings"));
  $("account-debug-button").addEventListener("click", () => setView("debug"));
  $("account-logs-button").addEventListener("click", () => setView("logs"));
  $("recent-open-logs").addEventListener("click", () => setView("logs"));
  $("stale-retry").addEventListener("click", () => {
    poll();
    fetchSigner();
  });
  $("signer-refresh").addEventListener("click", () => fetchSigner());
}

function openAddAccountDialog() {
  $("add-account-error").hidden = true;
  const input = $("new-account");
  input.value = "";
  $("add-account-dialog").showModal();
  window.requestAnimationFrame(() => input.focus());
}

async function addSessionFromDialog(event) {
  event.preventDefault();
  if (app.addBusy) return;
  const account = $("new-account").value.trim();
  if (account && !validQq(account)) {
    showAddError("请输入有效的 QQ 账号（1–4294967295）");
    return;
  }
  const authGen = app.authGeneration;
  app.addBusy = true;
  $("add-session-submit").disabled = true;
  $("add-session-submit").querySelector("span").textContent = "正在创建…";
  showAddError("");
  try {
    const created = await controlApi("sessions", {
      method: "POST",
      body: { account },
      scoped: false,
      timeout: 12000,
    });
    if (!authActive(authGen)) return;
    if (!created || !created.id) throw new ApiError("控制台没有返回会话 ID", 200);
    let list = app.sessions;
    try {
      list = await fetchSessions();
    } catch {
      // The new session id is authoritative; the next poll will reconcile the list.
    }
    if (!authActive(authGen)) return;
    applySessions(list.length ? list : app.sessions, { allowSelect: false });
    $("add-account-dialog").close();
    selectSession(created.id, { prefill: account });
    setView("overview");
    showToast("会话已创建，登录仍需要你手动获取二维码", "success");
  } catch (error) {
    if (authActive(authGen)) showAddError(friendlyError(error, "创建会话失败"));
  } finally {
    app.addBusy = false;
    $("add-session-submit").disabled = false;
    $("add-session-submit").querySelector("span").textContent = "创建会话";
  }
}

function showAddError(message) {
  $("add-account-error").textContent = message;
  $("add-account-error").hidden = !message;
}

function validQq(value) {
  if (!/^\d+$/.test(value)) return false;
  const number = Number(value);
  return Number.isSafeInteger(number) && number >= 1 && number <= 4294967295;
}

function openConfirmDialog(options) {
  const dialog = $("confirm-dialog");
  if (app.confirmResolve) settleConfirm(false);
  $("confirm-title").textContent = options.title || "确认操作";
  $("confirm-message").textContent = options.message || "";
  $("confirm-accept").textContent = options.confirmLabel || "确认";
  $("confirm-accept").className = "button " + (options.danger ? "danger-ghost" : "primary");
  dialog.showModal();
  window.requestAnimationFrame(() => {
    if (options.danger) $("confirm-cancel").focus();
    else $("confirm-accept").focus();
  });
  return new Promise((resolve) => {
    app.confirmResolve = resolve;
  });
}

function settleConfirm(value) {
  const resolve = app.confirmResolve;
  app.confirmResolve = null;
  const dialog = $("confirm-dialog");
  if (dialog.open) dialog.close();
  if (resolve) resolve(Boolean(value));
}

function bindConfirmDialog() {
  $("confirm-accept").addEventListener("click", () => settleConfirm(true));
  $("confirm-cancel").addEventListener("click", () => settleConfirm(false));
  $("confirm-close").addEventListener("click", () => settleConfirm(false));
  $("confirm-dialog").addEventListener("cancel", (event) => {
    event.preventDefault();
    settleConfirm(false);
  });
  $("confirm-dialog").addEventListener("close", () => {
    if (app.confirmResolve) settleConfirm(false);
  });
}

function bindAddDialog() {
  $("close-add-dialog").addEventListener("click", () => $("add-account-dialog").close());
  $("cancel-add-dialog").addEventListener("click", () => $("add-account-dialog").close());
  $("add-account-form").addEventListener("submit", addSessionFromDialog);
  $("add-account-dialog").addEventListener("close", () => {
    showAddError("");
    app.addBusy = false;
    $("add-session-submit").disabled = false;
    $("add-session-submit").querySelector("span").textContent = "创建会话";
  });
}

async function openAccountSession(account) {
  if (!account) return;
  const authGen = app.authGeneration;
  try {
    const created = await controlApi("sessions", {
      method: "POST",
      body: { account },
      scoped: false,
      timeout: 12000,
    });
    if (!authActive(authGen)) return;
    if (!created || !created.id) throw new ApiError("控制台没有返回会话 ID", 200);
    try {
      const list = await fetchSessions();
      if (!authActive(authGen)) return;
      applySessions(list, { allowSelect: false });
    } catch {
      // Keep the current list; polling reconciles it.
    }
    if (!authActive(authGen)) return;
    selectSession(created.id, { prefill: account });
    setView("overview");
    showToast("已选择会话，尚未发起登录", "success");
  } catch (error) {
    if (authActive(authGen)) showToast(friendlyError(error, "无法打开会话"), "error");
  }
}

function applySessions(list, options = {}) {
  const previousSessions = app.sessions;
  app.sessions = Array.isArray(list) ? list.slice() : [];
  const seen = new Set();
  for (const session of app.sessions) {
    if (!session || !session.id) continue;
    seen.add(session.id);
    const prior = app.sessionMeta.get(session.id);
    if (prior && prior.started_at && session.started_at && prior.started_at !== session.started_at) {
      resetLogState(session.id);
    }
    app.sessionMeta.set(session.id, { started_at: session.started_at || "" });
  }
  for (const id of [...app.sessionMeta.keys()]) {
    if (!seen.has(id) && !app.logs.has(id)) app.sessionMeta.delete(id);
  }

  renderSessionDock();
  renderAccountCounts();
  if (app.view === "accounts") renderAccounts({ force: true });

  if (!app.sessionID || !app.sessions.some((session) => session.id === app.sessionID)) {
    const next = chooseSession(app.sessions);
    if (next) {
      selectSession(next, { updateUrl: true, fetchNow: false });
    } else if (app.sessionID) {
      clearSessionSelection();
    } else {
      renderStatus();
      renderLoginSurface();
      renderSettings({ force: true });
      renderPlayground();
    }
  } else if (options.refreshStatus !== false) {
    renderSessionDock();
  }
  if (previousSessions.length !== app.sessions.length) updateDatalist();
  return true;
}

function clearSessionSelection() {
  closePlaygroundSocket("会话已移除", true);
  abortPlaygroundRequest();
  abortDiagnostics();
  app.epoch += 1;
  app.pollSeq += 1;
  if (app.pollAbort) {
    try {
      app.pollAbort.abort();
    } catch {
      // ignore
    }
  }
  app.polling = false;
  app.pollAbort = null;
  app.sessionID = "";
  app.status = null;
  app.statusError = "";
  app.transports = null;
  app.transportsError = "";
  app.transportsAt = 0;
  app.transportsStale = false;
  app.localLoginError = "";
  app.qrKey = "";
  app.settingsBusy = false;
  app.settingsSaveToken += 1;
  app.debug = null;
  app.debugText = "";
  app.debugBusy = false;
  app.sessionDockSig = "";
  syncUrl();
  $("qq-account").value = "";
  renderSessionDock();
  renderStatus();
  renderLoginSurface();
  renderSettings({ force: true });
  paintLogs(true);
  renderPlayground();
  renderDiagnostics();
}

function chooseSession(list) {
  const wanted = new URL(location.href).searchParams.get("session");
  if (wanted && list.some((session) => session.id === wanted)) return wanted;
  const online = list.find((session) => session.online);
  if (online) return online.id;
  const named = list.find((session) => session.account);
  if (named) return named.id;
  return list[0]?.id || "";
}

function selectSession(id, options = {}) {
  if (!id) return;
  if (id === app.sessionID) {
    if (options.updateUrl !== false) syncUrl();
    return;
  }

  closePlaygroundSocket("会话已切换", true);
  abortPlaygroundRequest();
  abortDiagnostics();

  app.epoch += 1;
  app.pollSeq += 1;
  if (app.pollAbort) {
    try {
      app.pollAbort.abort();
    } catch {
      // ignore
    }
  }
  app.polling = false;
  app.pollAbort = null;
  app.sessionID = id;
  renderOneBotSettings();renderOneBot12Settings(true);
  if(app.view==="settings") {loadOneBotSettings();loadOneBot12Settings();}
  app.status = null;
  app.statusError = "";
  app.transports = null;
  app.transportsError = "";
  app.transportsAt = 0;
  app.transportsStale = false;
  app.localLoginError = "";
  app.qrKey = "";
  app.debug = null;
  app.debugText = "";
  app.debugBusy = false;
  app.settingsError = "";
  app.settingsBusy = false;
  app.settingsSaveToken += 1;
  app.sessionDockSig = "";
  app.accountsSig = "";
  app.profilesSig = "";

  const session = currentSession();
  if (options.prefill !== undefined && options.prefill) $("qq-account").value = String(options.prefill);
  else $("qq-account").value = session?.account || "";

  if (options.updateUrl !== false) syncUrl();
  renderSessionDock();
  renderStatus();
  renderLoginSurface();
  renderSettings({ force: true });
  paintLogs(true);
  renderPlayground();
  renderDiagnostics();
  if (app.view === "debug" && app.booted) refreshDiagnostics();
  if (options.fetchNow !== false) poll();
}

function renderSessionDock() {
  const select = $("session-select");
  const chips = $("session-chips");
  const online = app.sessions.filter((session) => session.online).length;
  $("online-count").textContent = `${online} / ${app.sessions.length} 在线`;

  const signature = JSON.stringify(
    app.sessions.map((session) => [
      session.id,
      session.account || "",
      session.phase || "",
      Boolean(session.online),
      Boolean(session.external),
      session.nickname || "",
    ]),
  );
  const idsSignature = app.sessions.map((session) => session.id).join("|");
  const idsChanged = select.dataset.ids !== idsSignature;
  if (signature === app.sessionDockSig && !idsChanged) return;
  app.sessionDockSig = signature;
  select.dataset.ids = idsSignature;

  if (idsChanged) {
    if (!app.sessions.length) {
      const placeholder = new Option("暂无会话", "");
      placeholder.disabled = true;
      placeholder.selected = true;
      select.replaceChildren(placeholder);
    } else {
      const options = app.sessions.map((session) => {
        const phase = PHASES[session.phase]?.label || session.phase || "未知";
        const option = new Option(`${sessionTitle(session)} · ${phase}`, session.id);
        option.selected = session.id === app.sessionID;
        return option;
      });
      select.replaceChildren(...options);
    }
  } else {
    app.sessions.forEach((session) => {
      const option = Array.from(select.options).find((item) => item.value === session.id);
      if (!option) return;
      const phase = PHASES[session.phase]?.label || session.phase || "未知";
      option.textContent = `${sessionTitle(session)} · ${phase}`;
    });
  }
  if (app.sessions.some((session) => session.id === app.sessionID)) select.value = app.sessionID;

  const fragment = document.createDocumentFragment();
  for (const session of app.sessions) {
    const wrapper = el("span", "session-chip-wrap");
    wrapper.setAttribute("role", "listitem");
    const button = el("button", "session-chip" + (session.id === app.sessionID ? " selected" : "") + (session.online ? " is-online" : ""));
    button.type = "button";
    button.dataset.sessionId = session.id;
    button.setAttribute("aria-pressed", session.id === app.sessionID ? "true" : "false");
    button.setAttribute("aria-label", `${sessionTitle(session)}，${PHASES[session.phase]?.label || session.phase || "未知"}`);
    button.append(el("span", "chip-dot"));
    button.append(el("strong", "", session.account ? `QQ ${session.account}` : "待扫码"));
    button.append(el("span", "chip-phase", PHASES[session.phase]?.label || session.phase || "未知"));
    button.append(el("span", "chip-session", sessionShort(session.id)));
    wrapper.append(button);
    fragment.append(wrapper);
  }
  if (!app.sessions.length) {
    fragment.append(el("span", "empty-inline", "尚未创建会话。点击“添加 QQ 账号”开始。"));
  }
  chips.replaceChildren(fragment);
}

function bindSessionControls() {
  $("session-select").addEventListener("change", () => {
    const id = $("session-select").value;
    if (id && id !== app.sessionID) selectSession(id);
  });
  $("session-chips").addEventListener("click", (event) => {
    const button = event.target.closest("[data-session-id]");
    if (!button) return;
    if (button.dataset.sessionId !== app.sessionID) selectSession(button.dataset.sessionId);
  });
}

async function poll() {
  if (!app.authenticated || !app.booted || app.authLoggingOut || app.polling) return;
  const seq = ++app.pollSeq;
  const epoch = app.epoch;
  const authGen = app.authGeneration;
  const stillActive = () => app.authenticated && !app.authLoggingOut && app.authGeneration === authGen && epoch === app.epoch;
  const controller = new AbortController();
  app.polling = true;
  app.pollAbort = controller;
  const failures = [];
  let statusOK = false;

  try {
    let list;
    try {
      list = await fetchSessions(controller.signal);
    } catch (error) {
      if (isAbort(error) || !stillActive()) return;
      app.statusError = "无法读取会话列表";
      setConnection("offline", "连接中断");
      showStale("无法读取会话列表。当前显示的数据可能已过期。请确认本地控制台仍在运行。");
      renderStatus();
      return;
    }
    if (!stillActive()) return;
    applySessions(list);
    if (!stillActive()) return;

    if (!app.sessionID) {
      app.lastSync = Date.now();
      updateSyncLabel();
      setConnection("ok", "服务已连接");
      hideStale();
      renderStatus();
      renderSessionDock();
      return;
    }

    const sid = app.sessionID;

    try {
      const status = await controlApi("status", { session: sid, signal: controller.signal, timeout: 10000 });
      if (!stillActive() || app.sessionID !== sid) return;
      if (!status || typeof status !== "object") throw new ApiError("会话状态格式不正确", 200);
      applyStatus(status);
      statusOK = true;
    } catch (error) {
      if (isAbort(error) || !stillActive() || app.sessionID !== sid) return;
      failures.push("会话状态");
      app.statusError = friendlyError(error, "无法读取会话状态");
      renderStatus();
    }

    try {
      const state = getLogState(sid);
      const entries = await controlApi("logs?after=" + encodeURIComponent(state.lastID), {
        session: sid,
        signal: controller.signal,
        timeout: 10000,
      });
      if (!stillActive() || app.sessionID !== sid) return;
      if (!Array.isArray(entries)) throw new ApiError("日志格式不正确", 200);
      appendLogs(sid, entries);
    } catch (error) {
      if (isAbort(error) || !stillActive() || app.sessionID !== sid) return;
      failures.push("日志");
      const state = getLogState(sid);
      state.error = friendlyError(error, "无法读取日志");
      state.fetched = true;
      if (!app.logsPaused) paintLogs();
    }

    try {
      const profiles = await fetchProfiles(controller.signal);
      if (!stillActive()) return;
      app.profiles = profiles;
      app.profilesError = "";
      updateDatalist();
      if (app.view === "accounts") renderAccounts();
    } catch (error) {
      if (isAbort(error) || !stillActive()) return;
      failures.push("设备档案");
      app.profilesError = friendlyError(error, "无法读取设备档案");
      if (app.view === "accounts") renderAccounts({ force: true });
    }

    try {
      const snapshot = await controlApi("transports", {
        session: sid,
        signal: controller.signal,
        timeout: 10000,
      });
      if (!stillActive() || app.sessionID !== sid) return;
      const normalized = normalizeTransports(snapshot);
      if (!normalized) throw new ApiError("传输快照格式不正确", 200);
      applyTransportSnapshot(normalized, { origin: "poll" });
    } catch (error) {
      if (isAbort(error) || !stillActive() || app.sessionID !== sid) return;
      failures.push("传输快照");
      handleTransportsError(error);
    }

    if (!stillActive() || app.sessionID !== sid) return;
    app.lastSync = Date.now();
    updateSyncLabel();
    if (!statusOK && !app.statusError) app.statusError = "无法读取会话状态";
    if (statusOK) app.statusError = "";
    renderStatus();

    if (failures.length === 0) {
      setConnection("ok", "服务已连接");
      hideStale();
    } else {
      setConnection("warn", "部分数据不可用");
      showStale(`以下数据暂不可用：${failures.join("、")}。已保留最后一次真实数据，可能不是最新。`);
    }
  } catch (error) {
    if (!isAbort(error) && stillActive()) {
      setConnection("warn", "同步异常");
      showStale(`同步数据时发生异常：${friendlyError(error)}。页面会继续重试。`);
    }
  } finally {
    if (seq === app.pollSeq) {
      app.polling = false;
      app.pollAbort = null;
    }
  }
}

function applyStatus(status) {
  const previous = app.status;
  if (previous && previous.started_at && status.started_at && previous.started_at !== status.started_at) {
    resetLogState(app.sessionID);
  }
  const phaseChanged = !previous || previous.phase !== status.phase;
  app.status = status;
  app.statusError = "";
  if (phaseChanged && (status.online || BUSY_PHASES.has(status.phase))) app.localLoginError = "";
  const session = currentSession();
  if (session) {
    session.phase = status.phase;
    session.online = status.online;
    session.nickname = status.nickname;
    session.account = status.account || session.account;
    session.uptime = status.uptime;
    session.messages = status.messages;
    session.notices = status.notices;
    session.started_at = status.started_at || session.started_at;
    session.error = status.error || "";
    session.notice = status.notice || "";
    session.opq = status.opq;
    session.onebot = status.onebot;
  }
  renderSessionDock();
  renderStatus();
  if (app.view === "accounts") renderAccounts({ force: true });
}

function renderStatus() {
  const session = currentSession();
  const status = app.status;
  const source = status || session || {};
  const noSession = !app.sessionID;
  const stale = Boolean(app.statusError);
  const loading = !noSession && !status && !stale;

  let phaseLabel = "";
  let phaseTone = "idle";
  if (noSession) {
    phaseLabel = "等待会话";
    phaseTone = "idle";
  } else if (loading) {
    phaseLabel = "读取中";
    phaseTone = "busy";
  } else if (stale) {
    phaseLabel = "状态未知";
    phaseTone = "muted";
  } else {
    const info = phaseInfo(status);
    phaseLabel = info.label;
    phaseTone = info.tone;
  }

  if (noSession) {
    $("metric-status").textContent = "等待会话";
    $("metric-account").textContent = "添加或选择一个 QQ 会话以开始";
  } else if (loading) {
    $("metric-status").textContent = "读取中";
    $("metric-account").textContent = "正在读取当前会话状态";
  } else if (stale) {
    $("metric-status").textContent = "未知";
    $("metric-account").textContent = "无法读取当前会话状态";
  } else {
    $("metric-status").textContent = phaseLabel;
    $("metric-account").textContent = status.account
      ? `QQ ${status.account}${status.nickname ? " · " + status.nickname : ""}`
      : "当前会话尚未绑定 QQ 账号";
  }
  $("metric-phase").className = "phase-badge tone-" + phaseTone;
  $("metric-phase").textContent = phaseLabel;

  const events = safeNumber(status?.messages) !== null || safeNumber(status?.notices) !== null
    ? (safeNumber(status?.messages) || 0) + (safeNumber(status?.notices) || 0)
    : null;
  $("metric-events").textContent = events === null ? "—" : events.toLocaleString("zh-CN");
  $("event-detail").textContent = status
    ? `消息 ${numberOrDash(status.messages)} · 通知 ${numberOrDash(status.notices)}`
    : "等待会话状态";

  const account = status?.account || (stale ? "" : session?.account) || "";
  const avatar = $("account-avatar");
  avatar.textContent = account ? account.slice(-2) : "OP";
  $("account-name").textContent = account ? `QQ ${account}` : session ? "待扫码账号" : "等待会话";
  $("account-nick").textContent = stale
    ? "会话状态未知；下方数据可能来自上一次成功读取"
    : status?.nickname || (status?.online ? "已在线" : account ? "等待扫码确认昵称" : "从上方选择或添加一个 QQ 账号");
  $("account-phase").className = "phase-badge tone-" + phaseTone;
  $("account-phase").textContent = phaseLabel;
  $("account-external").hidden = !session?.external;
  $("account-current-badge").hidden = !session;
  $("account-session").textContent = app.sessionID || "—";
  $("account-uptime").textContent = status ? durationLabel(status.uptime) : "—";
  $("account-messages").textContent = status ? numberOrDash(status.messages) : "—";
  $("account-notices").textContent = status ? numberOrDash(status.notices) : "—";
  const protocols = currentProtocols();
  $("account-protocols").textContent = protocols.length ? protocols.join(" · ") : "未启用";
  $("account-started").textContent = status ? dateTimeLabel(status.started_at) : "—";
  const heartbeat = status?.heartbeat;
  $("account-heartbeat-success").textContent = heartbeat?.last_success_at ? dateTimeLabel(heartbeat.last_success_at) : "尚未确认";
  const heartbeatStages = {waiting:"等待首次心跳",started:"已触发",signing:"正在签名",signed:"已签名",framing:"正在封包",sending:"正在发送",sent:"已写入，等待 QQ 回复",reply:"已收到 QQ 回复",success:"QQ 已确认",failed:"失败"};
  $("account-heartbeat-stage").textContent = heartbeat ? `${heartbeat.stale ? "失活 · " : ""}${heartbeatStages[heartbeat.stage] || heartbeat.stage || "—"}${heartbeat.consecutive_failures ? ` · 连续失败 ${heartbeat.consecutive_failures} 次` : ""}` : "—";
  $("account-heartbeat-stage").title = heartbeat?.last_error || "";

  const alert = $("account-alert");
  const notice = $("account-notice");
  if (stale) {
    alert.textContent = `会话状态不可用：${app.statusError}`;
    alert.hidden = false;
  } else if (status?.error) {
    alert.textContent = status.error;
    alert.hidden = false;
  } else {
    alert.hidden = true;
  }
  if (status?.notice) {
    notice.textContent = status.notice;
    notice.hidden = false;
  } else {
    notice.hidden = true;
  }

  $("device-info").hidden = !status?.guid;
  $("device-guid").textContent = status?.guid || "";
  fillComputerName("device-computer-name", status?.computer_name);
  $("settings-device-info").hidden = !status?.guid;
  $("settings-guid").textContent = status?.guid || "";
  fillComputerName("settings-computer-name", status?.computer_name);
  $("sidebar-version").textContent = status?.version
    ? String(status.version).replace("QQNT-v", "QQ ")
    : "正在读取版本";

  const protocolText = protocols.length ? protocols.join(" · ") : "协议未启用";
  $("footer-protocols").textContent = protocols.length ? protocols.join(" + ") : "未启用";
  const heroBadge = $("overview-hero-badge");
  const heroSummary = $("overview-hero-summary");
  if (heroBadge) {
    const onlineTotal = app.sessions.filter((item) => item.online).length;
    heroBadge.textContent = onlineTotal > 0
      ? `GATEWAY ONLINE · ${onlineTotal}/${app.sessions.length} 会话在线`
      : `PROTOCOL GATEWAY · ${protocols.length ? protocols.join(" / ") : "LOCAL"}`;
  }
  if (heroSummary) {
    heroSummary.textContent = account
      ? `当前查看 QQ ${account}（${phaseLabel}），已启用 ${protocolText}，各协议事件与消息路由实时同步。`
      : `当前共有 ${app.sessions.length} 个独立会话槽位，已启用 ${protocolText}。创建或选择会话后即可扫码上线。`;
  }

  $("api-context-account").textContent = account ? `QQ ${account}` : "未绑定账号";
  $("api-context-session").textContent = app.sessionID || "—";
  $("api-context-protocol").textContent = getPlaygroundState().protocol === "opq" ? "OPQ" : "OneBot v11";
  $("settings-session-account").textContent = account ? `QQ ${account}` : session ? "待扫码账号" : "等待会话";
  $("settings-session-id").textContent = app.sessionID || "—";
  const settingsPhase = $("settings-session-state");
  settingsPhase.className = "phase-badge tone-" + phaseTone;
  settingsPhase.textContent = phaseLabel;
  $("settings-protocols").textContent = protocolText;
  $("logs-session-title").textContent = account ? `QQ ${account} · 会话日志` : "当前会话日志";
  renderMetricsTransport();
  renderLoginSurface();
  renderHeadless();
  renderSettingsContext();
  renderPlaygroundTargets();
  renderSigner();
}

function renderLoginSurface() {
  const status = app.status;
  const noSession = !app.sessionID;
  const stale = Boolean(app.statusError);
  const busy = !stale && Boolean(status && BUSY_PHASES.has(status.phase));
  const online = !stale && Boolean(status?.online);
  const recovering = Boolean(status?.reconnect?.reconnecting);
  const requiresLogin = Boolean(status?.reconnect?.requires_login);
  const loginState = $("login-state");
  let stateLabel = "未连接";
  let tone = "idle";
  if (noSession) {
    stateLabel = "等待会话";
    tone = "idle";
  } else if (stale) {
    stateLabel = "状态未知";
    tone = "muted";
  } else if (!status) {
    stateLabel = "读取中";
    tone = "busy";
  } else {
    const info = phaseInfo(status);
    stateLabel = info.label;
    tone = info.tone;
  }
  loginState.className = "phase-badge tone-" + tone;
  loginState.textContent = stateLabel;

  $("qq-account").disabled = noSession || busy || online || recovering;
  $("login-submit").disabled = noSession || busy || online || recovering || requiresLogin || stale || !status;
  const restoreAccount = restorableSavedAccount();
  $("login-submit-text").textContent = noSession
    ? "等待会话"
    : requiresLogin
      ? "请先断开旧会话"
      : recovering
        ? "自动重连中"
        : online
          ? "账号已在线"
          : busy
            ? "登录进行中"
            : stale
              ? "状态未知"
              : !status
                ? "正在读取状态"
                : restoreAccount
                  ? "恢复保存的登录"
                  : "获取登录二维码";
  $("cancel-login").hidden = !(busy || (stale && status && BUSY_PHASES.has(status.phase)));
  $("cancel-login").textContent = status?.phase === "restoring" ? "取消恢复" : "取消本次登录";
  const canDisconnect = online || recovering || Boolean(status?.reconnect?.requires_login || status?.reconnect?.last_error);
  $("disconnect").hidden = !canDisconnect;
  $("account-disconnect").hidden = !canDisconnect;
  const saved = status?.saved_login;
  const profileSaved = savedLoginForAccount($("qq-account")?.value.trim() || status?.account || "");
  const shownSaved = saved?.saved ? saved : profileSaved;
  $("resume-login").hidden = !shownSaved?.saved;
  $("resume-login").disabled = !shownSaved?.restorable || busy || recovering || online || stale;
  $("forget-login").hidden = !saved?.saved;
  $("forget-login").disabled = busy || stale;
  $("saved-login-info").hidden = !status || noSession;
  $("saved-login-info").textContent = shownSaved?.saved
    ? shownSaved.restorable ? `登录票据已加密保存在本机；${shownSaved.auto_resume ? "启动时自动恢复" : "已暂停自动恢复，可手动恢复"}。` : "本机记录不可恢复，请重新扫码或清除保存的登录。"
    : shownSaved?.reason === "disabled" ? "已关闭登录票据持久化。" : "首次扫码成功后将加密保存登录票据，下次启动无需再扫码。";
  $("saved-login-info").title = shownSaved?.reason || "";

  const emailError = status?.error || app.localLoginError || "";
  $("login-error").textContent = emailError;
  $("login-error").hidden = !emailError;
  $("login-notice").textContent = status?.notice || "";
  $("login-notice").hidden = !status?.notice;
  paintQR();
}

function paintQR() {
  const area = $("qr-area");
  if (!area) return;
  const status = app.status;
  const session = currentSession();
  let key = "";
  if (!app.sessionID) key = "no-session";
  else if (app.statusError) key = "stale";
  else if (!status) key = "loading";
  else if (status.online) key = "online";
  else if (status.reconnect?.reconnecting) key = "reconnect:" + status.reconnect.attempt;
  else if (status.qr) key = "qr:" + status.qr;
  else key = "phase:" + status.phase;

  if (key === app.qrKey) return;
  app.qrKey = key;

  if (!app.sessionID) {
    const box = el("div", "qr-placeholder");
    const glyph = el("span", "qr-glyph");
    glyph.append(icon("i-qr"));
    box.append(
      glyph,
      el("strong", "", "尚未选择会话"),
      el("p", "", "使用“添加 QQ 账号”创建会话后，才能请求登录二维码。"),
    );
    area.replaceChildren(box);
    return;
  }
  if (app.statusError) {
    const box = el("div", "qr-placeholder");
    const glyph = el("span", "qr-glyph");
    glyph.append(icon("i-warning"));
    box.append(glyph, el("strong", "", "会话状态暂不可用"), el("p", "", "无法确认二维码是否仍然有效。请等待本地控制台恢复后重试。"));
    area.replaceChildren(box);
    return;
  }
  if (!status) {
    const box = el("div", "qr-placeholder");
    const spinner = el("span", "qr-spinner");
    spinner.append(icon("i-loader"));
    box.append(spinner, el("strong", "", "正在读取会话"), el("p", "", "正在从本地控制台获取当前会话状态。"));
    area.replaceChildren(box);
    return;
  }
  if (status.online) {
    const box = el("div", "qr-online");
    const check = el("span", "qr-check");
    check.append(icon("i-check"));
    box.append(check, el("strong", "", "账号已连接"), el("p", "", `QQ ${status.account || session?.account || ""}`));
    area.replaceChildren(box);
    return;
  }
  if (status.reconnect?.reconnecting) {
    const box = el("div", "qr-placeholder");
    const spinner = el("span", "qr-spinner");
    spinner.append(icon("i-loader"));
    box.append(spinner, el("strong", "", `正在第 ${status.reconnect.attempt} 次重连`), el("p", "", "保留当前账号与登录票据，网络恢复后自动上线；可以点击断开连接停止重试。"));
    area.replaceChildren(box);
    return;
  }
  if (status.qr) {
    const decoded = String(status.qr);
    if (!decoded.startsWith("data:image/")) {
      const box = el("div", "qr-placeholder");
      box.append(el("strong", "", "二维码数据不可用"), el("p", "", "后端返回的二维码不是本地图片数据。"));
      area.replaceChildren(box);
      return;
    }
    const wrapper = el("div", "qr-wrapper");
    const image = el("img", "qr-image");
    image.src = decoded;
    image.alt = "QQ 登录二维码";
    const countdown = el("div", "qr-countdown");
    wrapper.append(image, countdown);
    area.replaceChildren(wrapper);
    updateQrCountdown();
    return;
  }
  if (BUSY_PHASES.has(status.phase)) {
    const box = el("div", "qr-placeholder");
    if (status.phase === "requesting") {
      const spinner = el("span", "qr-spinner");
      spinner.append(icon("i-loader"));
      box.append(spinner, el("strong", "", "正在获取二维码…"), el("p", "", "正在连接签名服务与 QQ 服务器。"));
    } else if (status.phase === "waiting") {
      const glyph = el("span", "qr-glyph");
      glyph.append(icon("i-qr"));
      box.append(glyph, el("strong", "", "二维码已过期"), el("p", "", "请取消本次登录后重新获取。不会自动刷新或重试。"));
    } else {
      const spinner = el("span", "qr-spinner");
      spinner.append(icon("i-loader"));
      box.append(spinner, el("strong", "", `${PHASES[status.phase]?.label || status.phase}…`), el("p", "", status.phase === "restoring" ? "正在使用本机保存的票据恢复，QQ 确认后才会显示在线；失败不会自动请求二维码。" : "扫码已确认，正在完成登录，请稍候。"));
    }
    area.replaceChildren(box);
    return;
  }
  if (status.phase === "error") {
    const box = el("div", "qr-placeholder");
    const glyph = el("span", "qr-glyph");
    glyph.append(icon("i-warning"));
    box.append(glyph, el("strong", "", "登录未完成"), el("p", "", "查看错误信息后重新获取二维码。控制台不会自动重试。"));
    area.replaceChildren(box);
    return;
  }
  if (status.phase === "cancelled") {
    const box = el("div", "qr-placeholder");
    const glyph = el("span", "qr-glyph");
    glyph.append(icon("i-info"));
    box.append(glyph, el("strong", "", "本次登录已取消"), el("p", "", "设备身份已保留。可随时重新获取二维码。"));
    area.replaceChildren(box);
    return;
  }
  const box = el("div", "qr-placeholder");
  const glyph = el("span", "qr-glyph");
  glyph.append(icon("i-qr"));
  box.append(
    glyph,
    el("strong", "", status.phase === "ready" ? "准备好连接了吗？" : "等待获取登录二维码"),
    el("p", "", "使用手机 QQ 扫码确认。二维码只会在你点击按钮后请求。"),
  );
  area.replaceChildren(box);
}

function updateQrCountdown() {
  const status = app.status;
  const node = $("qr-area")?.querySelector(".qr-countdown");
  if (!node || !status || !status.qr_expires) return;
  const expires = new Date(status.qr_expires).getTime();
  if (!Number.isFinite(expires)) return;
  const remain = Math.max(0, Math.ceil((expires - Date.now()) / 1000));
  if (remain > 0) {
    node.textContent = `使用手机 QQ 扫码 · ${remain}s 后过期`;
    node.classList.remove("is-expired");
  } else {
    node.textContent = "二维码已过期，请取消后重新获取";
    node.classList.add("is-expired");
  }
}

function fillComputerName(id, name) {
  const node = $(id);
  if (!node) return;
  if (name) {
    node.textContent = name;
    node.hidden = false;
  } else {
    node.textContent = "";
    node.hidden = true;
  }
}

function savedLoginForAccount(account) {
  if (!account) return null;
  const profile = (app.profiles || []).find((p) => p.account === account);
  return profile?.saved_login || null;
}

function restorableSavedAccount() {
  const status = app.status;
  const input = $("qq-account")?.value.trim() || "";
  const account = input || status?.account || "";
  if (status?.saved_login?.restorable && (!input || input === status.account)) {
    return account || status.account || "";
  }
  const saved = savedLoginForAccount(account);
  if (saved?.restorable) return account;
  return "";
}

async function submitLogin(event) {
  event.preventDefault();
  if (app.localLoginError) app.localLoginError = "";
  const status = app.status;
  if (!status) {
    app.localLoginError = "当前会话状态未知，暂不能发起登录";
    renderLoginSurface();
    return;
  }
  if (status.online || status.reconnect?.reconnecting || BUSY_PHASES.has(status.phase)) return;
  const input = $("qq-account").value.trim();
  if (input && !validQq(input)) {
    app.localLoginError = "请输入有效的 QQ 账号（1–4294967295）";
    renderLoginSurface();
    return;
  }
  const account = input || status.account || "";
  const restoreAccount = restorableSavedAccount();
  const authGen = app.authGeneration;
  $("login-submit").disabled = true;
  $("login-submit-text").textContent = "正在请求…";
  app.localLoginError = "";
  try {
    if (restoreAccount) {
      await controlApi("resume", {
        method: "POST",
        body: { account: restoreAccount },
        session: app.sessionID,
        timeout: 10000,
      });
      if (!authActive(authGen)) return;
      showToast("正在使用本机保存的登录恢复，无需扫码", "success");
    } else {
      await controlApi("login", {
        method: "POST",
        body: { account },
        session: app.sessionID,
        timeout: 15000,
      });
      if (!authActive(authGen)) return;
      showToast("已请求登录二维码；扫码与确认不会自动进行", "success");
    }
    await poll();
  } catch (error) {
    if (!authActive(authGen)) return;
    app.localLoginError = friendlyError(error, restoreAccount ? "恢复登录失败" : "无法发起登录");
    renderLoginSurface();
    showToast(app.localLoginError, "error");
  } finally {
    if (authActive(authGen)) renderLoginSurface();
  }
}

async function cancelLogin() {
  const authGen = app.authGeneration;
  try {
    await controlApi("cancel", { method: "POST", body: {}, session: app.sessionID, timeout: 10000 });
    if (!authActive(authGen)) return;
    showToast("已请求取消本次登录", "success");
    await poll();
  } catch (error) {
    if (authActive(authGen)) showToast(friendlyError(error, "取消登录失败"), "error");
  }
}

async function disconnectSession() {
  if (!app.sessionID) return;
  const account = currentAccount();
  const session = app.sessionID;
  const authGen = app.authGeneration;
  const confirmed = await openConfirmDialog({
    title: "断开当前会话？",
    message: `将断开 ${account ? "QQ " + account : "当前会话"} 的 QQ 连接。\n设备身份和已保存档案会保留，传输配置不会被删除；之后仍可重新扫码。\n\n不会影响其他账号。`,
    confirmLabel: "断开会话",
    danger: true,
  });
  if (!confirmed || !authActive(authGen) || app.sessionID !== session) return;
  try {
    await controlApi("disconnect", { method: "POST", body: {}, session, timeout: 15000 });
    if (!authActive(authGen)) return;
    showToast("会话已断开，设备身份已保留", "success");
    await poll();
  } catch (error) {
    if (authActive(authGen)) showToast(friendlyError(error, "断开会话失败"), "error");
  }
}

async function restoreSavedLogin() {
  if (!app.sessionID || !app.status?.saved_login?.restorable || BUSY_PHASES.has(app.status.phase)) return;
  const session = app.sessionID;
  const authGen = app.authGeneration;
  try {
    await controlApi("resume", { method: "POST", body: { account: currentAccount() }, session, timeout: 10000 });
    if (authActive(authGen)) await poll();
  } catch (error) {
    if (authActive(authGen)) showToast(friendlyError(error, "恢复登录失败"), "error");
  }
}

async function forgetSavedLogin() {
  const session = app.sessionID;
  const authGen = app.authGeneration;
  if (!session) return;
  const confirmed = await openConfirmDialog({ title: "清除保存的登录？", message: "将断开当前 QQ 并删除本机保存的登录票据，下次需要重新扫码。设备 GUID、账号档案和端口设置会保留。", confirmLabel: "断开并清除", danger: true });
  if (!confirmed || !authActive(authGen) || app.sessionID !== session) return;
  try {
    await controlApi("forget-login", { method: "POST", body: {}, session, timeout: 15000 });
    if (authActive(authGen)) { showToast("保存的登录已清除", "success"); await poll(); }
  } catch (error) {
    if (authActive(authGen)) showToast(friendlyError(error, "清除失败"), "error");
  }
}

async function fetchSigner() {
  if (!app.authenticated || !app.booted || app.authLoggingOut || app.signerBusy) return;
  const authGen = app.authGeneration;
  app.signerBusy = true;
  renderSigner();
  try {
    const value = await controlApi("signer", { scoped: false, timeout: 8000 });
    if (authGen !== app.authGeneration || !app.authenticated) return;
    if (!value || typeof value !== "object") throw new ApiError("签名健康响应格式不正确", 200);
    app.signer = value;
    app.signerAt = Date.now();
    app.signerFetchError = "";
  } catch (error) {
    if (authGen !== app.authGeneration || !app.authenticated) return;
    if (!isAbort(error)) {
      app.signerFetchError = friendlyError(error, "无法读取签名健康");
      app.signerAt = Date.now();
    }
  } finally {
    if (authGen === app.authGeneration) {
      app.signerBusy = false;
      renderSigner();
    }
  }
}

function renderSigner() {
  const value = app.signer;
  const error = app.signerFetchError || (!app.signerBusy && value && !value.ready ? value.error || "" : "");
  let state = "尚未检查";
  let detail = "等待第一次健康检查";
  let latency = "—";
  let unit = "";
  let orbClass = "signer-orb";
  let dotClass = "unknown";

  if (app.signerBusy) {
    state = "检查中…";
    detail = "正在读取签名服务健康接口";
    orbClass += " is-busy";
    dotClass = "busy";
  } else if (app.signerFetchError) {
    state = "未知";
    detail = app.signerFetchError;
    orbClass += " is-error";
    dotClass = "error";
  } else if (value) {
    if (value.ready) {
      state = "健康";
      detail = value.adapter ? `适配器 ${value.adapter} · 健康检查通过` : "签名服务健康检查通过";
      latency = safeNumber(value.latency_ms) === null ? "—" : String(Math.round(Number(value.latency_ms)));
      unit = latency === "—" ? "" : "ms";
      orbClass += " is-ready";
      dotClass = "online";
    } else {
      state = "未就绪";
      detail = value.error || "签名服务报告未就绪";
      orbClass += " is-error";
      dotClass = "error";
    }
  }

  $("metric-signer").textContent = latency === "—" && value?.ready ? "就绪" : latency;
  $("signer-unit").textContent = unit;
  $("signer-detail").textContent = detail;
  $("signer-dot").className = "status-dot is-" + dotClass;
  $("signer-state").textContent = state;
  $("signer-detail-small").textContent = detail;
  $("signer-orb").className = orbClass;
  $("signer-latency").textContent = latency;
  $("signer-latency-unit").textContent = unit;
  $("signer-adapter").textContent = value?.adapter || "—";
  $("signer-checked").textContent = app.signerAt ? clockLabel(app.signerAt) : "—";
  $("signer-error").textContent = error;
  $("signer-error").hidden = !error;
}

function renderHeadless() {
  const account = currentAccount();
  const command = [
    "openopq --config Config.both.json",
    `  --headless --quiet${account ? " --account " + account : ""}`,
    "  --login --data-dir ./data",
    "  --qr-file ./data/login-qr.png",
    "  --log-file ./data/openopq.jsonl",
  ].join("\n");
  const node = $("headless-command");
  if (node) node.textContent = command;
}

function setHealthCard(key, state, detail, tone) {
  const card = $("health-" + key);
  const stateNode = $("health-" + key + "-state");
  const detailNode = $("health-" + key + "-detail");
  const dot = $("health-" + key + "-dot");
  if (!card || !stateNode || !detailNode) return;
  stateNode.textContent = state;
  detailNode.textContent = detail || "—";
  card.className = "health-card" + (tone ? " is-" + tone : "");
  setStatusDot(dot, tone === "ok" ? "online" : tone === "error" ? "error" : tone === "busy" ? "busy" : "unknown");
}

function normalizeTransports(snapshot) {
  if (!snapshot || typeof snapshot !== "object" || !snapshot.services || typeof snapshot.services !== "object") {
    return null;
  }
  const services = {};
  for (const definition of SERVICE_DEFS) {
    const raw = snapshot.services[definition.key];
    if (!raw || typeof raw !== "object") {
      services[definition.key] = null;
      continue;
    }
    services[definition.key] = {
      enabled: Boolean(raw.enabled),
      port: safeNumber(raw.port) ?? 0,
      listening: Boolean(raw.listening),
      address: typeof raw.address === "string" ? raw.address : "",
      path: typeof raw.path === "string" ? raw.path : "",
      connections: safeNumber(raw.connections),
      active_requests: safeNumber(raw.active_requests),
      total_requests: safeNumber(raw.total_requests),
      error: typeof raw.error === "string" ? raw.error : "",
    };
  }
  return {
    session_id: typeof snapshot.session_id === "string" ? snapshot.session_id : "",
    scope: typeof snapshot.scope === "string" ? snapshot.scope : "gateway",
    persistent: Boolean(snapshot.persistent),
    services,
  };
}

function combineEndpoint(service) {
  if (!service) return "";
  const address = String(service.address || "").replace(/\/+$/, "");
  const path = String(service.path || "");
  if (!address) return path;
  if (!path) return address;
  return address + (path.startsWith("/") ? path : "/" + path);
}

function serviceRuntimeInfo(service) {
  if (!service) return { label: "未返回", className: "is-error", state: "未返回" };
  if (service.error) return { label: "异常", className: "is-error", state: "异常" };
  if (!service.enabled) return { label: "未启用", className: "is-disabled", state: "未启用" };
  if (service.listening) return { label: "监听中", className: "is-live", state: "监听中" };
  return { label: "未监听", className: "is-error", state: "已启用 · 未监听" };
}

function serviceMetricText(service, definition) {
  if (!service) return "—";
  if (definition.kind === "ws") return `${numberOrDash(service.connections)} 个连接`;
  return `${numberOrDash(service.active_requests)} 活动 · 累计 ${numberOrDash(service.total_requests)}`;
}

function sumKnown(values) {
  const numbers = values.filter((value) => typeof value === "number" && Number.isFinite(value));
  return numbers.length ? numbers.reduce((total, value) => total + value, 0) : null;
}

function applyTransportSnapshot(snapshot, options = {}) {
  if (!snapshot) return;
  if (snapshot.session_id && snapshot.session_id !== app.sessionID) return;
  const sid = app.sessionID;
  app.transports = snapshot;
  app.transportsError = "";
  app.transportsAt = Date.now();
  app.transportsStale = false;
  pushTransportSample(sid, snapshot);
  let draft = app.drafts.get(sid);
  if (options.origin === "save" || !draft || !draft.dirty) {
    draft = draftFromSnapshot(snapshot);
    app.drafts.set(sid, draft);
  } else if (draft) {
    draft.remoteChanged = !draftMatchesSnapshot(draft, snapshot);
  }
  if (options.origin === "save") app.settingsError = "";
  renderMetricsTransport();
  renderOverviewTransports();
  renderTransportChart();
  renderSettingsContext();
  renderPlaygroundTargets();
  if (app.view === "api") renderWs();
  if (app.view === "settings") {
    if (!draft?.dirty && !settingsContainsFocus()) renderTransportServiceCards(draft);
    else updateSettingsRuntimeNodes();
    updateSettingsDirtyUI();
  }
}

function handleTransportsError(error) {
  app.transportsStale = true;
  if (error instanceof ApiError && error.status === 404) {
    app.transportsError = "传输快照接口不可用（transports）。请确认后端版本与控制台静态资源一致。";
  } else {
    app.transportsError = friendlyError(error, "无法读取传输快照");
  }
  renderMetricsTransport();
  renderOverviewTransports();
  renderTransportChart();
  renderSettingsContext();
  renderPlaygroundTargets();
  if (app.view === "api") renderWs();
  if (app.view === "settings") {
    const draft = app.drafts.get(app.sessionID);
    if (draft) updateSettingsRuntimeNodes();
    updateSettingsDirtyUI();
  }
}

function pushTransportSample(sid, snapshot) {
  if (!sid || !snapshot) return;
  const wsValues = ["opq_ws", "onebot_ws", "onebot12_ws"].map((key) => snapshot.services[key]?.connections);
  const httpActive = ["opq_http", "onebot_http", "onebot12_http"].map((key) => snapshot.services[key]?.active_requests);
  const ws = sumKnown(wsValues);
  const active = sumKnown(httpActive);
  if (ws === null && active === null) return;
  const history = app.transportHistory.get(sid) || [];
  history.push({ at: Date.now(), ws, http: active });
  if (history.length > SAMPLE_LIMIT) history.splice(0, history.length - SAMPLE_LIMIT);
  app.transportHistory.set(sid, history);
}

function renderMetricsTransport() {
  const snapshot = app.transports;
  const valueNode = $("metric-ws");
  const detail = $("metric-ws-detail");
  const dot = $("metric-ws-dot");
  if (!snapshot || !snapshot.services) {
    valueNode.textContent = "—";
    detail.textContent = app.transportsError ? "传输快照不可用" : "等待传输快照";
    setStatusDot(dot, app.transportsError ? "error" : "unknown");
    return;
  }
  const opq = safeNumber(snapshot.services.opq_ws?.connections);
  const onebot = safeNumber(snapshot.services.onebot_ws?.connections);
  const total = sumKnown([opq, onebot]);
  valueNode.textContent = total === null ? "—" : total.toLocaleString("zh-CN");
  if (opq === null && onebot === null) {
    detail.textContent = "快照未返回连接计数";
    setStatusDot(dot, "unknown");
    return;
  }
  const stale = app.transportsStale || Boolean(app.transportsError);
  detail.textContent = stale
    ? `快照已过期 · OPQ ${opq ?? "—"} · OneBot ${onebot ?? "—"}`
    : `OPQ ${opq ?? "—"} · OneBot ${onebot ?? "—"}`;
  setStatusDot(dot, stale ? "warn" : total > 0 ? "online" : "unknown");
}

function renderOverviewTransports() {
  const list = $("overview-transport-list");
  const snapshot = app.transports;
  const updated = $("transport-overview-updated");
  if (updated) {
    updated.textContent = snapshot && app.transportsAt
      ? app.transportsStale || app.transportsError
        ? "快照已过期"
        : `快照 ${clockLabel(app.transportsAt)}`
      : "等待快照";
    updated.classList.toggle("is-live", Boolean(snapshot) && !app.transportsStale && !app.transportsError);
  }
  const scopeNote = $("transport-scope-note");
  if (scopeNote) {
    const persistence = snapshot?.persistent ? "配置持久保存。" : "当前快照不是持久配置。";
    scopeNote.textContent = `WS 仅统计通过本控制台网关的连接；HTTP 无状态，展示活动请求与累计请求。绕过网关的外部连接不会计入。${snapshot ? persistence : ""}`;
  }
  if (!list) return;
  if (!snapshot) {
    list.replaceChildren(
      el(
        "div",
        "transport-empty",
        app.transportsError ? `无法读取传输快照：${app.transportsError}` : "正在读取传输快照…",
      ),
    );
    return;
  }
  const fragment = document.createDocumentFragment();
  for (const definition of SERVICE_DEFS) {
    const service = snapshot.services[definition.key];
    const runtime = serviceRuntimeInfo(service);
    const row = el("div", "transport-row");
    row.dataset.service = definition.key;

    const main = el("div", "transport-main");
    const dot = el("span", "transport-state" + (runtime.className === "is-live" ? " is-listening" : runtime.className === "is-error" ? " is-error" : " is-disabled"));
    main.append(dot);
    const name = el("span", "transport-name");
    name.append(el("strong", "", definition.label), el("small", "", `${definition.group} · ${definition.kind.toUpperCase()}`));
    main.append(name);
    row.append(main);

    const stat = el("span", "transport-stat");
    if (definition.kind === "ws") {
      stat.append(el("strong", "", numberOrDash(service?.connections)), el("small", "", "WS 连接 · 本网关"));
    } else {
      stat.append(el("strong", "", numberOrDash(service?.active_requests)), el("small", "", `活动请求 · 累计 ${numberOrDash(service?.total_requests)}`));
    }
    row.append(stat);

    const endpoint = el("span", "transport-endpoint");
    endpoint.append(el("code", "", combineEndpoint(service) || "尚未提供地址"));
    const copyButton = el("button", "copy-button");
    copyButton.type = "button";
    copyButton.dataset.copyService = definition.key;
    copyButton.setAttribute("aria-label", `复制 ${definition.label} 地址`);
    copyButton.append(icon("i-copy"));
    endpoint.append(copyButton);
    row.append(endpoint);

    const badge = el("span", "phase-badge " + (runtime.className === "is-live" ? "tone-online" : runtime.className === "is-error" ? "tone-danger" : "tone-muted"), runtime.label);
    row.append(badge);
    fragment.append(row);
  }
  list.replaceChildren(fragment);
}

function renderTransportChart() {
  const svg = $("transport-chart");
  const empty = $("transport-chart-empty");
  const history = app.transportHistory.get(app.sessionID) || [];
  const snapshot = app.transports;
  const wsLatest = sumKnown(["opq_ws", "onebot_ws", "onebot12_ws"].map((key) => snapshot?.services?.[key]?.connections));
  const httpLatest = sumKnown(["opq_http", "onebot_http", "onebot12_http"].map((key) => snapshot?.services?.[key]?.active_requests));
  $("chart-ws-latest").textContent = wsLatest === null ? "—" : wsLatest.toLocaleString("zh-CN");
  $("chart-http-latest").textContent = httpLatest === null ? "—" : httpLatest.toLocaleString("zh-CN");
  $("chart-window").textContent = history.length
    ? `${clockLabel(history[0].at)}–${clockLabel(history.at(-1).at)} · ${history.length} 次采样`
    : "尚无采样";

  if (!svg) return;
  svg.replaceChildren();
  if (!history.length) {
    if (empty) {
      empty.hidden = false;
      empty.textContent = snapshot ? "等待下一次真实传输采样" : "等待传输快照后开始真实采样";
    }
    return;
  }
  if (empty) empty.hidden = true;

  const width = 640;
  const height = 132;
  const padX = 8;
  const padTop = 15;
  const padBottom = 11;
  const chartHeight = height - padTop - padBottom;

  for (let index = 0; index <= 3; index += 1) {
    const y = padTop + (chartHeight / 3) * index;
    svg.append(svgEl("line", { x1: padX, y1: y, x2: width - padX, y2: y, class: "chart-grid" }));
  }

  const values = [];
  history.forEach((sample) => {
    if (typeof sample.ws === "number") values.push(sample.ws);
    if (typeof sample.http === "number") values.push(sample.http);
  });
  const max = Math.max(1, ...values);
  const xFor = (index) => {
    if (history.length === 1) return width / 2;
    return padX + (index / (history.length - 1)) * (width - padX * 2);
  };
  const yFor = (value) => padTop + chartHeight - (value / max) * chartHeight;

  const wsPoints = [];
  const httpPoints = [];
  history.forEach((sample, index) => {
    if (typeof sample.ws === "number") wsPoints.push(`${xFor(index).toFixed(2)},${yFor(sample.ws).toFixed(2)}`);
    if (typeof sample.http === "number") httpPoints.push(`${xFor(index).toFixed(2)},${yFor(sample.http).toFixed(2)}`);
  });

  if (wsPoints.length >= 2) {
    const areaPoints = [`${xFor(history.findIndex((sample) => typeof sample.ws === "number")).toFixed(2)},${(height - padBottom).toFixed(2)}`, ...wsPoints, `${xFor(history.length - 1 - [...history].reverse().findIndex((sample) => typeof sample.ws === "number")).toFixed(2)},${(height - padBottom).toFixed(2)}`];
    svg.append(svgEl("polygon", { points: areaPoints.join(" "), class: "area-ws" }));
    svg.append(svgEl("polyline", { points: wsPoints.join(" "), class: "series-ws" }));
  }
  if (httpPoints.length >= 2) {
    svg.append(svgEl("polyline", { points: httpPoints.join(" "), class: "series-http" }));
  }

  if (history.length === 1) {
    const sample = history[0];
    if (typeof sample.ws === "number") svg.append(svgEl("circle", { cx: width / 2, cy: yFor(sample.ws), r: 2.4, class: "sample-dot" }));
    if (typeof sample.http === "number") svg.append(svgEl("circle", { cx: width / 2, cy: yFor(sample.http), r: 2.2, class: "sample-dot http" }));
  } else {
    const last = history.at(-1);
    if (typeof last.ws === "number") svg.append(svgEl("circle", { cx: xFor(history.length - 1), cy: yFor(last.ws), r: 2.6, class: "sample-dot" }));
    if (typeof last.http === "number") svg.append(svgEl("circle", { cx: xFor(history.length - 1), cy: yFor(last.http), r: 2.4, class: "sample-dot http" }));
  }
  svg.setAttribute(
    "aria-label",
    `最近 ${history.length} 次真实采样：WebSocket 连接 ${wsLatest ?? "未知"}，HTTP 活动请求 ${httpLatest ?? "未知"}`,
  );
}

function renderSettingsContext() {
  const snapshot = app.transports;
  const stale = app.transportsStale || Boolean(app.transportsError);
  const status = $("transports-sync-status");
  if (status) {
    status.textContent = snapshot
      ? stale
        ? `快照已过期 · ${clockLabel(app.transportsAt)}`
        : `已同步 ${clockLabel(app.transportsAt)}`
      : app.transportsError
        ? "传输快照不可用"
        : "等待传输快照";
  }
  const persist = $("transport-persist-note");
  if (persist) {
    persist.textContent = snapshot
      ? snapshot.persistent
        ? " 当前配置由后端持久保存。"
        : " 当前配置仅在本次进程内有效。"
      : "";
  }
  $("settings-persist-text").textContent = snapshot
    ? snapshot.persistent
      ? "后端持久保存"
      : "仅当前进程"
    : "—";

  const error = app.settingsError || app.transportsError;
  $("transports-error-text").textContent = error;
  $("transports-error").hidden = !error;
}

function bindOverviewTransportCopy() {
  $("overview-transport-list").addEventListener("click", (event) => {
    const button = event.target.closest("[data-copy-service]");
    if (!button) return;
    copyServiceEndpoint(button.dataset.copyService);
  });
}

function copyServiceEndpoint(key) {
  const service = app.transports?.services?.[key];
  copyText(combineEndpoint(service), "服务地址已复制");
}

function draftFromSnapshot(snapshot) {
  const services = {};
  for (const definition of SERVICE_DEFS) {
    const service = snapshot?.services?.[definition.key] || null;
    const port = safeNumber(service?.port) ?? 0;
    services[definition.key] = {
      enabled: Boolean(service?.enabled),
      portMode: port === 0 ? "shared" : "dedicated",
      port: String(port),
    };
  }
  return { services, dirty: false, remoteChanged: false };
}

function draftMatchesSnapshot(draft, snapshot) {
  if (!draft || !snapshot) return false;
  return SERVICE_DEFS.every((definition) => {
    const service = snapshot.services?.[definition.key];
    const current = draft.services[definition.key];
    if (!service || !current) return false;
    const port = current.portMode === "shared" ? 0 : Number(current.port);
    if (current.portMode === "dedicated" && port === 0) return false;
    return Boolean(service.enabled) === Boolean(current.enabled) && port === (safeNumber(service.port) ?? 0);
  });
}

function serviceDraftMatches(draftService, service) {
  if (!draftService) return false;
  if (!service) return !draftService.enabled && draftService.portMode === "shared";
  const port = draftService.portMode === "shared" ? 0 : Number(draftService.port);
  if (draftService.portMode === "dedicated" && port === 0) return false;
  return Boolean(draftService.enabled) === Boolean(service.enabled) && port === (safeNumber(service.port) ?? 0);
}

function settingsContainsFocus() {
  const container = $("transport-settings");
  return Boolean(container && document.activeElement && container.contains(document.activeElement));
}

function renderSettings(options = {}) {
  renderSettingsContext();
  if (app.view !== "settings") return;
  const force = Boolean(options.force);
  let draft = app.drafts.get(app.sessionID);
  if (!draft && app.transports) {
    draft = draftFromSnapshot(app.transports);
    app.drafts.set(app.sessionID, draft);
  }
  const container = $("transport-settings");
  if (!draft) {
    const empty = el("div", "empty-state compact-empty");
    const iconNode = el("span", "empty-icon");
    iconNode.append(icon(app.transportsError ? "i-warning" : "i-loader"));
    empty.append(
      iconNode,
      el("h2", "", app.transportsError ? "传输快照不可用" : "正在读取传输配置"),
      el("p", "", app.transportsError || "读取当前账号的真实服务状态后，才能编辑端口与启用状态。"),
    );
    container.replaceChildren(empty);
    updateSettingsDirtyUI();
    return;
  }
  if (force || (!draft.dirty && !settingsContainsFocus())) {
    renderTransportServiceCards(draft);
  } else {
    updateSettingsRuntimeNodes();
  }
  updateSettingsDirtyUI();
}

function renderTransportServiceCards(draft) {
  const container = $("transport-settings");
  if (!container) return;
  const fragment = document.createDocumentFragment();
  const snapshot = app.transports;
  for (const definition of SERVICE_DEFS) {
    const service = snapshot?.services?.[definition.key] || null;
    const draftService = draft.services[definition.key];
    const runtime = serviceRuntimeInfo(service);
    const changed = !serviceDraftMatches(draftService, service);
    const card = el("article", "service-card" + (changed ? " has-draft" : "") + (draftService.enabled ? "" : " is-disabled"));
    card.dataset.serviceCard = definition.key;

    const head = el("div", "service-card-head");
    const identity = el("div", "service-identity");
    const glyph = el("span", "service-glyph");
    glyph.append(icon(definition.icon));
    const title = el("span", "service-title");
    title.append(el("strong", "", definition.label), el("small", "", `${definition.group} · ${definition.kind.toUpperCase()} · ${definition.pathLabel}`));
    identity.append(glyph, title);
    const badge = el(
      "span",
      "service-runtime-badge " + runtime.className,
      runtime.state,
    );
    badge.dataset.runtimeBadge = definition.key;
    head.append(identity, badge);
    card.append(head);

    const form = el("div", "service-form");
    const switchLabel = el("label", "switch-label");
    const switchNode = el("span", "switch");
    const checkbox = el("input");
    checkbox.type = "checkbox";
    checkbox.checked = Boolean(draftService.enabled);
    checkbox.dataset.svcEnabled = definition.key;
    checkbox.disabled = app.settingsBusy;
    switchNode.append(checkbox, el("span", "switch-track"));
    switchLabel.append(switchNode, el("span", "", "启用此入口"));
    form.append(switchLabel);

    const controls = el("div", "port-controls");
    const modeLabel = el("label", "", "端口模式");
    const modeSelect = el("select");
    modeSelect.dataset.svcMode = definition.key;
    modeSelect.disabled = app.settingsBusy;
    modeSelect.append(
      new Option("共享控制台端口（0）", "shared"),
      new Option("独立回环端口", "dedicated"),
    );
    modeSelect.value = draftService.portMode;
    modeLabel.append(modeSelect);
    const portLabel = el("label", "", "端口");
    const portInput = el("input");
    portInput.type = "number";
    portInput.min = "1";
    portInput.max = "65535";
    portInput.inputMode = "numeric";
    portInput.value = draftService.portMode === "shared" ? "0" : draftService.port;
    portInput.dataset.svcPort = definition.key;
    portInput.disabled = app.settingsBusy || draftService.portMode === "shared";
    portInput.setAttribute("aria-label", `${definition.label} 监听端口`);
    portLabel.append(portInput);
    controls.append(modeLabel, portLabel);
    form.append(controls);
    card.append(form);

    const runtimeBox = el("div", "service-runtime");
    const stateLine = el("div", "service-runtime-line");
    stateLine.append(el("span", "", "当前生效"), el("strong", "", runtime.state));
    stateLine.lastElementChild.dataset.runtimeState = definition.key;
    const metricLine = el("div", "service-runtime-line");
    metricLine.append(el("span", "", "真实计数"), el("strong", "", serviceMetricText(service, definition)));
    metricLine.lastElementChild.dataset.runtimeMetric = definition.key;
    const endpointLine = el("div", "service-endpoint-line");
    const endpointCode = el("code", "", combineEndpoint(service) || "尚未提供监听地址");
    endpointCode.dataset.runtimeAddress = definition.key;
    const copyButton = el("button", "copy-button");
    copyButton.type = "button";
    copyButton.dataset.copyService = definition.key;
    copyButton.setAttribute("aria-label", `复制 ${definition.label} 地址`);
    copyButton.append(icon("i-copy"));
    endpointLine.append(endpointCode, copyButton);
    runtimeBox.append(stateLine, metricLine, endpointLine);
    const errorLine = el("div", "service-error", service?.error || "");
    errorLine.dataset.runtimeError = definition.key;
    errorLine.hidden = !service?.error;
    runtimeBox.append(errorLine);
    card.append(runtimeBox);

    if (changed) {
      const note = el("div", "service-draft-note");
      note.dataset.draftNote = definition.key;
      note.append(icon("i-warning"), el("span", "", "草稿与当前生效配置不同，应用后才会改变真实监听。"));
      card.append(note);
    }

    fragment.append(card);
  }
  container.replaceChildren(fragment);
  container.setAttribute("aria-busy", app.settingsBusy ? "true" : "false");
}

function updateSettingsRuntimeNodes() {
  const snapshot = app.transports;
  for (const definition of SERVICE_DEFS) {
    const service = snapshot?.services?.[definition.key] || null;
    const runtime = serviceRuntimeInfo(service);
    const badge = document.querySelector(`[data-runtime-badge="${definition.key}"]`);
    if (badge) {
      badge.className = "service-runtime-badge " + runtime.className;
      badge.textContent = runtime.state;
    }
    const state = document.querySelector(`[data-runtime-state="${definition.key}"]`);
    if (state) state.textContent = runtime.state;
    const metric = document.querySelector(`[data-runtime-metric="${definition.key}"]`);
    if (metric) metric.textContent = serviceMetricText(service, definition);
    const address = document.querySelector(`[data-runtime-address="${definition.key}"]`);
    if (address) address.textContent = combineEndpoint(service) || "尚未提供监听地址";
    const error = document.querySelector(`[data-runtime-error="${definition.key}"]`);
    if (error) {
      error.textContent = service?.error || "";
      error.hidden = !service?.error;
    }
    refreshDraftIndicator(definition.key);
  }
}

function refreshDraftIndicator(key) {
  const definition = SERVICE_BY_KEY[key];
  const service = app.transports?.services?.[key] || null;
  const draft = app.drafts.get(app.sessionID);
  const draftService = draft?.services?.[key];
  if (!definition || !draftService) return;
  const changed = !serviceDraftMatches(draftService, service);
  const card = document.querySelector(`[data-service-card="${key}"]`);
  if (card) {
    card.classList.toggle("has-draft", changed);
    card.classList.toggle("is-disabled", !draftService.enabled);
  }
  let note = document.querySelector(`[data-draft-note="${key}"]`);
  if (changed && !note) {
    note = el("div", "service-draft-note");
    note.dataset.draftNote = key;
    note.append(icon("i-warning"), el("span", "", "草稿与当前生效配置不同，应用后才会改变真实监听。"));
    card?.append(note);
  } else if (!changed && note) {
    note.remove();
  }
}

function updateSettingsDirtyUI() {
  const draft = app.drafts.get(app.sessionID);
  const state = $("settings-dirty-state");
  const hint = $("settings-sync-hint");
  const save = $("save-transports");
  const discard = $("discard-transports");
  const saveText = $("save-transports-text");
  if (!draft) {
    state.textContent = app.transportsError ? "配置不可用" : "等待配置";
    state.className = "";
    hint.textContent = "读取真实传输快照后才能编辑。";
    save.disabled = true;
    discard.disabled = true;
    saveText.textContent = "应用配置";
    return;
  }
  if (app.settingsBusy) {
    state.textContent = "正在应用配置…";
    state.className = "is-busy";
    hint.textContent = "正在等待后端原子更新；失败时现有入口保持不变。";
    save.disabled = true;
    discard.disabled = true;
    saveText.textContent = "正在应用…";
    return;
  }
  if (draft.dirty) {
    state.textContent = "有未应用的更改";
    state.className = "is-dirty";
    hint.textContent = draft.remoteChanged
      ? "后端快照已更新，你的草稿保持不变；应用会覆盖后端配置。"
      : "应用后立即生效。端口被占用时后端会返回错误，当前配置不会改变。";
    save.disabled = false;
    discard.disabled = false;
    saveText.textContent = "应用配置";
    return;
  }
  state.textContent = "已与后端同步";
  state.className = "";
  hint.textContent = "状态每 2.5 秒刷新；修改端口或启用状态后，需要显式应用。";
  save.disabled = true;
  discard.disabled = true;
  saveText.textContent = "应用配置";
}

function markDraftDirty() {
  const draft = app.drafts.get(app.sessionID);
  if (!draft) return;
  draft.dirty = true;
  app.settingsError = "";
  renderSettingsContext();
  updateSettingsDirtyUI();
}

function bindTransportSettings() {
  const container = $("transport-settings");
  container.addEventListener("change", (event) => {
    const draft = app.drafts.get(app.sessionID);
    if (!draft) return;
    const target = event.target;
    if (target.dataset.svcEnabled) {
      const key = target.dataset.svcEnabled;
      draft.services[key].enabled = target.checked;
      markDraftDirty();
      refreshDraftIndicator(key);
      return;
    }
    if (target.dataset.svcMode) {
      const key = target.dataset.svcMode;
      const serviceDraft = draft.services[key];
      serviceDraft.portMode = target.value;
      if (target.value === "shared") {
        serviceDraft.port = "0";
      } else if (!serviceDraft.port || Number(serviceDraft.port) === 0) {
        serviceDraft.port = "";
      }
      const portInput = container.querySelector(`[data-svc-port="${key}"]`);
      if (portInput) {
        portInput.value = serviceDraft.portMode === "shared" ? "0" : serviceDraft.port;
        portInput.disabled = serviceDraft.portMode === "shared" || app.settingsBusy;
        portInput.setAttribute("aria-invalid", "false");
      }
      markDraftDirty();
      refreshDraftIndicator(key);
      return;
    }
    if (target.dataset.svcPort) {
      const key = target.dataset.svcPort;
      draft.services[key].port = target.value;
      markDraftDirty();
      refreshDraftIndicator(key);
    }
  });
  container.addEventListener("input", (event) => {
    const target = event.target;
    if (!target.dataset.svcPort) return;
    const draft = app.drafts.get(app.sessionID);
    if (!draft) return;
    draft.services[target.dataset.svcPort].port = target.value;
    markDraftDirty();
    refreshDraftIndicator(target.dataset.svcPort);
  });
  container.addEventListener("focusout", () => {
    window.setTimeout(() => {
      const draft = app.drafts.get(app.sessionID);
      if (draft && !draft.dirty && !settingsContainsFocus()) renderSettings({ force: true });
    }, 0);
  });
  container.addEventListener("click", (event) => {
    const button = event.target.closest("[data-copy-service]");
    if (button) copyServiceEndpoint(button.dataset.copyService);
  });
  $("save-transports").addEventListener("click", saveTransports);
  $("discard-transports").addEventListener("click", discardTransportDraft);
  $("transports-retry").addEventListener("click", () => {
    poll();
  });
}

function validateDraft(draft) {
  const invalid = [];
  const payload = {};
  $$('[data-svc-port]', $("transport-settings")).forEach((input) => input.setAttribute("aria-invalid", "false"));
  for (const definition of SERVICE_DEFS) {
    const serviceDraft = draft.services[definition.key];
    let port = 0;
    if (serviceDraft.portMode === "shared") {
      port = 0;
    } else {
      const raw = String(serviceDraft.port ?? "").trim();
      const number = Number(raw);
      if (!/^\d+$/.test(raw) || !Number.isInteger(number) || number < 1 || number > 65535) {
        invalid.push({ key: definition.key, label: definition.label });
      } else {
        port = number;
      }
    }
    payload[definition.key] = { enabled: Boolean(serviceDraft.enabled), port };
  }
  if (invalid.length) {
    const first = invalid[0];
    const input = document.querySelector(`[data-svc-port="${first.key}"]`);
    if (input) {
      input.setAttribute("aria-invalid", "true");
      input.focus();
    }
    const names = invalid.map((item) => item.label).join("、");
    app.settingsError = `独立端口必须是 1–65535 的整数：${names}`;
    renderSettingsContext();
    return null;
  }
  return { services: payload };
}

async function saveTransports() {
  if (app.settingsBusy) return;
  const draft = app.drafts.get(app.sessionID);
  if (!draft) return;
  const payload = validateDraft(draft);
  if (!payload) {
    showToast(app.settingsError, "error");
    return;
  }
  const sid = app.sessionID;
  const epoch = app.epoch;
  const token = ++app.settingsSaveToken;
  app.settingsBusy = true;
  app.settingsError = "";
  renderSettings({ force: true });
  try {
    const snapshot = await controlApi("transports", {
      method: "POST",
      body: payload,
      session: sid,
      timeout: 15000,
    });
    if (app.sessionID !== sid || app.epoch !== epoch || app.settingsSaveToken !== token) {
      showToast("传输配置已应用至原会话", "success");
      return;
    }
    const normalized = normalizeTransports(snapshot);
    if (!normalized) throw new ApiError("后端没有返回有效的传输快照", 200);
    app.settingsBusy = false;
    app.drafts.set(sid, draftFromSnapshot(normalized));
    applyTransportSnapshot(normalized, { origin: "save" });
    renderSettings({ force: true });
    showToast("传输配置已应用；QQ 登录状态未受影响", "success");
  } catch (error) {
    if (app.sessionID !== sid || app.epoch !== epoch || app.settingsSaveToken !== token) return;
    app.settingsBusy = false;
    app.settingsError = `应用失败：${friendlyError(error, "后端拒绝了配置")}`;
    renderSettings({ force: true });
    showToast(app.settingsError, "error");
  } finally {
    if (app.sessionID === sid && app.epoch === epoch && app.settingsSaveToken === token) {
      app.settingsBusy = false;
      updateSettingsDirtyUI();
    }
  }
}

function discardTransportDraft() {
  if (app.settingsBusy) return;
  if (!app.transports) return;
  app.settingsError = "";
  app.drafts.set(app.sessionID, draftFromSnapshot(app.transports));
  renderSettings({ force: true });
  showToast("已放弃未应用的更改", "success");
}

function normalizeSecurity(value) {
  if (!value || typeof value !== "object" || !value.console || typeof value.console !== "object") return null;
  const hours = safeNumber(value.console.session_hours);
  return {
    console: {
      required: value.console.required !== false,
      key_configured: Boolean(value.console.key_configured),
      session_hours: hours === null ? 12 : Math.max(1, Math.round(hours)),
    },
    http: {
      enabled: Boolean(value.http?.enabled),
      configured: Boolean(value.http?.configured),
    },
    ws: {
      enabled: Boolean(value.ws?.enabled),
      configured: Boolean(value.ws?.configured),
    },
    scope: typeof value.scope === "string" ? value.scope : "instance",
    persistent: Boolean(value.persistent),
  };
}

function securityDraftFromSnapshot(snapshot) {
  return {
    consoleKey: "",
    httpEnabled: Boolean(snapshot?.http?.enabled),
    httpToken: "",
    wsEnabled: Boolean(snapshot?.ws?.enabled),
    wsToken: "",
  };
}

function readSecurityControls() {
  const draft = app.securityDraft;
  if (!draft) return;
  draft.consoleKey = $("security-console-key")?.value || "";
  draft.httpEnabled = Boolean($("security-http-enabled")?.checked);
  draft.httpToken = $("security-http-token")?.value || "";
  draft.wsEnabled = Boolean($("security-ws-enabled")?.checked);
  draft.wsToken = $("security-ws-token")?.value || "";
}

function hydrateSecurityForm() {
  const snapshot = app.security;
  const draft = app.securityDraft;
  if (!snapshot || !draft) return;
  const key = $("security-console-key");
  const http = $("security-http-token");
  const ws = $("security-ws-token");
  if (key) key.value = draft.consoleKey || "";
  if (http) http.value = draft.httpToken || "";
  if (ws) ws.value = draft.wsToken || "";
  const httpToggle = $("security-http-enabled");
  const wsToggle = $("security-ws-enabled");
  if (httpToggle) httpToggle.checked = Boolean(draft.httpEnabled);
  if (wsToggle) wsToggle.checked = Boolean(draft.wsEnabled);
}

function setPasswordVisibility(button, input, visible) {
  if (!button || !input) return;
  input.type = visible ? "text" : "password";
  button.setAttribute("aria-pressed", visible ? "true" : "false");
  const label = button.dataset.showLabel || button.getAttribute("aria-label") || "显示凭据";
  button.setAttribute("aria-label", visible ? label.replace(/^显示/, "隐藏") : label);
  button.replaceChildren(icon(visible ? "i-eye-off" : "i-eye"));
}

function resetPasswordReveal(buttonID, inputID) {
  const button = $(buttonID);
  const input = $(inputID);
  if (!button || !input) return;
  input.type = "password";
  button.setAttribute("aria-pressed", "false");
  button.setAttribute("aria-label", button.dataset.showLabel || "显示凭据");
  button.replaceChildren(icon("i-eye"));
}

function bindPasswordReveal(buttonID, inputID) {
  const button = $(buttonID);
  const input = $(inputID);
  if (!button || !input) return;
  if (!button.dataset.showLabel) button.dataset.showLabel = button.getAttribute("aria-label") || "显示凭据";
  button.addEventListener("click", () => {
    setPasswordVisibility(button, input, input.type === "password");
  });
}

function securityGenerateToken() {
  try {
    if (!globalThis.crypto || typeof globalThis.crypto.getRandomValues !== "function") {
      throw new Error("当前浏览器不支持安全随机数");
    }
    const values = new Uint8Array(24);
    globalThis.crypto.getRandomValues(values);
    return Array.from(values, (value) => value.toString(16).padStart(2, "0")).join("");
  } catch {
    showToast("无法生成安全随机 Token", "error");
    return "";
  }
}

function generateSecurityInput(inputID, buttonID) {
  if (!app.authenticated || app.securitySaving || !app.security) return;
  const input = $(inputID);
  if (!input) return;
  const token = securityGenerateToken();
  if (!token) return;
  input.value = token;
  setPasswordVisibility($(buttonID), input, true);
  readSecurityControls();
  refreshSecurityDirty();
  showToast("已生成随机 Token；保存后才会生效", "success");
}

function refreshSecurityDirty() {
  const snapshot = app.security;
  const draft = app.securityDraft;
  if (!snapshot || !draft) {
    app.securityDirty = false;
    return;
  }
  app.securityDirty = Boolean(
    draft.consoleKey.length
    || draft.httpToken.length
    || draft.wsToken.length
    || draft.httpEnabled !== snapshot.http.enabled
    || draft.wsEnabled !== snapshot.ws.enabled,
  );
  app.securityError = "";
  renderSecurity();
}

function securityStatusText() {
  if (app.securityLoading) return "正在刷新实例安全状态…";
  if (app.securityError && app.security) return app.securityError;
  if (!app.securityAt) return "等待读取";
  return `已同步 ${clockLabel(app.securityAt)}`;
}

function renderSecurity() {
  const panel = $("security-settings");
  if (!panel) return;
  const snapshot = app.security;
  const form = $("security-form");
  const loading = $("security-loading");
  const topError = $("security-error");
  const formError = $("security-form-error");
  const sync = $("security-sync-state");
  const configured = $("security-console-configured");
  const hours = $("security-session-hours");
  const httpState = $("security-http-state");
  const wsState = $("security-ws-state");
  const consoleInput = $("security-console-key");
  const httpInput = $("security-http-token");
  const wsInput = $("security-ws-token");
  const save = $("save-security");
  const discard = $("discard-security");
  const refresh = $("security-refresh");
  const dirtyState = $("security-dirty-state");
  const hint = $("security-sync-hint");
  const saveText = $("save-security-text");
  const formErrorText = app.security ? app.securityError : "";

  if (loading) loading.hidden = !(app.securityLoading && !snapshot);
  const showTopError = Boolean(app.securityError && !snapshot);
  if (topError) {
    topError.textContent = showTopError ? app.securityError : "";
    topError.hidden = !showTopError;
  }
  if (formError) {
    formError.textContent = formErrorText;
    formError.hidden = !formErrorText;
  }
  if (form) form.hidden = !snapshot;
  if (sync) sync.textContent = securityStatusText();
  if (configured) configured.textContent = snapshot?.console.key_configured ? "已配置" : "未配置";
  if (hours) hours.textContent = `${snapshot?.console.session_hours ?? 12} 小时`;
  if (httpState) {
    httpState.textContent = snapshot?.http.enabled ? "已启用" : "未启用";
    httpState.className = "soft-badge" + (snapshot?.http.enabled ? " tone-online" : "");
  }
  if (wsState) {
    wsState.textContent = snapshot?.ws.enabled ? "已启用" : "未启用";
    wsState.className = "soft-badge" + (snapshot?.ws.enabled ? " tone-online" : "");
  }
  if (consoleInput) consoleInput.placeholder = snapshot?.console.key_configured ? "已配置，留空保持" : "设置至少 16 个字符的控制台密钥";
  if (httpInput) httpInput.placeholder = snapshot?.http.configured ? "已配置，留空保持" : "至少 16 个字符";
  if (wsInput) wsInput.placeholder = snapshot?.ws.configured ? "已配置，留空保持" : "至少 16 个字符";
  if (refresh) refresh.disabled = Boolean(app.securityLoading || app.securitySaving);
  if (form) {
    const disabled = Boolean(!snapshot || app.securitySaving || app.securityLoading);
    for (const control of form.querySelectorAll("input, select, button")) {
      control.disabled = disabled;
    }
  }
  if (save) save.disabled = Boolean(!snapshot || app.securitySaving || app.securityLoading || !app.securityDirty);
  if (discard) discard.disabled = Boolean(!snapshot || app.securitySaving || app.securityLoading || !app.securityDirty);
  if (saveText) saveText.textContent = app.securitySaving ? "正在保存…" : "保存安全设置";
  if (dirtyState) {
    dirtyState.className = "";
    if (app.securitySaving) {
      dirtyState.textContent = "正在保存实例安全设置…";
      dirtyState.className = "is-busy";
    } else if (!snapshot) {
      dirtyState.textContent = app.securityError ? "实例安全设置不可用" : "等待读取实例安全设置";
    } else if (app.securityDirty) {
      dirtyState.textContent = "有未保存的更改";
      dirtyState.className = "is-dirty";
    } else {
      dirtyState.textContent = "已与后端同步";
    }
  }
  if (hint) {
    if (app.securitySaving) hint.textContent = "设置保存后才会应用到经过本网关的外部客户端。";
    else if (!snapshot) hint.textContent = "请刷新后重试。控制台登录不能被关闭。";
    else if (app.securityDirty) hint.textContent = "修改 HTTP / WS Token 或策略会立即影响新请求；现有外部 WS 会在保存后关闭。";
    else hint.textContent = "Console、HTTP、WS 三个凭据必须彼此不同。敏感值不会写入页面存储或复制地址。";
  }
  renderExternalAuthPresentation();
}

async function loadSecurity(options = {}) {
  if (!app.authenticated || !app.booted || app.authLoggingOut) return;
  if (app.securityLoading || app.securitySaving) return;
  if (app.securityLoaded && !options.force) return;
  const authGen = app.authGeneration;
  app.securityLoading = true;
  app.securityError = "";
  renderSecurity();
  try {
    const raw = await controlApi("security", { scoped: false, timeout: SECURITY_TIMEOUT });
    if (authGen !== app.authGeneration || !app.authenticated) return;
    const snapshot = normalizeSecurity(raw);
    if (!snapshot) throw new ApiError("安全设置响应格式不正确", 200);
    app.security = snapshot;
    app.securityLoaded = true;
    app.securityAt = Date.now();
    app.securityError = "";
    if (!app.securityDirty) {
      app.securityDraft = securityDraftFromSnapshot(snapshot);
      hydrateSecurityForm();
    }
    if (options.notify && app.securityDirty) showToast("实例安全状态已刷新；未保存的草稿保持不变", "info");
  } catch (error) {
    if (authGen !== app.authGeneration || !app.authenticated) return;
    app.securityError = friendlyError(error, "无法读取实例安全设置");
    if (options.manual) showToast(app.securityError, "error");
  } finally {
    if (authGen === app.authGeneration) {
      app.securityLoading = false;
      renderSecurity();
      refreshAuthDependentViews();
    }
  }
}

function validateSecurityDraft() {
  const snapshot = app.security;
  const draft = app.securityDraft;
  if (!snapshot || !draft) return null;
  const problems = [];
  const entered = [
    ["控制台密钥", draft.consoleKey],
    ["HTTP Token", draft.httpToken],
    ["WS Token", draft.wsToken],
  ].filter(([, value]) => value.length > 0);
  for (const [label, value] of entered) {
    if (value.length < SECRET_MIN_LENGTH) problems.push(`${label}至少需要 ${SECRET_MIN_LENGTH} 个字符。`);
    else if (value !== value.trim()) problems.push(`${label}首尾不能包含空格。`);
  }
  if (draft.httpEnabled && !snapshot.http.configured && !draft.httpToken) {
    problems.push("启用 HTTP 鉴权前需要配置 HTTP Token。");
  }
  if (draft.wsEnabled && !snapshot.ws.configured && !draft.wsToken) {
    problems.push("启用 WS 鉴权前需要配置 WS Token。");
  }
  for (let left = 0; left < entered.length; left += 1) {
    for (let right = left + 1; right < entered.length; right += 1) {
      if (entered[left][1] === entered[right][1]) {
        problems.push(`${entered[left][0]}不能与${entered[right][0]}相同。`);
      }
    }
  }
  if (problems.length) {
    app.securityError = problems.join(" ");
    renderSecurity();
    showToast(problems[0], "error");
    return null;
  }
  const payload = {
    http: { enabled: Boolean(draft.httpEnabled) },
    ws: { enabled: Boolean(draft.wsEnabled) },
  };
  if (draft.consoleKey) payload.console_key = draft.consoleKey;
  if (draft.httpToken) payload.http.token = draft.httpToken;
  if (draft.wsToken) payload.ws.token = draft.wsToken;
  return {
    payload,
    keyChanged: Boolean(draft.consoleKey),
    enteredCount: entered.length,
  };
}

async function saveSecuritySettings(event) {
  if (event) event.preventDefault();
  if (!app.authenticated || app.authLoggingOut || app.securitySaving) return;
  const validated = validateSecurityDraft();
  if (!validated) return;
  if (!app.securityDirty) {
    showToast("没有需要保存的更改", "info");
    return;
  }
  if (validated.keyChanged) {
    const confirmed = await openConfirmDialog({
      title: "修改控制台密钥？",
      message:
        "保存后，所有控制台登录会话会立即失效，你必须使用新密钥重新登录。\n" +
        "HTTP / WS 访问策略会一并更新。\n\n" +
        "这不会断开任何 QQ 连接，也不会影响已挂接的旧进程。",
      confirmLabel: "修改并重新登录",
      danger: true,
    });
    if (!confirmed) return;
  }
  const authGen = app.authGeneration;
  const token = ++app.securitySaveToken;
  app.securitySaving = true;
  app.securityError = "";
  renderSecurity();
  try {
    const raw = await controlApi("security", {
      method: "POST",
      body: validated.payload,
      scoped: false,
      timeout: SECURITY_TIMEOUT + 5000,
    });
    if (authGen !== app.authGeneration || app.securitySaveToken !== token || !app.authenticated) return;
    const snapshot = normalizeSecurity(raw);
    if (!snapshot) throw new ApiError("安全设置保存后没有返回有效的实例快照", 200);
    const reauth = Boolean(raw?.reauthenticate) || validated.keyChanged;
    app.security = snapshot;
    app.securityLoaded = true;
    app.securityAt = Date.now();
    app.securityError = "";
    app.securityDirty = false;
    app.securityDraft = securityDraftFromSnapshot(snapshot);
    for (const id of ["security-console-key", "security-http-token", "security-ws-token"]) {
      const input = $(id);
      if (input) input.value = "";
    }
    const consoleToggle = $("toggle-security-console-key");
    const httpToggle = $("toggle-security-http-token");
    const wsToggle = $("toggle-security-ws-token");
    if (consoleToggle) setPasswordVisibility(consoleToggle, $("security-console-key"), false);
    if (httpToggle) setPasswordVisibility(httpToggle, $("security-http-token"), false);
    if (wsToggle) setPasswordVisibility(wsToggle, $("security-ws-token"), false);
    hydrateSecurityForm();
    renderSecurity();
    if (reauth) {
      lockConsole("控制台密钥已更新，请使用新密钥重新登录。QQ 连接未受影响。", { reason: "reauth" });
      return;
    }
    refreshAuthDependentViews();
    showToast("实例安全设置已保存；QQ 登录状态未受影响", "success");
  } catch (error) {
    if (authGen !== app.authGeneration || app.securitySaveToken !== token || !app.authenticated) return;
    app.securityError = `保存失败：${friendlyError(error, "后端拒绝了安全设置")}`;
    renderSecurity();
    showToast(app.securityError, "error");
  } finally {
    if (authGen === app.authGeneration && app.securitySaveToken === token) {
      app.securitySaving = false;
      renderSecurity();
    }
  }
}

function discardSecurityDraft() {
  if (app.securitySaving || !app.security) return;
  app.securityDraft = securityDraftFromSnapshot(app.security);
  app.securityDirty = false;
  app.securityError = "";
  for (const id of ["security-console-key", "security-http-token", "security-ws-token"]) {
    const input = $(id);
    if (input) input.value = "";
  }
  hydrateSecurityForm();
  renderSecurity();
  showToast("已放弃未保存的安全设置更改", "success");
}

function refreshAuthDependentViews() {
  if (!app.booted) return;
  renderPlaygroundTargets();
  if ($("ws-endpoint")) renderWs();
}

function renderExternalAuthPresentation() {
  const httpAuth = Boolean(app.security?.http?.enabled);
  const wsAuth = Boolean(app.security?.ws?.enabled);
  const httpBadge = $("api-external-auth-badge");
  const httpHelp = $("api-external-auth-help");
  if (httpBadge) {
    httpBadge.hidden = !httpAuth;
    httpBadge.textContent = "需鉴权";
  }
  if (httpHelp) {
    if (!app.securityLoaded) {
      httpHelp.textContent = "实例安全状态尚未读取。复制的地址不会包含 Token。";
    } else if (httpAuth) {
      httpHelp.textContent = "此外部地址需要 Authorization: Bearer <HTTP_TOKEN>。控制台测试入口使用同源登录 Cookie，不需要外部 Token；复制的地址不会包含 Token。";
    } else {
      httpHelp.textContent = "HTTP 鉴权当前未启用。复制的地址不会包含 Token；启用后外部客户端需发送 Authorization 请求头。";
    }
  }
  const wsBadge = $("ws-auth-badge");
  const wsHelp = $("ws-auth-help");
  if (wsBadge) {
    wsBadge.hidden = !wsAuth;
    wsBadge.textContent = "需鉴权";
  }
  if (wsHelp) {
    if (!app.securityLoaded) {
      wsHelp.textContent = "实例安全状态尚未读取。测试连接通过受保护的同源 socket，不需要外部 Token。";
    } else if (wsAuth) {
      wsHelp.textContent = "外部 WebSocket 握手需 Authorization: Bearer <WS_TOKEN>，或仅对 WS 使用 ?access_token=<WS_TOKEN>。控制台测试连接使用受保护的同源 socket，不需要外部 Token；复制的地址不会包含 Token。";
    } else {
      wsHelp.textContent = "WS 鉴权当前未启用。控制台测试连接通过受保护的同源 socket；复制的地址不会包含 Token。";
    }
  }
}

function bindSecuritySettings() {
  const form = $("security-form");
  if (!form) return;
  const readingFields = ["security-console-key", "security-http-token", "security-ws-token", "security-http-enabled", "security-ws-enabled"];
  const refreshInteraction = (event) => {
    if (!readingFields.includes(event.target.id)) return;
    readSecurityControls();
    refreshSecurityDirty();
  };
  form.addEventListener("input", refreshInteraction);
  form.addEventListener("change", refreshInteraction);
  form.addEventListener("submit", saveSecuritySettings);
  $("discard-security").addEventListener("click", discardSecurityDraft);
  $("security-refresh").addEventListener("click", () => loadSecurity({ manual: true, force: true }));
  bindPasswordReveal("toggle-security-console-key", "security-console-key");
  bindPasswordReveal("toggle-security-http-token", "security-http-token");
  bindPasswordReveal("toggle-security-ws-token", "security-ws-token");
  $("generate-console-key").addEventListener("click", () => generateSecurityInput("security-console-key", "toggle-security-console-key"));
  $("generate-http-token").addEventListener("click", () => generateSecurityInput("security-http-token", "toggle-security-http-token"));
  $("generate-ws-token").addEventListener("click", () => generateSecurityInput("security-ws-token", "toggle-security-ws-token"));
  renderSecurity();
}

function getLogState(sid = app.sessionID) {
  if (!sid) return null;
  let state = app.logs.get(sid);
  if (!state) {
    state = {
      entries: [],
      seen: new Set(),
      lastID: 0,
      error: "",
      fetched: false,
      loadedAt: 0,
    };
    app.logs.set(sid, state);
  }
  return state;
}

function resetLogState(sid) {
  if (!sid) return;
  app.logs.delete(sid);
  if (sid === app.sessionID) {
    app.qrKey = "";
    paintLogs(true);
  }
}

function normalizeLogEntry(entry) {
  if (!entry || typeof entry !== "object") return null;
  const id = Number(entry.id);
  if (!Number.isFinite(id)) return null;
  return {
    id,
    time: entry.time || "",
    level: typeof entry.level === "string" ? entry.level.toUpperCase() : "INFO",
    message: typeof entry.message === "string" ? entry.message : String(entry.message ?? ""),
    fields: entry.fields && typeof entry.fields === "object" && !Array.isArray(entry.fields) ? entry.fields : null,
  };
}

function appendLogs(sid, entries) {
  const state = getLogState(sid);
  if (!state || !Array.isArray(entries)) return;
  const sorted = entries
    .map(normalizeLogEntry)
    .filter(Boolean)
    .sort((left, right) => left.id - right.id);
  for (const entry of sorted) {
    if (state.seen.has(entry.id)) continue;
    state.entries.push(entry);
    state.seen.add(entry.id);
    state.lastID = Math.max(state.lastID, entry.id);
  }
  if (state.entries.length > MAX_LOGS) {
    state.entries = state.entries.slice(-MAX_LOGS);
    state.seen = new Set(state.entries.map((entry) => entry.id));
  }
  state.error = "";
  state.fetched = true;
  state.loadedAt = Date.now();
  if (sid === app.sessionID && !app.logsPaused) paintLogs();
  if (sid === app.sessionID) paintRecentLogs();
}

function bindLogs() {
  $$(".log-filter").forEach((button) => {
    button.addEventListener("click", () => {
      app.logLevel = button.dataset.level || "ALL";
      $$(".log-filter").forEach((node) => {
        const selected = node === button;
        node.classList.toggle("selected", selected);
        node.setAttribute("aria-pressed", selected ? "true" : "false");
      });
      paintLogs(true);
    });
  });
  $("log-search").addEventListener("input", () => {
    app.logSearch = $("log-search").value;
    paintLogs(true);
  });
  $("pause-logs").addEventListener("click", () => {
    app.logsPaused = !app.logsPaused;
    updatePauseButton();
    if (!app.logsPaused) paintLogs(true);
    else renderLogStatus();
  });
  $("clear-logs").addEventListener("click", () => {
    const state = getLogState();
    if (!state) return;
    state.entries = [];
    state.seen = new Set();
    paintLogs(true);
    paintRecentLogs();
    showToast("已清空本地显示；游标保留，新日志会继续追加", "success");
  });
  $("download-logs").addEventListener("click", downloadLogs);
}

function updatePauseButton() {
  const button = $("pause-logs");
  if (!button) return;
  button.replaceChildren(icon(app.logsPaused ? "i-play" : "i-stop"));
  const label = app.logsPaused ? "继续日志显示" : "暂停日志显示";
  button.setAttribute("aria-label", label);
  button.setAttribute("title", label);
  button.setAttribute("aria-pressed", app.logsPaused ? "true" : "false");
  button.classList.toggle("is-active", app.logsPaused);
}

function renderLogStatus() {
  const state = getLogState();
  const status = $("log-status-text");
  const error = $("log-error");
  if (!state) {
    if (status) status.textContent = "等待日志";
    if (error) error.hidden = true;
    return;
  }
  if (status) {
    status.textContent = state.error
      ? "日志暂不可用"
      : app.logsPaused
        ? "显示已暂停 · 缓冲继续"
        : state.fetched
          ? `已同步 ${clockLabel(state.loadedAt)}`
          : "正在载入";
  }
  if (error) {
    error.textContent = state.error ? `日志读取失败：${state.error}；已保留本地缓冲。` : "";
    error.hidden = !state.error;
  }
}

function logMatches(entry) {
  if (app.logLevel !== "ALL" && entry.level !== app.logLevel) return false;
  if (!app.logSearch) return true;
  const haystack = [entry.message, formatFields(entry.fields)].join(" ").toLowerCase();
  return haystack.includes(app.logSearch.toLowerCase());
}

function formatFields(fields) {
  if (!fields || !Object.keys(fields).length) return "";
  return Object.entries(fields)
    .map(([key, value]) => {
      let rendered;
      if (typeof value === "string") rendered = value;
      else {
        try {
          rendered = JSON.stringify(value);
        } catch {
          rendered = String(value);
        }
      }
      return `${key}=${rendered}`;
    })
    .join("  ");
}

function makeLogRow(entry, recent = false) {
  const row = el("div", recent ? "recent-log-row" : "log-row");
  row.append(el("time", "log-time", clockLabel(entry.time)));
  const level = LEVELS.has(entry.level) ? entry.level : "INFO";
  row.append(el("span", "log-level " + level, level));
  const message = el("span", "log-message", entry.message);
  const fields = formatFields(entry.fields);
  if (fields) message.append(el("span", "log-fields", fields));
  row.append(message);
  return row;
}

function paintLogs(force = false) {
  renderLogStatus();
  paintRecentLogs();
  if (app.view !== "logs" && !force) return;
  if (app.logsPaused && !force) return;
  const state = getLogState();
  const output = $("log-output");
  if (!output) return;
  const position = output.scrollTop;
  const entries = state ? state.entries.filter(logMatches) : [];
  const fragment = document.createDocumentFragment();
  entries.forEach((entry) => fragment.append(makeLogRow(entry)));
  if (!entries.length) {
    let emptyText = "当前会话还没有日志记录。";
    if (!state) emptyText = "请选择一个会话。";
    else if (!state.fetched) emptyText = "正在载入日志…";
    else if (state.error) emptyText = "日志暂不可用；下方保留的缓冲仍可查看筛选结果。";
    else if (state.entries.length) emptyText = "没有符合当前级别与搜索条件的日志。";
    fragment.append(el("div", "log-empty", emptyText));
  }
  output.replaceChildren(fragment);
  $("log-count").textContent = state
    ? `${entries.length} 条 · 缓冲 ${state.entries.length}/${MAX_LOGS}`
    : "0 条日志";
  output.scrollTop = $("auto-scroll").checked ? output.scrollHeight : position;
  renderLogStatus();
}

function paintRecentLogs() {
  const state = getLogState();
  const list = $("recent-logs");
  const empty = $("recent-log-empty");
  if (!list || !empty) return;
  if (app.logsPaused) return;
  const entries = state ? state.entries.slice(-4).reverse() : [];
  if (!entries.length) {
    list.replaceChildren();
    empty.hidden = false;
    empty.textContent = state?.fetched ? "当前会话还没有运行记录。" : "正在载入运行记录…";
    return;
  }
  empty.hidden = true;
  list.replaceChildren(...entries.map((entry) => makeLogRow(entry, true)));
}

function downloadLogs() {
  const state = getLogState();
  if (!state || !state.entries.length) {
    showToast("当前会话没有可导出的日志", "error");
    return;
  }
  const text = state.entries.map((entry) => JSON.stringify(entry)).join("\n") + "\n";
  const blob = new Blob([text], { type: "application/x-ndjson;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `openopq-logs-${(currentAccount() || app.sessionID || "session").slice(-8)}.jsonl`;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  showToast(`已导出 ${state.entries.length} 条当前会话日志`, "success");
}

function renderAccountCounts() {
  const online = app.sessions.filter((session) => session.online).length;
  $("account-count").textContent = String(app.sessions.length);
  $("sessions-count").textContent = String(app.sessions.length);
  $("sessions-online-count").textContent = String(online);
  $("profiles-count").textContent = `${app.profiles.length} 个档案`;
  const profilesError = $("profiles-error");
  profilesError.textContent = app.profilesError ? `设备档案读取失败：${app.profilesError}` : "";
  profilesError.hidden = !app.profilesError;
}

function updateDatalist() {
  const seen = new Set();
  const options = [];
  for (const profile of app.profiles) {
    if (!profile?.account || seen.has(profile.account)) continue;
    seen.add(profile.account);
    const option = new Option(profile.account, profile.account);
    options.push(option);
  }
  for (const session of app.sessions) {
    if (!session.account || seen.has(session.account)) continue;
    seen.add(session.account);
    options.push(new Option(session.account, session.account));
  }
  $("saved-accounts").replaceChildren(...options);
}

function renderAccounts(options = {}) {
  const grid = $("sessions-grid");
  const empty = $("sessions-empty");
  if (!grid) return;
  renderAccountCounts();
  const active = document.activeElement;
  const focusedButton = active && grid.contains(active) && active.dataset.sessionAction
    ? { action: active.dataset.sessionAction, id: active.dataset.sessionId }
    : null;
  empty.hidden = app.sessions.length > 0;
  if (!app.sessions.length) {
    grid.replaceChildren();
  } else {
    const fragment = document.createDocumentFragment();
    for (const session of app.sessions) {
      const card = el("article", "session-card" + (session.id === app.sessionID ? " is-selected" : ""));
      card.dataset.sessionId = session.id;
      const head = el("div", "session-card-head");
      const avatar = el("span", "session-avatar", session.account ? session.account.slice(-2) : "OP");
      const title = el("div", "session-card-title");
      title.append(
        el("strong", "", sessionTitle(session)),
        el("small", "", session.nickname || (session.external ? "外部接入会话" : "本地会话") + " · " + sessionShort(session.id)),
      );
      const badge = el("span", "phase-badge");
      setPhaseBadge(badge, session);
      head.append(avatar, title, badge);
      card.append(head);

      const meta = el("div", "session-card-meta");
      const metaItems = [
        ["运行时长", durationLabel(session.uptime)],
        ["事件", `${numberOrDash((safeNumber(session.messages) || 0) + (safeNumber(session.notices) || 0))} 条`],
        ["协议", [session.opq ? "OPQ" : "", session.onebot ? "OneBot" : ""].filter(Boolean).join(" · ") || "—"],
        ["会话", sessionShort(session.id)],
      ];
      for (const [label, value] of metaItems) {
        const item = el("div");
        item.append(el("span", "", label), el("strong", "", value));
        meta.append(item);
      }
      card.append(meta);
      if (session.error) {
        card.append(el("div", "inline-error session-card-note", session.error));
      } else if (session.notice) {
        card.append(el("div", "inline-notice session-card-note", session.notice));
      }

      const foot = el("div", "session-card-foot");
      const viewButton = el("button", "button compact ghost", "查看");
      viewButton.type = "button";
      viewButton.dataset.sessionAction = "view";
      viewButton.dataset.sessionId = session.id;
      const settingsButton = el("button", "button compact ghost", "设置");
      settingsButton.type = "button";
      settingsButton.dataset.sessionAction = "settings";
      settingsButton.dataset.sessionId = session.id;
      const logsButton = el("button", "button compact ghost", "日志");
      logsButton.type = "button";
      logsButton.dataset.sessionAction = "logs";
      logsButton.dataset.sessionId = session.id;
      foot.append(viewButton, settingsButton, logsButton);
      const flags = el("span", "session-flags");
      if (session.external) flags.append(el("span", "soft-badge", "外部"));
      if (session.online) flags.append(el("span", "soft-badge", "在线"));
      foot.append(flags);
      card.append(foot);
      fragment.append(card);
    }
    grid.replaceChildren(fragment);
    if (focusedButton) {
      const restore = Array.from(grid.querySelectorAll("[data-session-action]")).find(
        (node) => node.dataset.sessionAction === focusedButton.action && node.dataset.sessionId === focusedButton.id,
      );
      if (restore) restore.focus({ preventScroll: true });
    }
  }

  const profilesGrid = $("profiles-grid");
  const profilesEmpty = $("profiles-empty");
  const signature = JSON.stringify(app.profiles);
  if (options.forceProfiles || signature !== app.profilesSig || !profilesGrid.childElementCount) {
    app.profilesSig = signature;
    profilesEmpty.hidden = app.profiles.length > 0;
    if (!app.profiles.length) {
      profilesGrid.replaceChildren();
    } else {
      const fragment = document.createDocumentFragment();
      for (const profile of app.profiles) {
        const card = el("article", "profile-card");
        const head = el("div", "profile-head");
        const avatar = el("span", "profile-avatar", String(profile.account || "?").slice(-2));
        const title = el("div", "profile-title");
        title.append(el("strong", "", profile.account || "—"), el("small", "", profile.computer_name || "已保存设备身份"));
        head.append(avatar, title);
        card.append(head);
        const details = el("details", "device-details");
        const summary = el("summary");
        summary.append(el("span", "", "设备 GUID"), el("span", "details-note", "仅展开时显示"), icon("i-chevron"));
        const row = el("div", "device-guid-row");
        row.append(el("code", "", profile.guid || "—"));
        const copy = el("button", "copy-button");
        copy.type = "button";
        copy.dataset.copyGuid = profile.guid || "";
        copy.setAttribute("aria-label", "复制设备 GUID");
        copy.append(icon("i-copy"));
        row.append(copy);
        details.append(summary, row);
        card.append(details);
        const last = el("div", "profile-last");
        last.append(el("span", "", "最近登录"), el("span", "", profile.last_login ? dateTimeLabel(profile.last_login) : "尚未完成登录"));
        card.append(last);
        const actions = el("div", "profile-actions");
        const open = el("button", "button compact ghost", "打开会话");
        open.type = "button";
        open.dataset.profileAccount = profile.account || "";
        actions.append(open);
        card.append(actions);
        fragment.append(card);
      }
      profilesGrid.replaceChildren(fragment);
    }
  }
  updateDatalist();
}

function bindAccountGrids() {
  $("sessions-grid").addEventListener("click", (event) => {
    const button = event.target.closest("[data-session-action]");
    if (!button) return;
    const id = button.dataset.sessionId;
    if (!id) return;
    selectSession(id);
    const action = button.dataset.sessionAction;
    if (action === "settings") setView("settings");
    else if (action === "logs") setView("logs");
    else setView("overview");
  });
  $("profiles-grid").addEventListener("click", (event) => {
    const copyButton = event.target.closest("[data-copy-guid]");
    if (copyButton) {
      copyText(copyButton.dataset.copyGuid || "", "设备 GUID 已复制");
      return;
    }
    const button = event.target.closest("[data-profile-account]");
    if (!button) return;
    openAccountSession(button.dataset.profileAccount);
  });
}

function createPlaygroundState() {
  return {
    protocol: "opq",
    opq: {
      template: "opq-status",
      body: OPQ_CLUSTER_BODY,
    },
    onebot: {
      template: "onebot-status",
      body: "{}",
      action: "get_status",
      mode: "path",
    },
    result: null,
    history: [],
    sending: false,
    wsEndpoint: "opq_ws",
    wsEvents: [],
    wsNote: "",
    wsError: "",
  };
}

function getPlaygroundState(sid = app.sessionID) {
  if (!sid) return createPlaygroundState();
  let state = app.playgrounds.get(sid);
  if (!state) {
    state = createPlaygroundState();
    app.playgrounds.set(sid, state);
  }
  return state;
}

function playgroundTemplate(protocol, id) {
  return API_TEMPLATES[protocol].find((template) => template.id === id) || API_TEMPLATES[protocol][0];
}

function getProtocolDraft(state = getPlaygroundState()) {
  return state[state.protocol === "opq" ? "opq" : "onebot"];
}

function renderPlayground() {
  const state = getPlaygroundState();
  const protocol = state.protocol;
  const draft = getProtocolDraft(state);
  $$(".protocol-tab").forEach((tab) => {
    const active = tab.dataset.protocol === protocol;
    tab.classList.toggle("active", active);
    tab.setAttribute("aria-selected", active ? "true" : "false");
    tab.tabIndex = active ? 0 : -1;
  });
  $("api-context-account").textContent = currentAccount() ? `QQ ${currentAccount()}` : "未绑定账号";
  $("api-context-session").textContent = app.sessionID || "—";
  $("api-context-protocol").textContent = protocol === "opq" ? "OPQ" : "OneBot v11";

  const select = $("api-template");
  const templates = API_TEMPLATES[protocol];
  const selectedTemplate = playgroundTemplate(protocol, draft.template);
  select.replaceChildren(
    ...templates.map((template) => {
      const option = new Option(template.label, template.id);
      option.selected = template.id === selectedTemplate.id;
      return option;
    }),
  );
  select.value = selectedTemplate.id;
  const hint = $("api-template-hint");
  if (hint) hint.textContent = selectedTemplate.hint || "";

  const onebot = protocol === "onebot";
  $("onebot-action-field").hidden = !onebot || draft.mode === "envelope";
  $("onebot-mode-field").hidden = !onebot;
  $("onebot-action").value = onebot ? draft.action || "" : "";
  $("onebot-action").disabled = Boolean(onebot && draft.mode === "envelope");
  $("onebot-mode").value = onebot ? draft.mode : "path";
  $("api-body").value = draft.body || "";
  updateJsonStatus();
  renderPlaygroundTargets();
  renderApiResult(state);
  renderApiHistory(state);
  renderWs(state);
}

function switchProtocol(protocol) {
  const state = getPlaygroundState();
  if (state.protocol === protocol) return;
  state.protocol = protocol;
  renderPlayground();
}

function selectApiTemplate(id) {
  const state = getPlaygroundState();
  const draft = getProtocolDraft(state);
  const template = playgroundTemplate(state.protocol, id);
  draft.template = template.id;
  if (template.id !== "custom") {
    draft.body = template.body;
    if (state.protocol === "onebot") {
      draft.action = template.action || draft.action;
      draft.mode = "path";
    }
  }
  renderPlayground();
}

function updateJsonStatus() {
  const state = getPlaygroundState();
  const draft = getProtocolDraft(state);
  const body = draft.body || "";
  const node = $("json-status");
  let result = { ok: false, value: null, error: "" };
  if (!body.trim()) {
    node.textContent = "需要 JSON";
    node.className = "json-state is-bad";
    return result;
  }
  try {
    result.value = JSON.parse(body);
    result.ok = true;
    if (state.protocol === "onebot" && draft.mode === "envelope") {
      if (!result.value || typeof result.value !== "object" || Array.isArray(result.value) || typeof result.value.action !== "string" || !result.value.action) {
        node.textContent = "根信封需要 action 字段";
        node.className = "json-state is-bad";
        return { ok: false, value: result.value, error: "根信封需要 action 字符串" };
      }
    }
    node.textContent = `JSON 有效 · ${body.split("\n").length} 行`;
    node.className = "json-state is-ok";
    return result;
  } catch (error) {
    const message = error instanceof Error ? error.message : "JSON 语法错误";
    node.textContent = `JSON 错误 · ${message}`;
    node.className = "json-state is-bad";
    return { ok: false, value: null, error: message };
  }
}

function getPlaygroundTarget() {
  const state = getPlaygroundState();
  const protocol = state.protocol;
  const account = currentAccount();
  const sid = app.sessionID;
  const definition = protocol === "opq" ? SERVICE_BY_KEY.opq_http : SERVICE_BY_KEY.onebot_http;
  const service = app.transports?.services?.[definition.key] || null;
  const draft = getProtocolDraft(state);
  const blocked = [];
  let relativePath = "";
  let actionLabel = "";
  let onebotAction = "";

  if (!sid) blocked.push("没有当前会话。");
  if (!app.transports) blocked.push("传输快照不可用，无法确认 HTTP 入口是否启用。");
  else if (app.transportsStale || app.transportsError) blocked.push("传输快照已过期，无法确认端点当前是否可用；请等待同步恢复后再发送。");
  if (protocol === "opq" && !account) blocked.push("当前会话尚未绑定 QQ 账号，OPQ 请求无法按 qq 路由。");
  if (protocol === "onebot") {
    if (draft.mode === "envelope") {
      let envelope = null;
      try {
        envelope = JSON.parse(draft.body || "{}");
      } catch {
        // JSON status handles the detailed error.
      }
      onebotAction = typeof envelope?.action === "string" ? envelope.action : "";
      actionLabel = onebotAction || "根信封";
      if (!onebotAction) blocked.push("OneBot 根信封需要非空 action 字段。");
    } else {
      onebotAction = String(draft.action || "").trim();
      actionLabel = onebotAction || "未指定动作";
      if (!onebotAction) blocked.push("OneBot 动作不能为空。");
      else if (!/^[A-Za-z0-9_]+$/.test(onebotAction)) blocked.push("OneBot 动作只能包含字母、数字与下划线。");
    }
  } else {
    actionLabel = "MagicCgiCmd";
  }

  if (sid) {
    if (protocol === "opq") {
      relativePath = `/accounts/${encodeURIComponent(sid)}/v1/LuaApiCaller?funcname=MagicCgiCmd&timeout=10&qq=${encodeURIComponent(account || "QQ")}`;
    } else if (draft.mode === "envelope") {
      relativePath = `/accounts/${encodeURIComponent(sid)}/onebot/v11/`;
    } else {
      relativePath = `/accounts/${encodeURIComponent(sid)}/onebot/v11/${encodeURIComponent(onebotAction || "action")}`;
    }
  }
  if (sid) {
    const relay = new URL("/console/api/relay", location.origin);
    relay.searchParams.set("session", sid);
    relay.searchParams.set("protocol", protocol);
    if (protocol === "onebot" && draft.mode !== "envelope") relay.searchParams.set("action", onebotAction);
    relativePath = relay.pathname + relay.search;
  }
  const actualURL = relativePath ? new URL(relativePath, location.origin).toString() : "";
  const externalURL = combineEndpoint(service);

  if (service) {
    if (!service.enabled) blocked.push(`${definition.label} 当前未启用，请在协议设置中启用并应用。`);
    else if (!service.listening) blocked.push(`${definition.label} 已启用但尚未监听。`);
  } else if (app.transports) {
    blocked.push(`传输快照没有返回 ${definition.label} 服务。`);
  }

  return {
    ok: blocked.length === 0,
    reason: blocked.join(" "),
    actualURL,
    externalURL,
    service,
    definition,
    actionLabel,
    onebotAction,
    account,
    protocol,
    sid,
  };
}

function renderPlaygroundTargets() {
  const target = getPlaygroundTarget();
  const requestNode = $("api-request-url");
  const externalNode = $("api-external-url");
  if (requestNode) requestNode.textContent = target.actualURL || "不可用";
  if (externalNode) externalNode.textContent = target.externalURL || "—";
  renderExternalAuthPresentation();
  const warning = $("api-transport-warning");
  if (warning) {
    const authNote = app.security?.http?.enabled
      ? " HTTP 鉴权已启用，外部客户端需发送 Authorization: Bearer <HTTP_TOKEN>。"
      : "";
    warning.className = "api-warning" + (target.ok ? " is-ok" : " is-blocked");
    warning.replaceChildren(
      icon(target.ok ? "i-info" : "i-warning"),
      el("span", "", target.ok
        ? `端点可用。控制台测试经受保护的同源 relay，浏览器不需要外部 Token${target.service?.port > 0 ? `；独立端口 ${target.service.port} 也由该入口代理` : ""}。外部客户端使用“外部客户端端点”。${authNote}`
        : target.reason || "当前端点不可用。"),
    );
  }
  const send = $("send-api");
  if (send) {
    const state = getPlaygroundState();
    send.disabled = !target.ok || state.sending;
    const label = send.querySelector("span");
    if (label) label.textContent = state.sending ? "正在发送…" : "发送请求";
  }
}

function updatePlaygroundFromInput() {
  const state = getPlaygroundState();
  const draft = getProtocolDraft(state);
  draft.body = $("api-body").value;
  updateJsonStatus();
}

function formatPlaygroundJson() {
  const state = getPlaygroundState();
  const draft = getProtocolDraft(state);
  try {
    const value = JSON.parse(draft.body || "{}");
    draft.body = JSON.stringify(value, null, 2);
    $("api-body").value = draft.body;
    updateJsonStatus();
    showToast("JSON 已格式化", "success");
  } catch (error) {
    showToast(friendlyError(error, "JSON 语法错误"), "error");
    updateJsonStatus();
  }
}

function toggleOnebotMode(mode) {
  const state = getPlaygroundState();
  const draft = state.onebot;
  if (draft.mode === mode) return;
  let parsed = null;
  try {
    parsed = JSON.parse(draft.body || "{}");
  } catch {
    // Leave the text intact; the JSON state remains visible to the user.
  }
  if (parsed) {
    if (mode === "envelope") {
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed) && typeof parsed.action === "string" && parsed.action) {
        // Already an envelope.
      } else {
        draft.body = JSON.stringify({ action: draft.action || "get_status", params: parsed ?? {} }, null, 2);
      }
    } else if (parsed && typeof parsed === "object" && !Array.isArray(parsed) && typeof parsed.action === "string") {
      draft.action = parsed.action;
      draft.body = JSON.stringify(parsed.params ?? {}, null, 2);
    }
  }
  draft.mode = mode;
  renderPlayground();
}

function getPlaygroundAction(state = getPlaygroundState()) {
  if (state.protocol === "opq") return "MagicCgiCmd";
  const draft = state.onebot;
  if (draft.mode === "envelope") {
    try {
      const envelope = JSON.parse(draft.body || "{}");
      return typeof envelope.action === "string" ? envelope.action : "envelope";
    } catch {
      return "envelope";
    }
  }
  return String(draft.action || "").trim();
}

function detectProtocolError(protocol, payload) {
  if (!payload || typeof payload !== "object") return "";
  if (payload.CgiBaseResponse && typeof payload.CgiBaseResponse === "object") {
    const ret = safeNumber(payload.CgiBaseResponse.Ret);
    if (ret !== null && ret !== 0) return payload.CgiBaseResponse.ErrMsg || `OPQ Ret=${ret}`;
  }
  if (payload.retcode !== undefined && safeNumber(payload.retcode) !== null && Number(payload.retcode) !== 0) {
    return payload.wording || payload.message || `OneBot retcode=${payload.retcode}`;
  }
  if (payload.status !== undefined && payload.status !== "ok") {
    return payload.wording || payload.message || `OneBot status=${payload.status}`;
  }
  if (payload.error && typeof payload.error === "string") return payload.error;
  return "";
}

function buildProtocolError(result) {
  if (!result) return "";
  if (result.transportError) return result.transportError;
  return detectProtocolError(result.protocol, result.parsed);
}

async function sendPlaygroundRequest() {
  const state = getPlaygroundState();
  if (state.sending) return;
  const target = getPlaygroundTarget();
  if (!target.ok) {
    showToast(target.reason || "当前端点不可用", "error");
    return;
  }
  const draft = getProtocolDraft(state);
  const parsed = updateJsonStatus();
  if (!parsed.ok) {
    showToast(parsed.error || "请求体不是有效 JSON", "error");
    $("api-body").focus();
    return;
  }
  const action = getPlaygroundAction(state);
  const custom = draft.template === "custom";
  const dangerous = DANGEROUS_ACTION.test(`${action} ${draft.body}`);
  if (custom || dangerous) {
    const confirmed = await openConfirmDialog({
      title: dangerous ? "确认发送可能写入的请求？" : "确认发送自定义请求？",
      message:
        `账号：${target.account ? "QQ " + target.account : "未绑定"}\n` +
        `会话：${target.sid}\n` +
        `协议：${state.protocol === "opq" ? "OPQ" : "OneBot v11"}\n` +
        `动作：${action || target.actionLabel}\n` +
        `地址：${target.actualURL}\n\n` +
        (dangerous
          ? "该请求可能改变联系人、群或消息状态。请确认账号与目标参数无误。"
          : "这是自定义 JSON 请求，可能触发未预期的业务动作。"),
      confirmLabel: dangerous ? "确认发送写操作" : "确认发送",
      danger: dangerous,
    });
    if (!confirmed) return;
    if (app.sessionID !== target.sid) {
      showToast("会话已切换，请求未发送", "error");
      return;
    }
    const freshTarget = getPlaygroundTarget();
    if (!freshTarget.ok || freshTarget.actualURL !== target.actualURL) {
      showToast(freshTarget.reason || "端点状态已变化，请求未发送", "error");
      renderPlaygroundTargets();
      return;
    }
  }

  const sid = target.sid;
  const epoch = app.epoch;
  const owner = getPlaygroundState(sid);
  owner.sending = true;
  state.sending = true;
  state.result = null;
  renderPlaygroundTargets();
  const controller = new AbortController();
  app.playgroundAbort = controller;
  let timedOut = false;
  const timeoutTimer = window.setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, 30000);
  const started = performance.now();
  const requestBody = draft.body;
  const result = {
    at: Date.now(),
    protocol: state.protocol,
    template: draft.template,
    action,
    status: 0,
    duration: 0,
    ok: false,
    url: target.actualURL,
    requestBody,
    responseText: "",
    parsed: null,
    protocolError: "",
    transportError: "",
  };
  try {
    const response = await fetch(target.actualURL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: requestBody,
      credentials: "same-origin",
      cache: "no-store",
      signal: controller.signal,
    });
    if (response.status === 401 && target.actualURL.includes("/console/api/relay")) {
      const lost = await consoleSessionLost();
      if (lost) return;
    }
    const raw = await response.text();
    result.status = response.status;
    result.ok = response.status >= 200 && response.status < 300;
    let parsedPayload = null;
    if (raw) {
      try {
        parsedPayload = JSON.parse(raw);
        result.responseText = truncateText(JSON.stringify(parsedPayload, null, 2), 12000);
      } catch {
        result.responseText = truncateText(raw, 12000);
      }
    } else {
      result.responseText = "（空响应）";
    }
    result.protocolError = detectProtocolError(state.protocol, parsedPayload);
  } catch (error) {
    if (isAbort(error)) {
      if (timedOut) {
        result.transportError = "请求超时（30 秒），未获得响应";
        result.responseText = result.transportError;
      } else {
        if (app.sessionID === sid && app.epoch === epoch) showToast("请求已取消", "info");
        owner.sending = false;
        state.sending = false;
        if (app.playgroundAbort === controller) app.playgroundAbort = null;
        window.clearTimeout(timeoutTimer);
        if (app.sessionID === sid && app.epoch === epoch) renderPlaygroundTargets();
        return;
      }
    } else {
      result.transportError = friendlyError(error, "网络请求失败");
      result.responseText = result.transportError;
    }
  } finally {
    window.clearTimeout(timeoutTimer);
    result.duration = Math.round(performance.now() - started);
  }
  owner.sending = false;
  state.sending = false;
  if (app.playgroundAbort === controller) app.playgroundAbort = null;

  owner.history.unshift(result);
  if (owner.history.length > MAX_HISTORY) owner.history.length = MAX_HISTORY;
  owner.result = result;
  if (app.sessionID === sid && app.epoch === epoch) {
    state.result = result;
    state.sending = false;
    renderApiResult(state);
    renderApiHistory(state);
    renderPlaygroundTargets();
    if (result.protocolError || !result.ok) showToast(result.protocolError || result.transportError || "请求返回了错误响应", "error");
    else showToast("请求已发送并收到响应", "success");
  }
}

function renderApiResult(state = getPlaygroundState()) {
  const result = state.result;
  const output = $("api-response");
  const status = $("api-http-status");
  const duration = $("api-duration");
  const protocolError = $("api-proto-error");
  const used = $("api-used-url");
  if (!result) {
    if (status) status.textContent = "—";
    if (duration) duration.textContent = "—";
    if (protocolError) {
      protocolError.hidden = true;
      protocolError.textContent = "";
    }
    if (used) used.textContent = "实际调用地址将在这里显示。";
    if (output) output.textContent = "尚未发送请求。选择一个模板，确认请求体后手动发送。";
    return;
  }
  if (status) {
    status.textContent = result.status ? String(result.status) : "网络错误";
    status.className = result.ok ? "response-ok" : "response-bad";
  }
  if (duration) duration.textContent = durationMsLabel(result.duration);
  const error = buildProtocolError(result);
  if (protocolError) {
    protocolError.textContent = error;
    protocolError.hidden = !error;
  }
  if (used) used.textContent = result.url || "—";
  if (output) output.textContent = result.responseText || "（无响应内容）";
}

function renderApiHistory(state = getPlaygroundState()) {
  const list = $("api-history");
  const empty = $("api-history-empty");
  if (!list) return;
  if (!state.history.length) {
    list.replaceChildren();
    empty.hidden = false;
    return;
  }
  empty.hidden = true;
  const fragment = document.createDocumentFragment();
  state.history.forEach((entry, index) => {
    const button = el("button", "history-item");
    button.type = "button";
    button.dataset.historyIndex = String(index);
    const status = el("span", "history-status " + (entry.ok && !buildProtocolError(entry) ? "is-ok" : "is-error"), entry.status ? String(entry.status) : "ERR");
    const main = el("span", "history-main");
    main.append(el("strong", "", `${entry.protocol === "opq" ? "OPQ" : "OneBot"} · ${entry.action || "请求"}`), el("small", "", entry.url || "—"));
    button.append(status, main, el("span", "history-time", clockLabel(entry.at)));
    fragment.append(button);
  });
  list.replaceChildren(fragment);
}

function bindPlayground() {
  $$(".protocol-tab").forEach((tab) => {
    tab.addEventListener("click", () => switchProtocol(tab.dataset.protocol));
    tab.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      const tabs = $$(".protocol-tab");
      const index = tabs.indexOf(tab);
      const next = tabs[(index + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length];
      next.focus();
      switchProtocol(next.dataset.protocol);
    });
  });
  $("api-template").addEventListener("change", (event) => selectApiTemplate(event.target.value));
  $("api-body").addEventListener("input", updatePlaygroundFromInput);
  $("format-json").addEventListener("click", formatPlaygroundJson);
  $("onebot-action").addEventListener("input", (event) => {
    const state = getPlaygroundState();
    state.onebot.action = event.target.value;
    renderPlaygroundTargets();
  });
  $("onebot-mode").addEventListener("change", (event) => toggleOnebotMode(event.target.value));
  $("send-api").addEventListener("click", sendPlaygroundRequest);
  $("api-history").addEventListener("click", (event) => {
    const button = event.target.closest("[data-history-index]");
    if (!button) return;
    const state = getPlaygroundState();
    const result = state.history[Number(button.dataset.historyIndex)];
    if (!result) return;
    state.result = result;
    renderApiResult(state);
    showToast("已载入本地历史响应", "success");
  });
  $("clear-history").addEventListener("click", () => {
    const state = getPlaygroundState();
    state.history = [];
    state.result = null;
    renderApiResult(state);
    renderApiHistory(state);
  });
  $("ws-endpoint").addEventListener("change", (event) => {
    const state = getPlaygroundState();
    state.wsEndpoint = event.target.value;
    state.wsError = "";
    renderWs(state);
  });
  $("ws-connect").addEventListener("click", connectPlaygroundSocket);
  $("ws-disconnect").addEventListener("click", () => disconnectPlaygroundSocket("用户断开测试", false));
  $("clear-ws-events").addEventListener("click", () => {
    const state = getPlaygroundState();
    state.wsEvents = [];
    renderWs(state);
  });
}

function getWsTarget(endpointKey) {
  const state = getPlaygroundState();
  const definition = SERVICE_BY_KEY[endpointKey];
  const service = app.transports?.services?.[endpointKey] || null;
  const account = currentAccount();
  const sid = app.sessionID;
  const blocked = [];
  if (!sid) blocked.push("没有当前会话。");
  if (!app.transports) blocked.push("传输快照不可用，无法确认 WebSocket 入口是否启用。");
  else if (app.transportsStale || app.transportsError) blocked.push("传输快照已过期，无法确认 WebSocket 端点当前是否可用；请等待同步恢复后再连接。");
  if (endpointKey === "opq_ws" && !account) blocked.push("当前会话尚未绑定 QQ 账号，OPQ WebSocket 无法按 qq 路由。");
  if (service) {
    if (!service.enabled) blocked.push(`${definition.label} 当前未启用，请在协议设置中启用并应用。`);
    else if (!service.listening) blocked.push(`${definition.label} 已启用但尚未监听。`);
  } else if (app.transports) {
    blocked.push(`传输快照没有返回 ${definition.label} 服务。`);
  }
  let relativePath = "";
  if (sid) {
    if (endpointKey === "opq_ws") {
      relativePath = `/accounts/${encodeURIComponent(sid)}/ws?qq=${encodeURIComponent(account || "QQ")}`;
    } else {
      relativePath = `/accounts/${encodeURIComponent(sid)}/onebot/v11/ws`;
    }
  }
  let actualURL = "";
  if (relativePath) {
    const relay = new URL("/console/api/socket", location.origin);
    relay.searchParams.set("session", sid);
    relay.searchParams.set("protocol", endpointKey === "opq_ws" ? "opq" : endpointKey === "onebot12_ws" ? "onebot12" : "onebot");
    relativePath = relay.pathname + relay.search;
    const url = new URL(relativePath, location.origin);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    actualURL = url.toString();
  }
  return {
    ok: blocked.length === 0,
    reason: blocked.join(" "),
    actualURL,
    externalURL: combineEndpoint(service),
    service,
    definition,
    endpointKey,
    sid,
    account,
    state,
  };
}

function renderWs(state = getPlaygroundState()) {
  if (!$("ws-endpoint")) return;
  const select = $("ws-endpoint");
  const endpoint = state.wsEndpoint || "opq_ws";
  select.value = endpoint;
  const target = getWsTarget(endpoint);
  const connection = app.wsConnection && app.wsConnection.sid === app.sessionID ? app.wsConnection : null;
  const isOpen = Boolean(connection);
  select.disabled = isOpen;
  $("ws-connect").disabled = isOpen;
  $("ws-disconnect").disabled = !isOpen;
  $("ws-url").textContent = target.actualURL || "—";
  const externalNode = $("ws-external-url");
  if (externalNode) externalNode.textContent = target.externalURL || "—";
  renderExternalAuthPresentation();

  let statusText = "尚未连接";
  let dot = "unknown";
  if (connection) {
    statusText = connection.statusText;
    dot = connection.state === "open" ? "online" : connection.state === "error" ? "error" : "busy";
  } else if (state.wsError) {
    statusText = state.wsError;
    dot = "error";
  } else if (state.wsNote) {
    statusText = state.wsNote;
    dot = "unknown";
  } else if (!target.ok) {
    statusText = "端点当前不可用";
    dot = "error";
  }
  $("ws-status").textContent = statusText;
  setStatusDot($("ws-dot"), dot);
  $("ws-error").textContent = state.wsError || "";
  $("ws-error").hidden = !state.wsError;

  const events = state.wsEvents || [];
  const output = $("ws-events");
  const wasAtBottom = output.scrollTop + output.clientHeight >= output.scrollHeight - 6;
  const previousScroll = output.scrollTop;
  if (!events.length) {
    output.textContent = "连接后，收到的事件会显示在这里。缓冲区最多保留 80 条。";
  } else {
    output.textContent = events
      .slice(-MAX_WS_EVENTS)
      .map((event) => `${clockLabel(event.at)} · ${bytesLabel(event.size)}\n${event.text}`)
      .join("\n\n");
  }
  output.scrollTop = wasAtBottom ? output.scrollHeight : previousScroll;
  $("ws-event-count").textContent = `${events.length} / ${MAX_WS_EVENTS} 条事件`;
}

function connectPlaygroundSocket() {
  if (app.wsConnection) return;
  const state = getPlaygroundState();
  const target = getWsTarget(state.wsEndpoint || "opq_ws");
  if (!target.ok) {
    state.wsError = target.reason;
    state.wsNote = "";
    renderWs(state);
    showToast(target.reason || "WebSocket 端点不可用", "error");
    return;
  }
  state.wsError = "";
  state.wsNote = "";
  let socket;
  try {
    socket = new WebSocket(target.actualURL);
  } catch (error) {
    state.wsError = friendlyError(error, "无法创建 WebSocket 连接");
    renderWs(state);
    showToast(state.wsError, "error");
    return;
  }
  const connection = {
    sid: app.sessionID,
    endpoint: state.wsEndpoint,
    socket,
    state: "connecting",
    statusText: "正在连接…",
    openedAt: 0,
    closedAt: 0,
    intentional: false,
  };
  app.wsConnection = connection;
  renderWs(state);

  socket.addEventListener("open", () => {
    if (app.wsConnection !== connection) return;
    connection.state = "open";
    connection.openedAt = Date.now();
    connection.statusText = `已连接 · ${clockLabel(connection.openedAt)}`;
    const owner = getPlaygroundState(connection.sid);
    owner.wsNote = "";
    owner.wsError = "";
    if (app.sessionID === connection.sid) renderWs(owner);
  });
  socket.addEventListener("message", (event) => {
    if (app.wsConnection !== connection) return;
    const owner = getPlaygroundState(connection.sid);
    const raw = typeof event.data === "string" ? event.data : "[非文本事件]";
    let size = raw.length;
    try {
      size = new TextEncoder().encode(raw).length;
    } catch {
      // Fall back to character count if TextEncoder is unavailable.
    }
    let text = truncateText(raw, 8000);
    try {
      const parsed = JSON.parse(raw);
      text = truncateText(JSON.stringify(parsed, null, 2), 8000);
    } catch {
      // Keep the original text event.
    }
    owner.wsEvents.push({ at: Date.now(), size, text });
    if (owner.wsEvents.length > MAX_WS_EVENTS) owner.wsEvents.splice(0, owner.wsEvents.length - MAX_WS_EVENTS);
    if (app.sessionID === connection.sid) renderWs(owner);
  });
  socket.addEventListener("error", () => {
    if (app.wsConnection !== connection) return;
    connection.state = "error";
    connection.statusText = "连接错误（浏览器未提供更多信息）";
    const owner = getPlaygroundState(connection.sid);
    owner.wsError = "WebSocket 连接发生错误；请检查服务是否启用并监听。";
    if (app.sessionID === connection.sid) renderWs(owner);
    consoleSessionLost();
  });
  socket.addEventListener("close", (event) => {
    if (app.wsConnection !== connection) return;
    connection.state = "closed";
    connection.closedAt = Date.now();
    const parts = [`已关闭 · ${clockLabel(connection.closedAt)}`];
    if (event.code) parts.push(`code ${event.code}`);
    if (event.reason) parts.push(event.reason);
    connection.statusText = parts.join(" · ");
    const owner = getPlaygroundState(connection.sid);
    owner.wsNote = connection.statusText;
    owner.wsError = "";
    app.wsConnection = null;
    if (app.sessionID === connection.sid) renderWs(owner);
  });
}

function disconnectPlaygroundSocket(reason, dueSwitch) {
  const connection = app.wsConnection;
  if (!connection) return;
  const owner = getPlaygroundState(connection.sid);
  owner.wsNote = dueSwitch ? `已关闭：${reason}` : "正在断开测试连接…";
  owner.wsError = "";
  connection.intentional = true;
  connection.state = "closing";
  connection.statusText = owner.wsNote;
  try {
    connection.socket.close(1000, String(reason || "close").slice(0, 120));
  } catch {
    // The close handler, if any, will settle the state.
  }
  if (dueSwitch) {
    app.wsConnection = null;
    if (app.sessionID === connection.sid) renderWs(owner);
  } else if (app.sessionID === connection.sid) {
    renderWs(owner);
  }
}

function closePlaygroundSocket(reason, dueSwitch = false) {
  if (!app.wsConnection) return;
  disconnectPlaygroundSocket(reason, dueSwitch);
}

function abortPlaygroundRequest() {
  if (app.playgroundAbort) {
    try {
      app.playgroundAbort.abort();
    } catch {
      // ignore
    }
    app.playgroundAbort = null;
  }
}

function abortDiagnostics() {
  if (app.diagAbort) {
    try {
      app.diagAbort.abort();
    } catch {
      // ignore
    }
    app.diagAbort = null;
  }
  app.debugBusy = false;
}

async function refreshDiagnostics() {
  if (!app.authenticated || !app.booted || app.authLoggingOut || app.debugBusy) return;
  if (!app.sessionID) {
    renderDiagnostics();
    return;
  }
  const sid = app.sessionID;
  const epoch = app.epoch;
  const authGen = app.authGeneration;
  const controller = new AbortController();
  app.diagAbort = controller;
  app.debugBusy = true;
  app.debug = null;
  renderDiagnostics();

  const jobs = [
    {
      key: "status",
      run: () => controlApi("status", { session: sid, signal: controller.signal, timeout: 10000 }),
    },
    {
      key: "signer",
      run: () => controlApi("signer", { scoped: false, signal: controller.signal, timeout: 8000 }),
    },
    {
      key: "transports",
      run: () => controlApi("transports", { session: sid, signal: controller.signal, timeout: 10000 }),
    },
    {
      key: "diagnostics",
      run: () => controlApi("diagnostics", { session: sid, signal: controller.signal, timeout: 10000 }),
    },
  ];

  const entries = {};
  await Promise.all(
    jobs.map(async (job) => {
      const started = performance.now();
      try {
        const value = await job.run();
        entries[job.key] = {
          ok: true,
          value,
          duration_ms: Math.round(performance.now() - started),
          error: "",
        };
      } catch (error) {
        if (isAbort(error)) {
          entries[job.key] = {
            ok: false,
            value: null,
            duration_ms: Math.round(performance.now() - started),
            error: "请求已取消",
            aborted: true,
          };
          return;
        }
        entries[job.key] = {
          ok: false,
          value: null,
          duration_ms: Math.round(performance.now() - started),
          error: friendlyError(error, "请求失败"),
        };
      }
    }),
  );

  if (!app.authenticated || app.authGeneration !== authGen || app.sessionID !== sid || app.epoch !== epoch) return;
  if (controller.signal.aborted) {
    app.debugBusy = false;
    if (app.diagAbort === controller) app.diagAbort = null;
    return;
  }
  app.debug = {
    sessionID: sid,
    fetchedAt: Date.now(),
    entries,
  };
  app.debugBusy = false;
  if (app.diagAbort === controller) app.diagAbort = null;

  if (entries.status?.ok) {
    applyStatus(entries.status.value);
  }
  if (entries.signer?.ok) {
    app.signer = entries.signer.value;
    app.signerAt = Date.now();
    app.signerFetchError = "";
    renderSigner();
  } else if (entries.signer) {
    app.signerFetchError = entries.signer.error || "无法读取签名健康";
    app.signerAt = Date.now();
    renderSigner();
  }
  if (entries.transports?.ok) {
    const normalized = normalizeTransports(entries.transports.value);
    if (normalized) applyTransportSnapshot(normalized, { origin: "diagnostics" });
  }
  renderDiagnostics();
}

function debugEntry(key) {
  return app.debug?.entries?.[key] || null;
}

function renderDiagnostics() {
  if (!app.debugBusy && !app.debug) {
    for (const key of ["api", "signer", "transports", "diagnostics"]) {
      setHealthCard(key, "等待检查", "点击“刷新检查”读取真实数据", "idle");
    }
  } else if (app.debugBusy) {
    for (const key of ["api", "signer", "transports", "diagnostics"]) {
      setHealthCard(key, "检查中…", "正在读取真实接口", "busy");
    }
  } else if (app.debug) {
    const status = debugEntry("status");
    const signer = debugEntry("signer");
    const transports = debugEntry("transports");
    const diagnostics = debugEntry("diagnostics");

    if (status?.ok) {
      const phase = phaseInfo(status.value);
      setHealthCard("api", "读取成功", `状态 ${phase.label} · ${durationMsLabel(status.duration_ms)}`, "ok");
    } else {
      setHealthCard("api", "读取失败", status?.error || "无法读取控制台状态", "error");
    }

    if (signer?.ok && signer.value?.ready) {
      setHealthCard("signer", "健康", `${durationMsLabel(signer.duration_ms)} · ${signer.value.adapter || "适配器已连接"}`, "ok");
    } else if (signer?.ok) {
      setHealthCard("signer", "未就绪", signer.value?.error || "签名服务报告未就绪", "error");
    } else {
      setHealthCard("signer", "读取失败", signer?.error || "无法读取签名健康", "error");
    }

    if (transports?.ok) {
      const services = Object.values(transports.value?.services || {});
      const enabled = services.filter((service) => service?.enabled).length;
      setHealthCard("transports", "已读取", `${enabled}/4 入口启用 · ${durationMsLabel(transports.duration_ms)}`, "ok");
    } else {
      setHealthCard("transports", "读取失败", transports?.error || "无法读取传输快照", "error");
    }

    if (diagnostics?.ok) {
      setHealthCard("diagnostics", "已读取", `采集 ${dateTimeLabel(diagnostics.value?.captured_at)}`, "ok");
    } else {
      setHealthCard("diagnostics", "读取失败", diagnostics?.error || "无法读取诊断数据", "error");
    }
  }
  renderDiagnosticsSummary();
  renderDebugJson();
}

function renderDiagnosticsSummary() {
  const entry = debugEntry("diagnostics");
  const value = entry?.ok ? entry.value : null;
  $("diag-goroutines").textContent = value ? numberOrDash(value.goroutines) : "—";
  $("diag-heap").textContent = value ? bytesLabel(value.heap_alloc_bytes) : "—";
  $("diag-sys").textContent = value ? bytesLabel(value.heap_sys_bytes) : "—";
  $("diag-uptime").textContent = value ? durationLabel(value.uptime_seconds) : "—";
  $("diag-sessions").textContent = value
    ? `${numberOrDash(value.session_count)} / ${numberOrDash(value.online_count)}`
    : "—";
  $("diag-version").textContent = value
    ? `${value.go_version || "—"} · ${value.platform || "—"}/${value.arch || "—"}`
    : "—";
  const meta = $("diagnostics-meta");
  if (app.debug?.fetchedAt) {
    meta.textContent = `采集于 ${clockLabel(app.debug.fetchedAt)} · 会话 ${sessionShort(app.debug.sessionID)}`;
  } else if (app.debugBusy) {
    meta.textContent = "正在读取诊断…";
  } else {
    meta.textContent = "等待读取";
  }
}

function renderDebugJson() {
  const tabs = $$(".debug-tab");
  tabs.forEach((tab) => {
    const active = tab.dataset.debugTab === app.debugTab;
    tab.classList.toggle("active", active);
    tab.setAttribute("aria-selected", active ? "true" : "false");
    tab.tabIndex = active ? 0 : -1;
  });
  const node = $("debug-json");
  if (!node) return;
  const entry = debugEntry(app.debugTab);
  if (!entry) {
    app.debugText = "";
    node.textContent = app.debugBusy ? "正在读取真实状态…" : "点击“刷新检查”读取真实状态。";
    return;
  }
  const content = entry.ok
    ? redactForExport(entry.value)
    : {
        error: entry.error || "请求失败",
        aborted: Boolean(entry.aborted),
        duration_ms: entry.duration_ms,
      };
  app.debugText = JSON.stringify(
    {
      tab: app.debugTab,
      fetched_at: app.debug?.fetchedAt ? new Date(app.debug.fetchedAt).toISOString() : "",
      duration_ms: entry.duration_ms,
      data: content,
    },
    null,
    2,
  );
  node.textContent = app.debugText;
}

function bindDebugTabs() {
  document.querySelector(".debug-tabs").addEventListener("click", (event) => {
    const tab = event.target.closest("[data-debug-tab]");
    if (!tab) return;
    app.debugTab = tab.dataset.debugTab;
    renderDebugJson();
  });
  document.querySelector(".debug-tabs").addEventListener("keydown", (event) => {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    const tabs = $$(".debug-tab");
    const current = event.target.closest("[data-debug-tab]");
    if (!current) return;
    event.preventDefault();
    const index = tabs.indexOf(current);
    const next = tabs[(index + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length];
    next.focus();
    app.debugTab = next.dataset.debugTab;
    renderDebugJson();
  });
}

function bindDiagnosticsButtons() {
  $("refresh-diagnostics").addEventListener("click", refreshDiagnostics);
  $("export-diagnostics").addEventListener("click", exportDiagnostics);
}

function redactForExport(value, key = "") {
  if (key && SENSITIVE_KEY.test(key)) return "[已隐藏]";
  if (typeof value === "string") {
    if (value.startsWith("data:image/")) return "[二维码已隐藏]";
    return value;
  }
  if (Array.isArray(value)) return value.map((item) => redactForExport(item, key));
  if (value && typeof value === "object") {
    const output = {};
    for (const [childKey, childValue] of Object.entries(value)) {
      output[childKey] = redactForExport(childValue, childKey);
    }
    return output;
  }
  return value;
}

async function exportDiagnostics() {
  if (!app.debug || app.debug.sessionID !== app.sessionID) {
    await refreshDiagnostics();
    if (!app.debug || app.debug.sessionID !== app.sessionID) {
      showToast("诊断数据尚不可用", "error");
      return;
    }
  }
  const entries = {};
  for (const [key, entry] of Object.entries(app.debug.entries || {})) {
    entries[key] = entry?.ok
      ? { ok: true, duration_ms: entry.duration_ms, data: redactForExport(entry.value) }
      : { ok: false, duration_ms: entry.duration_ms, error: entry.error, aborted: Boolean(entry.aborted) };
  }
  const payload = {
    product: "openopq",
    exported_at: new Date().toISOString(),
    session_id: app.sessionID,
    account: currentAccount() ? `QQ ${currentAccount()}` : null,
    note: "Diagnostics are read-only. Sensitive fields including GUID, QR, tokens and credentials are redacted. Memory values are for the console gateway process, not the signer SDK.",
    entries,
  };
  const text = JSON.stringify(payload, null, 2);
  const blob = new Blob([text], { type: "application/json;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  const stamp = new Date().toISOString().replace(/[:.]/g, "-");
  anchor.href = url;
  anchor.download = `openopq-diagnostics-${stamp}.json`;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  showToast("脱敏诊断已导出", "success");
}

function renderInitialState() {
  renderStatus();
  renderLoginSurface();
  renderSettings({ force: true });
  renderSecurity();
  renderPlayground();
  paintLogs(true);
  renderSigner();
  renderDiagnostics();
  renderAccountCounts();
  updatePauseButton();
  updateSyncLabel();
  renderSessionDock();
  setConnection("pending", "正在连接");
}

function bindAllEvents() {
  bindNavigation();
  bindSessionControls();
  bindCopyButtons();
  bindHeaderActions();
  bindConfirmDialog();
  bindAddDialog();
  bindOverviewTransportCopy();
  bindTransportSettings();
  bindLogs();
  bindPlayground();
  bindAccountGrids();
  bindDebugTabs();
  bindDiagnosticsButtons();
  bindAuthEvents();
  bindSecuritySettings();
  bindOneBotSettings();bindOneBot12Settings();
  bindMobileNavigation();
  $("login-form").addEventListener("submit", submitLogin);
  $("qq-account").addEventListener("input", () => {
    app.localLoginError = "";
    renderLoginSurface();
  });
  $("cancel-login").addEventListener("click", cancelLogin);
  $("disconnect").addEventListener("click", disconnectSession);
  $("account-disconnect").addEventListener("click", disconnectSession);
  $("resume-login").addEventListener("click", restoreSavedLogin);
  $("forget-login").addEventListener("click", forgetSavedLogin);
  $("log-search").value = app.logSearch;
  $("debug-json").textContent = "点击“刷新检查”读取真实状态。";
  updatePauseButton();
  $$(".log-filter").forEach((button) => {
    button.setAttribute("aria-pressed", button.classList.contains("selected") ? "true" : "false");
  });
  window.addEventListener("online", () => {
    if (!app.authenticated || !app.booted) return;
    showToast("网络已恢复，正在重新同步", "success");
    poll();
    fetchSigner();
  });
  window.addEventListener("offline", () => {
    if (!app.authenticated) return;
    setConnection("offline", "浏览器离线");
    showStale("浏览器当前离线。已保留最后一次真实数据，恢复后会继续同步。");
  });
  window.addEventListener("beforeunload", () => closePlaygroundSocket("页面关闭", true));
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && app.authenticated && app.booted && !app.authLoggingOut) {
      poll();
      fetchSigner();
    }
  });
}

function installSchedules() {
  if (app.schedulesInstalled) return;
  app.schedulesInstalled = true;
  window.setInterval(tickClock, 1000);
  window.setInterval(() => {
    if (app.authenticated && app.booted && !app.authLoggingOut) poll();
  }, POLL_INTERVAL);
  window.setInterval(() => {
    if (app.authenticated && app.booted && !app.authLoggingOut) fetchSigner();
  }, SIGNER_INTERVAL);
}

async function bootstrap() {
  if (!app.authenticated) return;
  const authGen = app.authGeneration;
  const stillActive = () => app.authenticated && !app.authLoggingOut && app.authGeneration === authGen;
  setConnection("pending", "正在连接");
  try {
    const list = await fetchSessions();
    if (!stillActive()) return;
    applySessions(list);
    if (!stillActive()) return;
    setConnection("ok", "服务已连接");
    app.lastSync = Date.now();
    updateSyncLabel();
    hideStale();
  } catch (error) {
    if (!stillActive()) return;
    setConnection("offline", "连接中断");
    showStale("无法读取会话列表。请确认本地控制台仍在运行；页面会继续尝试同步。");
    app.statusError = "无法读取会话列表";
    renderStatus();
  }
  if (!stillActive()) return;
  app.booted = true;
  if (app.view === "debug") refreshDiagnostics();
  poll();
  fetchSigner();
  loadSecurity({ silent: true });
}

function init() {
  bindAllEvents();
  tickClock();
  setViewFromHash();
  renderInitialState();
  installSchedules();
  checkAuthStatus();
}

if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init, { once: true });
else init();
