import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
const $ = (id) => document.getElementById(id);
const state = { authenticated: false, setup: false, sessions: [], recent: [], selected: null, socket: null, terminal: null, fit: null, attachment: 0, terminalState: null, view: "sessions", loading: false };
state.node = "";
state.restartToken = 0;
const encoder = new TextEncoder();
const pendingRestarts = new Map();
function notice(message) {
  $("notice").textContent = message;
  $("notice").hidden = !message;
}
function terminalNotice(message) {
  $("terminal-notice").textContent = message;
  $("terminal-notice").hidden = !message;
}
function node(tag, text, className) {
  const element = document.createElement(tag);
  if (text !== void 0) element.textContent = text;
  if (className) element.className = className;
  return element;
}
function button(text, action, className) {
  const element = node("button", text, className);
  element.type = "button";
  element.addEventListener("click", () => Promise.resolve(action()).catch((error) => notice(error.message)));
  return element;
}
const list = (value, key) => Array.isArray(value) ? value : Array.isArray(value?.[key]) ? value[key] : [];
async function api(path, body, method) {
  const endpoint = new URL(path, location.origin);
  if (state.node && /^\/api\/(sessions|recent|packages)(\/|$)/.test(endpoint.pathname)) endpoint.searchParams.set("node", state.node);
  const response = await fetch(endpoint, { method: method || (body === void 0 ? "GET" : "POST"), credentials: "same-origin", headers: body === void 0 ? {} : { "Content-Type": "application/json" }, body: body === void 0 ? void 0 : JSON.stringify(body) });
  if (response.status === 401) {
    state.authenticated = false;
    showAuth();
  }
  const text = await response.text();
  let data;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (!response.ok) {
    const error = new Error(typeof data?.error === "string" ? data.error : text.slice(0, 240) || `Request failed (${response.status})`);
    error.status = response.status;
    throw error;
  }
  return data;
}
function showAuth() {
  closeTerminal();
  $("app").hidden = true;
  $("auth").hidden = false;
  $("auth-title").textContent = state.setup ? "Set up your gateway" : "Welcome back";
  $("auth-description").textContent = state.setup ? "Create the root account for this gateway." : "Sign in to your sessions.";
  $("setup-field").hidden = !state.setup;
  $("setup-token").required = state.setup;
  $("password").minLength = state.setup ? 12 : 1;
  $("password").autocomplete = state.setup ? "new-password" : "current-password";
  $("auth-submit").textContent = state.setup ? "Create root account" : "Sign in";
}
async function bootstrap() {
  try {
    const status = await api("/api/status");
    state.setup = !!status.setup_required;
    state.authenticated = !!status.authenticated;
    if (state.authenticated) {
      $("auth").hidden = true;
      $("app").hidden = false;
      await refresh();
    } else showAuth();
  } catch (error) {
    showAuth();
    $("auth-error").textContent = error.message;
  }
}
$("auth-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const submit = $("auth-submit");
  submit.disabled = true;
  $("auth-error").textContent = "";
  try {
    await api(state.setup ? "/api/setup" : "/api/login", { password: $("password").value, ...state.setup ? { token: $("setup-token").value.trim() } : {} });
    $("password").value = "";
    $("setup-token").value = "";
    const returnTo = new URLSearchParams(location.search).get("return_to");
    if (returnTo && returnTo.startsWith("/") && !returnTo.startsWith("//")) {
      const destination = new URL(returnTo, location.origin);
      if (destination.origin === location.origin && destination.pathname === "/oauth/authorize") {
        location.assign(destination.href);
        return;
      }
    }
    await bootstrap();
    await refreshNodes();
  } catch (error) {
    $("auth-error").textContent = error.message;
  } finally {
    submit.disabled = false;
  }
});
$("logout").addEventListener("click", async () => {
  try {
    await api("/api/logout", {});
    state.authenticated = false;
    showAuth();
  } catch (error) {
    notice(error.message);
  }
});
async function refresh() {
  if (!state.authenticated || state.loading) return;
  state.loading = true;
  try {
    const selectedNode = state.node;
    const [sessions, recent] = await Promise.all([api("/api/sessions"), api("/api/recent")]);
    if (state.node !== selectedNode) return;
    if (!state.authenticated) return;
    state.sessions = list(sessions, "sessions");
    state.recent = list(recent, "recent");
    renderSessions();
    renderRecent();
    $("gateway-state").textContent = "Gateway connected";
  } catch (error) {
    $("gateway-state").textContent = "Connection interrupted";
    notice(error.message);
  } finally {
    state.loading = false;
  }
}
function renderSessions() {
  const host = $("sessions");
  host.replaceChildren();
  $("session-count").textContent = state.sessions.filter((session) => session.state?.running).length;
  $("session-empty").hidden = state.sessions.length > 0;
  for (const session of state.sessions) {
    const card = node("article", void 0, "session-card" + (session.id === state.selected ? " active" : ""));
    const top = node("div", void 0, "card-top");
    top.append(node("span", session.harness || "Agent", "agent-name"), node("span", session.state?.running ? "Running" : "Stopped", "badge"));
    card.append(top, node("p", session.directory || "Unknown folder", "path"), node("p", `${session.mode || "default"}${session.node_name ? " \xB7 " + session.node_name : ""}`, "session-meta"));
    const actions = node("div", void 0, "actions");
    const open = button("Open terminal", () => openTerminal(session), "primary");
    open.disabled = !session.state?.running || session.state?.interactive === false;
    actions.append(open);
    if (session.state?.running) {
      const restart = button("Restart", () => changeSession(session, "restart"));
      restart.disabled = session.state?.interactive === false;
      actions.append(restart, button("Stop", () => changeSession(session, "stop"), "danger"));
    } else {
      const start = button("Start fresh", () => startDialog(session));
      start.disabled = session.harness === "agy";
      actions.append(start);
    }
    if (session.harness === "agy") card.append(node("p", "Desktop launcher presence · start and interact locally. Detached desktop windows are not tracked.", "session-meta"));
    else if (session.state?.interactive === false) card.append(node("p", "Native stdio · browser input unavailable", "session-meta"));
    card.append(actions);
    host.append(card);
  }
}
async function changeSession(session, action) {
  if (!window.confirm(action === "stop" ? "Stop this agent? Its current conversation may end." : "Restart this agent? Its current conversation may end unless the agent supports resume.")) return;
  const key = JSON.stringify([state.node, session.id]);
  let body = {};
  if (action === "restart") {
    if (!pendingRestarts.has(key)) pendingRestarts.set(key, crypto.randomUUID());
    body = {operation_id: pendingRestarts.get(key)};
  }
  try {
    await api(`/api/sessions/${encodeURIComponent(session.id)}/${action}`, body);
  } catch (error) {
    if (error.status >= 400 && error.status < 500) pendingRestarts.delete(key);
    throw error;
  }
  pendingRestarts.delete(key);
  notice(action === "stop" ? "Agent stopped." : "Agent restart requested.");
  await refresh();
}
function renderRecent() {
  $("recent-folders").replaceChildren(...state.recent.filter(recent => recent.available !== false).map(recent => { const option=node("option"); option.value=recent.directory; return option; }));
  const host = $("recent");
  host.replaceChildren();
  if (!state.recent.length) {
    host.append(node("p", "Folders will appear here after you launch an agent.", "muted"));
    return;
  }
  for (const recent of state.recent) {
    const row = node("article", void 0, "list-row");
    const content = node("div");
    content.append(node("p", recent.directory, "path"), node("p", `${recent.harness || "claude"} \xB7 ${recent.mode || "default"}`, "muted"));
    const actions = node("div", undefined, "actions");
    const start = button("Start fresh", () => startDialog(recent));
    start.disabled = recent.harness === "agy" || recent.available === false;
    if (recent.harness === "agy") content.append(node("p", "Desktop launches must start locally.", "hint"));
    if (recent.available === false) content.append(node("p", "Unavailable · " + (recent.unavailable_reason || "Folder cannot be used"), "hint"));
    const lastUsed = new Date(recent.last_used);
    if (!Number.isNaN(lastUsed.getTime())) {
      const time = node("time", "Last used " + lastUsed.toLocaleString(), "hint");
      time.dateTime = lastUsed.toISOString();
      content.append(time);
    }
    actions.append(start,button("Forget", async () => {
      await api("/api/recent",{directory:recent.directory,harness:recent.harness,mode:recent.mode},"DELETE");
      await refresh();
    }));
    row.append(content, actions);
    host.append(row);
  }
}
function setView(view) {
  state.view = view;
  if (view === "settings") loadSettings();
  for (const name of ["sessions", "recent", "packages", "settings"]) $(`${name}-view`).hidden = name !== view;
  document.querySelectorAll("[data-view]").forEach((element) => {
    const active = element.dataset.view === view;
    element.classList.toggle("selected", active);
    if (active) element.setAttribute("aria-current", "page");
    else element.removeAttribute("aria-current");
  });
  $("view-title").textContent = { sessions: "Sessions", recent: "Recent folders", packages: "Packages", settings: "Gateway settings" }[view];
}
document.querySelectorAll("[data-view]").forEach((element) => element.addEventListener("click", () => setView(element.dataset.view)));
function startDialog(recent = {}) {
  $("start-directory").value = recent.directory || "";
  $("start-harness").value = recent.harness || "claude";
  $("start-mode").value = recent.mode || "default";
  $("start-error").textContent = "";
  $("start-dialog").showModal();
  $("start-directory").focus();
}
$("new-session").addEventListener("click", () => startDialog());
$("empty-start").addEventListener("click", () => startDialog());
$("cancel-start").addEventListener("click", () => $("start-dialog").close());
$("start-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const submit = event.target.querySelector("[type=submit]");
  submit.disabled = true;
  try {
    const session = await api("/api/sessions", { directory: $("start-directory").value.trim(), harness: $("start-harness").value, mode: $("start-mode").value.trim() });
    $("start-dialog").close();
    setView("sessions");
    await refresh();
    if (session?.id) openTerminal(session);
  } catch (error) {
    $("start-error").textContent = error.message;
  } finally {
    submit.disabled = false;
  }
});
async function loadPackages() {
  const directory = $("package-directory").value.trim();
  if (!directory) return;
  const packages = list(await api("/api/packages?directory=" + encodeURIComponent(directory)), "packages");
  const host = $("packages");
  host.replaceChildren();
  if (!packages.length) host.append(node("p", "No packages installed in this project.", "muted"));
  for (const pkg of packages) {
    const row = node("div", void 0, "list-row");
    row.append(node("strong", pkg.module || pkg.name || String(pkg)), node("span", pkg.commit ? pkg.commit.slice(0, 12) : "", "path"));
    host.append(row);
  }
}
$("package-directory-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    await loadPackages();
    $("package-status").textContent = "";
  } catch (error) {
    $("package-status").textContent = error.message;
  }
});
$("package-install-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const directory = $("package-directory").value.trim();
  if (!directory) {
    $("package-directory").reportValidity();
    return;
  }
  const submit = event.target.querySelector("button");
  submit.disabled = true;
  $("package-status").textContent = "Installing package and restarting affected agents\u2026";
  try {
    await api("/api/packages", { directory, spec: $("package-spec").value.trim() });
    $("package-spec").value = "";
    $("package-status").textContent = "Package installed. Affected agents have been restarted.";
    await Promise.all([loadPackages(), refresh()]);
  } catch (error) {
    $("package-status").textContent = error.message;
  } finally {
    submit.disabled = false;
  }
});
function controls() {
  return state.socket?.readyState === WebSocket.OPEN && !!state.attachment && state.terminalState?.owner === state.attachment && state.terminalState?.running;
}
function updateTerminalState(value) {
  if (value) state.terminalState = value;
  const current = state.terminalState;
  const owned = controls();
  $("take-control").textContent = owned ? "Release control" : "Take control";
  $("take-control").disabled = !current?.running || !owned && !!current.owner;
  $("terminal-status").textContent = !current?.running ? "Agent exited" + (current?.exit_code !== void 0 ? ` (${current.exit_code})` : "") : owned ? "You have control" : current.owner ? "Observing \xB7 another terminal has control" : "Observing \xB7 control available";
  document.querySelectorAll("[data-key]").forEach((element) => element.disabled = !owned);
  if (state.terminal) state.terminal.options.disableStdin = !owned;
}
function sendControl(action, extra = {}) {
  if (state.socket?.readyState === WebSocket.OPEN) state.socket.send(JSON.stringify({ action, epoch: state.terminalState?.epoch || 0, ...extra }));
}
function fit() {
  if (!state.terminal || !state.fit || $("terminal-panel").hidden) return;
  state.fit.fit();
  if (controls()) sendControl("resize", { rows: state.terminal.rows, columns: state.terminal.cols });
}
function sendInput(data) {
  if (!controls()) return;
  if (state.socket.bufferedAmount > 1024 * 1024) {
    terminalNotice("Connection is busy. Wait before sending more input.");
    return;
  }
  const bytes = typeof data === "string" ? encoder.encode(data) : data;
  for (let offset = 0; offset < bytes.length; offset += 32768) state.socket.send(bytes.subarray(offset, offset + 32768));
}
function closeTerminal() {
  state.restartToken++;
  const socket = state.socket;
  state.socket = null;
  socket?.close();
  state.terminal?.dispose();
  state.terminal = null;
  state.fit = null;
  state.selected = null;
  state.attachment = 0;
  state.terminalState = null;
  $("terminal-panel").hidden = true;
}
function openTerminal(session) {
  closeTerminal();
  state.selected = session.id;
  $("terminal-title").textContent = `${session.harness || "Agent"} \xB7 ${session.directory || ""}`;
  $("terminal-panel").hidden = false;
  $("terminal-status").textContent = "Connecting\u2026";
  $("take-control").disabled = true;
  terminalNotice("");
  state.terminal = new Terminal({ cursorBlink: true, fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace", fontSize: 13, scrollback: 5e3, allowProposedApi: false, disableStdin: true, screenReaderMode: true, theme: { background: "#191a1d", foreground: "#ececef", cursor: "#ececef" } });
  state.fit = new FitAddon();
  state.terminal.loadAddon(state.fit);
  state.terminal.open($("terminal"));
  fit();
  state.terminal.onData(sendInput);
  state.terminal.onBinary((data) => sendInput(Uint8Array.from(data, (character) => character.charCodeAt(0))));
  const url = new URL(`/api/sessions/${encodeURIComponent(session.id)}/terminal`, location.href);
  if (state.node) url.searchParams.set("node", state.node);
  url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const socket = new WebSocket(url);
  state.socket = socket;
  socket.binaryType = "arraybuffer";
  let pendingOutput = 0;
  socket.onmessage = (event) => {
    if (state.socket !== socket) return;
    if (event.data instanceof ArrayBuffer) {
      pendingOutput += event.data.byteLength;
      if (pendingOutput > 16 * 1024 * 1024) {
        socket.close();
        terminalNotice("Output exceeded this browser\u2019s buffer. Reconnect to catch up.");
        return;
      }
      const length = event.data.byteLength;
      state.terminal.write(new Uint8Array(event.data), () => pendingOutput -= length);
      return;
    }
    try {
      const message = JSON.parse(event.data);
      if (message.attachment) state.attachment = message.attachment;
      const previouslyOwned = controls();
      if (message.type === "attached" && message.offset) terminalNotice("Earlier output was truncated. Showing the available terminal history.");
      if (message.error) terminalNotice(message.error);
      if (message.type === "ended" && message.state?.restarting) reconnectAfterRestart(session);
      if (message.state) updateTerminalState(message.state);
      if (!previouslyOwned && controls()) {
        fit();
        state.terminal.focus();
      }
    } catch {
      terminalNotice("The terminal sent an invalid control message.");
      socket.close();
    }
  };
  socket.onclose = () => {
    if (state.socket !== socket) return;
    state.attachment = 0;
    updateTerminalState();
    if (state.terminalState?.restarting) $("terminal-status").textContent = "Restarting · waiting to reconnect";
    else if (state.terminalState?.running !== false) $("terminal-status").textContent = "Disconnected · reconnect to attach again";
    $("take-control").disabled = true;
  };
  socket.onerror = () => {
    if (state.socket === socket) terminalNotice("Could not connect to this terminal. Refresh the session list and try again.");
  };
  renderSessions();
  $("terminal-panel").scrollIntoView({ block: "nearest" });
}
async function reconnectAfterRestart(session) {
  const token = state.restartToken;
  const deadline = Date.now() + 90000;
  terminalNotice("Agent restarting. Waiting to reconnect…");
  while (Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 250));
    if (state.restartToken !== token || !state.authenticated) return;
    try {
      const sessions = list(await api("/api/sessions"), "sessions");
      if (state.restartToken !== token) return;
      const replacement = sessions.find(value => value.id === session.id);
      if (replacement?.state && !replacement.state.restarting) {
        openTerminal(replacement);
        terminalNotice("Agent restarted. This is a new process; conversation resume depends on the agent’s launch options. Reconnected as an observer.");
        return;
      }
    } catch {
      // A restarting worker may briefly be unavailable before its endpoint returns.
    }
  }
  if (state.restartToken === token) terminalNotice("The agent has not become available. Reconnect when its restart completes.");
}

let sharingEnabled=false;
async function loadSettings() {
  try {
    const settings=await api("/api/settings");
    sharingEnabled=!!settings.sharing_enabled;
    $("settings-platform").textContent=settings.platform || "Gateway machine";
    $("settings-running").textContent=settings.running?"Running":"Stopped";
    $("settings-sharing").textContent=sharingEnabled?"Enabled for allowed Tailscale identities":"Disabled. Local CLI sessions still work.";
    $("settings-allowlist").textContent=settings.allowed_peers?.length?settings.allowed_peers.join(", "):"No additional machines or tags";
    $("toggle-sharing").textContent=sharingEnabled?"Stop sharing":"Enable sharing";
    $("toggle-sharing").disabled=false;
  } catch(error) { $("settings-status").textContent=error.message; }
}
$("toggle-sharing").addEventListener("click",async()=>{
  $("toggle-sharing").disabled=true;
  try {
    await api("/api/settings/sharing",{enabled:!sharingEnabled});
    $("settings-status").textContent="Sharing change applies within five seconds. Local CLI sessions keep running.";
    await loadSettings(); await refreshNodes();
  } catch(error) { $("settings-status").textContent=error.message; $("toggle-sharing").disabled=false; }
});
$("stop-all-agents").addEventListener("click",async()=>{
  if(!confirm("Stop every agent on the gateway machine? Their current conversations may end."))return;
  try { await api("/api/node/stop-agents",{}); $("settings-status").textContent="Agents on the gateway machine have been stopped."; await refresh(); }
  catch(error) { $("settings-status").textContent=error.message; }
});
$("stop-gateway").addEventListener("click",async()=>{
  if(!confirm("Stop this gateway and disconnect browser access? Existing CLI sessions will keep running. Use agentpack gateway start on the machine to reconnect."))return;
  try { await api("/api/node/stop",{}); closeTerminal(); state.authenticated=false; $("settings-running").textContent="Stopped"; $("gateway-state").textContent="Gateway stopped"; $("settings-status").textContent="Gateway stopped. CLI sessions keep running. Run agentpack gateway start on the gateway machine to reconnect."; $("toggle-sharing").disabled=true; $("stop-all-agents").disabled=true; $("stop-gateway").disabled=true; }
  catch(error) { $("settings-status").textContent=error.message; }
});
$("take-control").addEventListener("click", () => sendControl(controls() ? "release" : "take"));
$("close-terminal").addEventListener("click", () => {
  closeTerminal();
  renderSessions();
});
$("reconnect").addEventListener("click", () => {
  const session = state.sessions.find((session2) => session2.id === state.selected);
  if (session) openTerminal(session);
  else notice("This session is no longer available.");
});
const keys = { escape: "\x1B", tab: "	", interrupt: "", up: "\x1B[A", down: "\x1B[B" };
document.querySelectorAll("[data-key]").forEach((element) => element.addEventListener("click", () => {
  sendInput(keys[element.dataset.key]);
  state.terminal?.focus();
}));
let resizeTimer;
new ResizeObserver(() => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(fit, 100);
}).observe($("terminal"));
$("refresh").addEventListener("click", () => {
  notice("");
  refresh();
});
setInterval(() => {
  if (!document.hidden) refresh();
}, 5e3);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refresh();
});
async function refreshNodes() {
  if (!state.authenticated) return;
  try {
    const nodes = list(await api("/api/nodes"), "nodes");
    const select = $("node-select");
    select.replaceChildren(node("option", "Gateway machine"));
    select.firstChild.value = "";
    for (const remote of nodes) {
      const option = node("option", remote.name || remote.id);
      option.value = remote.id;
      select.append(option);
    }
    if (state.node && !nodes.some(remote => remote.id === state.node)) {
      const offline = node("option", "Selected machine is unavailable");
      offline.value = state.node;
      select.append(offline);
    }
    select.value = state.node;
  } catch (error) { notice(error.message); }
}
$("node-select").addEventListener("change", async event => {
  closeTerminal();
  state.node = event.target.value;
  state.sessions = [];
  state.recent = [];
  $("package-directory").value = "";
  $("packages").replaceChildren();
  $("package-status").textContent = "";
  renderSessions(); renderRecent();
  while (state.loading) await new Promise(resolve => setTimeout(resolve, 50));
  await refresh();
});
setInterval(() => { if (!document.hidden) refreshNodes(); }, 15000);
bootstrap().then(refreshNodes);
