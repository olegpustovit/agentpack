import { Terminal } from "@xterm/xterm";
const $ = (id) => document.getElementById(id);
const esc = value => String(value ?? "").replace(/[&<>"']/g, char => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"})[char]);
const state = { authenticated: false, setup: false, sessions: [], recent: [], selected: null, socket: null, terminal: null, attachment: 0, terminalState: null, view: "workspace", loading: false };
state.node = "";
state.nodes = [];
state.sessionCache = new Map();
state.recentByNode = new Map();
state.grouping = "machine";
state.collapsedGroups = new Set();
state.zoom = "fit";
state.sidebarWidth = 264;
state.refreshGeneration = 0;
state.restartToken = 0;
state.inventory = new Map();
state.inventoryAt = 0;
state.libraryByNode = new Map();
state.libraryBusy = false;
state.libraryGeneration = 0;
state.libraryKind = "packages";
state.librarySelection = "";
state.revisionSelection = "";
state.machineSelection = "";
state.inventoryGeneration = 0;
state.retries = 0;
state.drafts = [];
state.replication = {peers:[],contacts:{}};
state.attachmentGeneration = 0;
state.replayComplete = true;
state.firstFrameMarked = false;
const systemAppearance=matchMedia("(prefers-color-scheme: dark)");
function terminalTheme(){
  const dark=document.documentElement.dataset.theme==="dark"||(!document.documentElement.dataset.theme&&systemAppearance.matches);
  return dark?{background:"#171b22",foreground:"#e6e8ed",cursor:"#e6e8ed",selectionBackground:"#ffffff40"}:{
    background:"#ffffff",foreground:"#202124",cursor:"#202124",selectionBackground:"#0969da40",
    black:"#202124",red:"#a31515",green:"#116329",yellow:"#7d4e00",blue:"#0550ae",magenta:"#8250df",cyan:"#006b75",white:"#d0d7de",
    brightBlack:"#57606a",brightRed:"#cf222e",brightGreen:"#1a7f37",brightYellow:"#9a6700",brightBlue:"#0969da",brightMagenta:"#a475f9",brightCyan:"#007c8a",brightWhite:"#ffffff"
  };
}
function applyTerminalTheme(){
  const theme=terminalTheme();
  document.documentElement.style.setProperty("--terminal-bg",theme.background);
  document.documentElement.style.setProperty("--terminal-fg",theme.foreground);
  if(state.terminal)state.terminal.options.theme=theme;
}
systemAppearance.addEventListener("change",applyTerminalTheme);
const encoder = new TextEncoder();
const pendingRestarts = new Map();
function notice(message) {
  $("notice").textContent = message;
  $("notice").hidden = !message;
}
function terminalMark(name) {
  if (globalThis.performance?.mark) performance.mark(`agentpack:terminal:${name}`);
  globalThis.console?.debug?.("agentpack terminal timing",name,Math.round(performance.now()));
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
async function api(path, body, method, target = state.node) {
  const endpoint = new URL(path, location.origin);
  if (target && /^\/api\/(sessions|recent|packages|library)(\/|$)/.test(endpoint.pathname)) endpoint.searchParams.set("node", target);
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
  if (!state.authenticated) return;
  const generation = ++state.refreshGeneration;
  state.loading = !state.sessionsLoaded;
  if (state.loading) renderSessions();
  const machines = [{id:"", name:"Gateway machine", online:true}, ...state.nodes];
  const merge = () => {
    const values = machines.flatMap(machine => state.sessionCache.get(machine.id) || []);
    if (generation !== state.refreshGeneration) return;
    state.sessions = values;
    const active = values.find(session => sessionKey(session) === state.selected);
    if (active?.stale) {
      $("terminal-status").textContent = "Offline · last-known session";
      $("connection-state").textContent = "Offline";
      $("terminal-stage").classList.add("stale");
    } else if (active && state.replayComplete) $("terminal-stage").classList.remove("stale");
    renderSessions();
  };
  await Promise.all(machines.filter(machine => machine.online !== false).map(async machine => {
    try {
      const sessions = await api("/api/sessions", void 0, void 0, machine.id);
      if (generation !== state.refreshGeneration) return;
      const rows = list(sessions, "sessions").map(session => ({...session, node:machine.id, machine:machine.name, stale:false}));
      state.sessionCache.set(machine.id, rows);
      merge();
      try {
        const recent = await api("/api/recent", void 0, void 0, machine.id);
        if (generation !== state.refreshGeneration) return;
        state.recentByNode.set(machine.id, list(recent, "recent"));
        if (!machine.id) state.recent = list(recent, "recent");
        renderRecent();
        if (machine.id === state.node) renderStartFolders();
      } catch (error) { if (!machine.id) notice(error.message); }
    } catch (error) {
      if (!machine.id) { notice(error.message); return; }
      const cached = state.sessionCache.get(machine.id) || [];
      state.sessionCache.set(machine.id, cached.map(session => ({...session, stale:true})));
      merge();
    }
  }));
  if (generation === state.refreshGeneration) {
    state.sessionsLoaded = true;
    state.loading = false;
    renderSessions();
    $("gateway-state").textContent = "Gateway connected";
    $("mesh-state").textContent = `${machines.filter(machine => machine.online !== false).length} of ${machines.length} machines online`;
  }
}
const basename = path => String(path || "Unknown project").replace(/[\\/]+$/, "").split(/[\\/]/).pop() || path;
const sessionName = session => session.name || basename(session.directory);
function renderSessions() {
  const host = $("sessions");
  const query = $("session-search").value.trim().toLowerCase();
  const rows = state.sessions.filter(session => [session.name, session.directory, session.harness, session.mode, session.machine].join(" ").toLowerCase().includes(query));
  const projectKey = session => `${session.node || ""}\0${session.directory || "Unknown project"}`;
  const groupKey = session => state.grouping === "machine" ? session.node || "" : projectKey(session);
  const group = (values, key) => values.reduce((map, value) => map.set(key(value), [...(map.get(key(value)) || []), value]), new Map());
  document.querySelectorAll("[data-group]").forEach(button => button.classList.toggle("on", button.dataset.group === state.grouping));
  host.replaceChildren();
  for (const [key, items] of group(rows, groupKey)) {
    const section = node("details", void 0, "group");
    const collapseKey = `${state.grouping}\0${key}`;
    section.open = !!query || !state.collapsedGroups.has(collapseKey);
    section.addEventListener("toggle", () => {
      if (!section.isConnected || query) return;
      if (section.open) state.collapsedGroups.delete(collapseKey); else state.collapsedGroups.add(collapseKey);
    });
    const heading = node("summary", void 0, "group-label");
    heading.title = state.grouping === "machine" ? (items[0].machine || "Gateway machine") : items[0].directory || key;
    heading.innerHTML = svgIcon("chevron").replace('<svg ', '<svg class="chev" ') + svgIcon(state.grouping === "machine" ? "laptop" : "folder");
    heading.append(node("span", state.grouping === "machine" ? (items[0].machine || "Gateway machine") : basename(items[0].directory), "grow"));
    if (state.grouping === "machine") heading.append(node("span", items[0].stale ? "Offline" : "Online", "group-state"));
    section.append(heading);
    host.append(section);
    const projects = group(items, state.grouping === "machine" ? projectKey : session => session.node || "");
    for (const [project, sessions] of projects) {
      const subhead = node("div", void 0, "subhead");
      subhead.innerHTML = svgIcon(state.grouping === "machine" ? "folder" : "laptop");
      subhead.append(node("span", state.grouping === "machine" ? basename(sessions[0].directory) : sessions[0].machine || "Gateway machine"));
      subhead.title = sessions[0].directory || "Unknown project";
      section.append(subhead);
      for (const session of sessions) {
        const running = session.state?.running;
        const status = session.stale ? "Offline" : running ? "Running" : "Stopped";
        const row = node("button", void 0, `row srow${sessionKey(session) === state.selected ? " sel" : ""}${!running || session.stale ? " offline" : ""}`);
        row.type = "button";
        row.dataset.session = session.id;
        row.setAttribute("aria-current", String(sessionKey(session) === state.selected));
        row.title = `${sessionName(session)} · ${session.directory || ""} · ${session.machine || "Gateway machine"} · ${status}`;
        const dot = node("span", void 0, `dot ${running && !session.stale ? "ok" : "off"}`);
        dot.setAttribute("role", "img"); dot.setAttribute("aria-label", status);
        row.append(dot, node("span", sessionName(session), "label"), node("span", session.harness || "Agent", "agent"));
        row.addEventListener("click", () => { terminalMark("click"); openTerminal(session); });
        row.addEventListener("contextmenu", event => { event.preventDefault(); showSessionMenu(session, row, event); });
        row.addEventListener("keydown", event => {
          if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) { event.preventDefault(); showSessionMenu(session, row); }
          if (event.key === "F2" && !session.stale) { event.preventDefault(); renameSession(session); }
        });
        const more = button("", () => showSessionMenu(session, more), "session-more");
        more.innerHTML = svgIcon("more");
        more.setAttribute("aria-label", `Actions for ${sessionName(session)}`);
        more.setAttribute("aria-haspopup", "menu");
        more.title = "Session actions";
        const entry = node("div", void 0, "session-entry");
        entry.append(row, more); section.append(entry);
      }
    }
  }
  $("session-empty").hidden = rows.length > 0;
  $("sessions").setAttribute("aria-busy", String(state.loading));
  const empty = $("session-empty").querySelector("p");
  empty.classList.toggle("loading-indicator", state.loading);
  empty.textContent = state.loading ? "Loading sessions…" : query ? "No sessions match your filter." : "No sessions yet.";
  const active = state.sessions.find(session => sessionKey(session) === state.selected);
  if (active) $("terminal-title").textContent = sessionName(active);
}
$("session-search").addEventListener("input", renderSessions);
document.querySelectorAll("[data-group]").forEach(button => button.addEventListener("click", () => { state.grouping = button.dataset.group; renderSessions(); }));
$("sessions").addEventListener("keydown", event => {
  if (!['ArrowDown','ArrowUp'].includes(event.key)) return;
  const rows = [...$("sessions").querySelectorAll("details[open] [data-session]")];
  const current = rows.findIndex(row => row === event.target || row.classList.contains("sel"));
  const next = rows[Math.max(0, Math.min(rows.length - 1, current + (event.key === "ArrowDown" ? 1 : -1)))];
  if (next) { event.preventDefault(); next.click(); requestAnimationFrame(() => $("sessions").querySelector("[data-session].sel")?.focus()); }
});
const sessionKey = (session) => JSON.stringify([session.node || "", session.id]);
async function changeSession(session, action) {
  if (!window.confirm(action === "stop" ? "Stop this agent? Its current conversation may end." : "Restart this agent? Its current conversation may end unless the agent supports resume.")) return;
  const key = sessionKey(session);
  let body = {};
  if (action === "restart") {
    if (!pendingRestarts.has(key)) pendingRestarts.set(key, crypto.randomUUID());
    body = {operation_id: pendingRestarts.get(key)};
  }
  try {
    await api(`/api/sessions/${encodeURIComponent(session.id)}/${action}`, body, void 0, session.node || "");
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
  if (!["workspace","sessions","library","machines","settings","recent","packages"].includes(view)) return;
  state.view = view === "sessions" ? "workspace" : view;
  $("app").dataset.current = view === "sessions" ? "workspace" : view;
  $("workspace").hidden = !["workspace","sessions"].includes(view);
  $("library").hidden = view !== "library"; $("machines").hidden = view !== "machines";
  $("recent-view").hidden = view !== "recent"; $("packages-view").hidden = view !== "packages"; $("settings-view").hidden = view !== "settings";
  if (view === "settings") { loadSettings(); void loadReplication(); }
  if (view === "recent") renderRecent();
  if (view === "library") { renderLibrary(); void loadWorkspaceData(); }
  if (view === "machines") { renderMachines(); void loadWorkspaceData(); }
  if (view === "packages") { const selected=state.sessions.find(session => sessionKey(session) === state.selected); $("package-directory").value=selected?.directory || ""; if(selected) state.node=selected.node || ""; }
  document.querySelectorAll("[data-view]").forEach(element => {
    const active = element.dataset.view === view || element.dataset.view === "workspace" && view === "sessions";
    element.classList.toggle("on", active);
    if (active) element.setAttribute("aria-current", "page"); else element.removeAttribute("aria-current");
  });
  if (state.view === "workspace") requestAnimationFrame(scaleTerminal);
}
document.querySelectorAll("[data-view]").forEach(element => element.addEventListener("click", () => setView(element.dataset.view)));
const machines = () => [{id:"",name:"Gateway machine",online:true},...state.nodes];
const machineName = id => machines().find(machine => machine.id === id)?.name || (id ? id : "Gateway machine");
const destinationKey = (nodeId,directory) => `${nodeId}\0${directory}`;
async function mapLimit(items, limit, work) {
  let next=0;
  await Promise.all(Array.from({length:Math.min(limit,items.length)},async()=>{
    while(next<items.length){const index=next++;await work(items[index]);}
  }));
}
async function loadInventory(force=false) {
  if(!force&&Date.now()-state.inventoryAt<10000)return;
  const generation=(state.inventoryGeneration||0)+1;state.inventoryGeneration=generation;
  await Promise.all(machines().map(async machine=>{
    const folders=state.recentByNode.get(machine.id)||[];
    const targets=folders.map(folder=>({machine,folder}));
    await mapLimit(targets,2,async({machine,folder})=>{
      const key=destinationKey(machine.id,folder.directory),previous=state.inventory.get(key);
      if(machine.online===false||folder.available===false){state.inventory.set(key,{node:machine.id,machine:machine.name,directory:folder.directory,folder_id:folder.folder_id||"",packages:previous?.packages||[],error:folder.unavailable_reason||previous?.error||"Folder unavailable",online:machine.online!==false,available:folder.available!==false,last_used:folder.last_used||"",harness:folder.harness||"",mode:folder.mode||""});return;}
      try{
        const response=await api(`/api/packages?directory=${encodeURIComponent(folder.directory)}`,void 0,"GET",machine.id);
        if(generation!==state.inventoryGeneration)return;
        state.inventory.set(key,{node:machine.id,machine:machine.name,directory:folder.directory,folder_id:folder.folder_id||"",packages:list(response,"packages"),error:"",online:true,available:true,last_used:folder.last_used||"",harness:folder.harness||"",mode:folder.mode||""});
      }catch(error){if(generation===state.inventoryGeneration)state.inventory.set(key,{node:machine.id,machine:machine.name,directory:folder.directory,folder_id:folder.folder_id||"",packages:previous?.packages||[],error:error.message,online:true,available:true,last_used:folder.last_used||"",harness:folder.harness||"",mode:folder.mode||""});}
    });
  }));
  if(generation!==state.inventoryGeneration)return;
  state.inventoryAt=Date.now();
  if(state.view==="library")renderLibrary();if(state.view==="machines")renderMachines();
}
async function loadLibrary(force=false) {
  if(state.libraryBusy&&!force)return;
  const generation=++state.libraryGeneration;state.libraryBusy=true;
  if(state.view==="library")renderLibrary();
  await Promise.all(machines().map(async machine=>{
    if(machine.online===false)return;
    const cached=state.libraryByNode.get(machine.id)||{packages:[],agentpacks:[]};
    const [packages,packs]=await Promise.allSettled([api("/api/library/packages",void 0,"GET",machine.id),api("/api/library/agentpacks",void 0,"GET",machine.id)]);
    const details=packs.status==="fulfilled"?await Promise.all(list(packs.value,"agentpacks").map(async pack=>{
      try{
        const result=await api(`/api/library/agentpacks/${encodeURIComponent(pack.id)}`,void 0,"GET",machine.id);
        return {...result.agentpack,_node:machine.id,_machine:machine.name,_revisions:list(result.revisions,"revisions").slice().sort((a,b)=>Date.parse(b.created_at)-Date.parse(a.created_at))};
      }catch(error){return {...pack,_node:machine.id,_machine:machine.name,_revisions:[],_error:error.message};}
    })):cached.agentpacks;
    const failure=[packages,packs].find(result=>result.status==="rejected")?.reason;
    const error=failure?(failure.status===404?`Update Agentpack on ${machine.name} to load saved agentpacks. Installed packages are still shown.`:`${machine.name}: ${failure.message}`):"";
    if(generation===state.libraryGeneration)state.libraryByNode.set(machine.id,{packages:packages.status==="fulfilled"?list(packages.value,"packages").map(item=>({...item,_node:machine.id,_machine:machine.name,_key:`${machine.id}\0${item.id}`})):cached.packages,agentpacks:details,error});
  }));
  if(generation===state.libraryGeneration){state.libraryBusy=false;if(state.view==="library")renderLibrary();if(state.view==="machines")renderMachines();}
}
async function loadWorkspaceData(force=false){await Promise.all([loadInventory(force),loadLibrary(force),loadDrafts(),loadReplication()]);}
async function loadDrafts(){
  try{state.drafts=list(await api("/api/library/drafts",void 0,"GET",""),"drafts");}
  catch(error){state.draftError=error.message;}
  if(state.view==="library")renderDrafts();
}
async function loadReplication(){
  try{state.replication=await api("/api/library/replication/peers",void 0,"GET","")||{peers:[],contacts:{}};state.replicationError="";}
  catch(error){state.replicationError=error.message;}
  renderReplication();
}
function registryPackages(){
  const byID=new Map();
  for(const item of [...state.libraryByNode.values()].flatMap(value=>value.packages||[])){
    const key=item.module&&item.commit?`${item.module.toLowerCase()}@${item.commit}`:item.id;
    const previous=byID.get(key);if(!previous)byID.set(key,{...item,_key:key,_nodes:[item._node]});else if(!previous._nodes.includes(item._node))previous._nodes.push(item._node);
  }
  const pins=new Set([...byID.values()].map(item=>`${item.module}@${item.commit}`));
  for(const target of destinations())for(const pin of target.packages){
    const key=`${pin.module}@${pin.commit}`;
    if(!pin.module||!pin.commit||pins.has(key))continue;
    pins.add(key);
    byID.set(key,{...pin,_node:target.node,_machine:target.machine,_key:key,_installed:true,project:target.directory,name:pin.name||pin.module,kind:pin.kind||"package"});
  }
  return [...byID.values()];
}
function registryAgentpacks(){
  const byID=new Map();
  for(const pack of [...state.libraryByNode.values()].flatMap(value=>value.agentpacks||[])){
    const previous=byID.get(pack.id);
    if(!previous)byID.set(pack.id,{...pack,_nodes:[pack._node],_revisions:[...(pack._revisions||[])]});
    else{
      previous._nodes.push(pack._node);
      for(const revision of pack._revisions||[])if(!previous._revisions.some(item=>item.id===revision.id))previous._revisions.push(revision);
      previous.heads=[...new Set([...(previous.heads||[]),...(pack.heads||[])])];
    }
  }
  return [...byID.values()].map(pack=>({...pack,_revisions:pack._revisions.slice().sort((a,b)=>Date.parse(b.created_at)-Date.parse(a.created_at))}));
}
function verifiedCopies(id){
  if(!id)return [];
  const copies=[];
  const local=state.libraryByNode.get("");
  if(local?.packages?.some(item=>item.id===id)||local?.agentpacks?.some(pack=>pack._revisions?.some(revision=>revision.id===id)))copies.push({id:"",name:"Gateway machine",online:true});
  for(const peer of state.replication.peers||[])if(peer.digests?.includes(id)){const machine=machines().find(value=>value.id===peer.node);copies.push({id:peer.node,name:machine?.name||peer.node,online:machine?.online!==false});}
  return copies;
}
function copiesLabel(id){const copies=verifiedCopies(id);return copies.length?`Verified on ${copies.map(copy=>copy.name+(copy.online?"":" (offline)")).join(", ")}`:"No verified copies yet";}
function destinations(){
  const out=[];
  for(const machine of machines())for(const folder of state.recentByNode.get(machine.id)||[]){
    const cached=state.inventory.get(destinationKey(machine.id,folder.directory));
    out.push(cached||{node:machine.id,machine:machine.name,directory:folder.directory,folder_id:folder.folder_id||"",packages:[],error:folder.unavailable_reason||"",online:machine.online!==false,available:folder.available!==false,last_used:folder.last_used||"",harness:folder.harness||"",mode:folder.mode||""});
  }
  return out.sort((a,b)=>a.machine.localeCompare(b.machine)||a.directory.localeCompare(b.directory));
}
function knownPackages(){
  const meta=registryPackages(),latest=new Map();
  for(const target of destinations())for(const pin of target.packages){
    if(!pin.module||!pin.commit)continue;
    const found=meta.find(item=>item.module===pin.module&&item.commit===pin.commit);
    const item={...(found||{}),...pin,module:pin.module,commit:pin.commit,_node:found?._node??target.node,_machine:found?._machine??target.machine,_key:found?._key??`${target.node}\0${pin.module}@${pin.commit}`,name:found?.name||pin.name||pin.module,kind:found?.kind||pin.kind||"package",_seen:target.last_used||""};
    const previous=latest.get(item.module);
    if(!previous||Date.parse(item._seen||0)>=Date.parse(previous._seen||0))latest.set(item.module,item);
  }
  for(const item of meta)if(!latest.has(item.module))latest.set(item.module,{...item,name:item.name||item.path||item.module,kind:item.kind||"package"});
  return [...latest.values()].sort((a,b)=>a.name.localeCompare(b.name));
}
function inventoryPin(target,module){return target.packages.find(item=>item.module===module);}
const libraryItemKey=item=>item._key||`${item._node}\0${item.id}`;
function svgIcon(name){
  const paths={package:'<path d="m12 2 9 4.5v11L12 22l-9-4.5v-11L12 2Zm9 4.5L12 11 3 6.5M12 11v11"/>',folder:'<path d="M3 7V5a2 2 0 0 1 2-2h4l3 4h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z"/>',laptop:'<rect x="4" y="4" width="16" height="11" rx="1.5"/><path d="M2 19h20"/>',server:'<rect x="4" y="3" width="16" height="8" rx="2"/><rect x="4" y="13" width="16" height="8" rx="2"/><path d="M8 7h.01M8 17h.01"/>',check:'<path d="m5 12 4 4L19 6"/>',install:'<path d="M12 3v12m-5-5 5 5 5-5M5 21h14"/>',back:'<path d="m15 6-6 6 6 6"/>',chevron:'<path d="m6 9 6 6 6-6"/>',more:'<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',rename:'<path d="m15 4 5 5M4 20l5-1L21 7l-5-5L4 14v6Z"/>',stop:'<rect x="5" y="5" width="14" height="14" rx="2"/>',restart:'<path d="M20 11a8 8 0 1 0-2 6M20 4v7h-7"/>',close:'<path d="m6 6 12 12M6 18 18 6"/>',terminal:'<path d="m5 6 5 5-5 5m8 1h6"/>'};
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths[name]||paths.package}</svg>`;
}
function renderLibrary(){
  const byModule=new Map();
  for(const item of registryPackages()){
    const key=item.module?.toLowerCase()||item.id;
    const previous=byModule.get(key);
    if(previous)previous._versions.push(item);
    else byModule.set(key,{...item,_key:key,_versions:[item]});
  }
  const packages=[...byModule.values()],packs=registryAgentpacks(),kind=state.libraryKind;
  $("packages-tab").textContent=`Packages ${packages.length}`;$("setups-tab").textContent=`Agentpacks ${packs.length}`;
  $("packages-tab").classList.toggle("on",kind==="packages");$("setups-tab").classList.toggle("on",kind==="agentpacks");
  const items=kind==="packages"?packages:packs,query=$("library-search").value.trim().toLowerCase();
  const filtered=items.filter(item=>[item.name,item.module,item.kind,item.commit,...(item._versions||[]).map(version=>version.commit),item.project,item._machine].join(" ").toLowerCase().includes(query));
  if(!filtered.some(item=>libraryItemKey(item)===state.librarySelection))state.librarySelection=libraryItemKey(filtered[0]||{});
  const listHost=$("library-list");listHost.replaceChildren();
  listHost.setAttribute("aria-busy",String(state.libraryBusy));
  if(!filtered.length){const error=[...state.libraryByNode.values()].find(value=>value.error)?.error;const empty=node("div",state.libraryBusy?"Loading library…":error?`Library unavailable: ${error}`:kind==="packages"?"No installed or saved packages found.":"No agentpacks saved yet.",state.libraryBusy?"empty loading-indicator":"empty");empty.setAttribute("role","status");listHost.append(empty);}
  for(const item of filtered){
    const selected=libraryItemKey(item)===state.librarySelection;
    const row=node("button",void 0,`row two${selected?" sel":""}`);row.type="button";row.dataset.item=libraryItemKey(item);row.setAttribute("aria-current",selected?"true":"false");
    const glyph=node("span",void 0,"glyph small-glyph");glyph.innerHTML=svgIcon(kind==="packages"?"package":"folder");
    const label=node("span",void 0,"label");label.append(node("span",item.name||item.path||item.module||"Unnamed package"));
    const heads=item.heads?.length||0;
    label.append(node("small",kind==="packages"?`${item.kind||"Package"} · ${item._versions?.length>1?`${item._versions.length} versions`:String(item.commit||"").slice(0,12)} · ${item._machine} · ${verifiedCopies(item.id).length} copies`:`${item._revisions?.length||0} ${(item._revisions?.length||0)===1?"revision":"revisions"} · ${heads>1?"Two versions to review · ":""}${item._machine}`));
    row.append(glyph,label);row.addEventListener("click",()=>pickLibrary(item,true));listHost.append(row);
  }
  const errors=[...state.libraryByNode.entries()].filter(([,value])=>value.error);
  $("library-summary").textContent=`${packages.length} ${packages.length===1?"package":"packages"} · ${packs.length} ${packs.length===1?"agentpack":"agentpacks"}${errors.length?" · Partial library":""}`;
  if(filtered.length)for(const [,value] of errors)listHost.append(node("p",value.error,"note library-error"));
  renderLibraryDetail(items.find(item=>libraryItemKey(item)===state.librarySelection));renderDrafts();
}
function pickLibrary(item,show=true){
  state.librarySelection=libraryItemKey(item);
  if(state.libraryKind==="agentpacks")state.revisionSelection=item._revisions?.[0]?.id||"";
  renderLibrary();if(show)$("library").classList.add("show-detail");
}
function renderDrafts(){
  const host=$("install-drafts");host.replaceChildren();
  if(!state.drafts.length)return;
  const caption=node("div",void 0,"list-caption");caption.append(node("span","Pending installs"),node("span",String(state.drafts.length)));host.append(caption);
  for(const draft of state.drafts){
    const machine=machines().find(item=>item.id===(draft.node==="local"?"":draft.node)),row=node("div",void 0,"draft");
    row.append(node("span",void 0,`dot ${machine?.online===false?"off":"ok"}`));
    const label=node("div",void 0,"label");label.append(node("span",draft.pack_id?`Restore ${draft.pack_id}`:draft.item),node("small",`${machine?.name||draft.node} / ${draft.directory} · ${draft.reason}`));row.append(label);
    row.append(button("Review…",()=>reviewDraft(draft)));
    row.append(button("Discard",()=>discardDraft(draft),"icon-btn"));host.append(row);
  }
}
async function discardDraft(draft){
  try{await api(`/api/library/drafts/${encodeURIComponent(draft.id)}`,void 0,"DELETE","");await loadDrafts();}
  catch(error){notice(error.message);}
}
function draftDiff(diff){
  if(!diff)return "<p class=note>No current diff is available.</p>";
  return [...(diff.add||[]).map(item=>["add","+","Add",item]),...(diff.update||[]).map(item=>["upd","~","Update",item]),...(diff.remove||[]).map(item=>["del","−","Remove",item])].map(([kind,sign,label,item])=>`<div class="diffline ${kind}"><span class="sign">${sign}</span>${label} <code>${esc(item)}</code></div>`).join("")||'<div class="diffline same"><span class="sign">=</span>No package changes</div>';
}
async function reviewDraft(draft){
  let review;
  try{review=await api(`/api/library/drafts/${encodeURIComponent(draft.id)}/review`,{},"POST","");}
  catch(error){notice(error.message);return;}
  const unavailable=!review.ready;
  showSheet(unavailable?"Draft needs attention":"Review pending change",`<p><strong>${esc(draft.item)}</strong> · ${esc(draft.directory)} on ${esc(machines().find(item=>item.id===(draft.node==="local"?"":draft.node))?.name||draft.node)}</p><p class="${unavailable?"err":"note"}" role="status">${esc(review.reason||draft.reason||"Ready to apply.")}</p>${review.diff?`<section class="sect"><header><h2>Current diff</h2></header>${draftDiff(review.diff)}</section>`:""}${unavailable?"":'<label class="check"><input id="draft-restart" type="checkbox" checked>Restart affected agents after apply</label>'}<p id="draft-error" class="err" role="alert"></p>`,unavailable?`<button type="button" id="draft-discard">Discard draft</button><button type="button" id="draft-close" class="primary">Keep draft</button>`:`<button type="button" id="draft-close">Cancel</button><button type="button" id="draft-apply" class="primary">${draft.pack_id?"Restore":"Install"}</button>`);
  $("draft-close").onclick=closeSheet;$("draft-discard")?.addEventListener("click",async()=>{await discardDraft(draft);closeSheet();});
  $("draft-apply")?.addEventListener("click",async()=>{
    const button=$("draft-apply");button.disabled=true;button.textContent=draft.pack_id?"Restoring…":"Installing…";
    const target=draft.node==="local"?"":draft.node;
    try{
      const restart=$("draft-restart").checked;
      if(draft.pack_id)await api(`/api/library/agentpacks/${encodeURIComponent(draft.pack_id)}/revisions/${encodeURIComponent(draft.revision)}/restore`,{directory:draft.directory,restart},"POST",target);
      else await api("/api/packages",{directory:draft.directory,spec:draft.item,restart},"POST",target);
      await api(`/api/library/drafts/${encodeURIComponent(draft.id)}`,void 0,"DELETE","");closeSheet();await loadDrafts();await loadWorkspaceData(true);
      notice(draft.pack_id?"Agentpack restored.":"Package installed.");
    }catch(error){$("draft-error").textContent=error.message;button.disabled=false;button.textContent=draft.pack_id?"Restore":"Install";}
  });
}
function renderLibraryDetail(item){
  const host=$("library-detail");
  if(!item){const error=[...state.libraryByNode.values()].find(value=>value.error)?.error;host.innerHTML=`<div class="empty">${esc(error?`Library data unavailable: ${error}`:"Select a saved package or agentpack.")}</div>`;return;}
  const back=`<button class="tb-btn back" id="lib-back">${svgIcon("back")}Library</button>`;
  if(state.libraryKind==="packages"){
    const installed=destinations().flatMap(target=>{const pin=inventoryPin(target,item.module);return pin?[{target,pin}]:[];});
    const packUses=registryAgentpacks().filter(pack=>pack._revisions?.some(rev=>rev.package_ids?.includes(item.id)));
    const rows=installed.map(({target,pin})=>`<tr><td>${esc(target.machine)}</td><td title="${esc(target.directory)}">${esc(target.directory.split(/[\\/]/).filter(Boolean).pop()||target.directory)}</td><td><code>${esc(pin.commit||"")}</code>${pin.commit!==item.commit?` <span class="faint">→ ${esc(item.commit)}</span>`:""}</td><td>${verifiedCopies(item.id).some(copy=>copy.id===target.node)?`<span class="cell">${svgIcon("check")}Verified</span>`:"—"}</td><td class="act">${pin.commit===item.commit?"Current":`<button data-update-target="${encodeURIComponent(destinationKey(target.node,target.directory))}" ${target.online&&target.available||target.folder_id?"":"disabled"}>Update</button>`}</td></tr>`).join("");
    host.innerHTML=`${back}<div class="insp wide"><div class="insp-head"><span class="glyph">${svgIcon("package")}</span><div class="title"><h1>${esc(item.name||item.path||item.module)}</h1><p>${esc(item.kind||"Package")} · pinned <code>${esc(item.commit)}</code> · ${item._installed?"installed":"saved"} on ${esc(item._machine)}</p></div><div class="actions"><button class="primary" id="install-item">${svgIcon("install")}Install…</button></div></div><dl class="kv"><dt>Module</dt><dd><code>${esc(item.module)}</code></dd><dt>Repository</dt><dd>${esc(item.url||item.module)}</dd><dt>Content</dt><dd><code>${esc(item.content_hash||"No content hash recorded")}</code></dd><dt>Source project</dt><dd>${esc(item.project||"Unknown project")}</dd><dt>Verified copies</dt><dd>${esc(copiesLabel(item.id))}</dd><dt>In agentpacks</dt><dd>${packUses.length?packUses.map(pack=>`<button class="link-btn" data-pack-key="${encodeURIComponent(libraryItemKey(pack))}">${esc(pack.name)}</button>`).join(", "):"—"}</dd></dl><section class="sect"><header><h2>Installed in</h2><span class="meta">${installed.length} ${installed.length===1?"project":"projects"}</span></header>${installed.length?`<div class="hscroll"><table class="grid compact installed"><thead><tr><th>Machine</th><th>Project</th><th>Pin</th><th>Copy</th><th></th></tr></thead><tbody>${rows}</tbody></table></div>`:"<p class=note>Not installed in a trusted project folder yet.</p>"}</section></div>`;
    if(item._versions?.length>1){
      const label=node("label","Version"),select=node("select");select.setAttribute("aria-label","Package version");
      item._versions.forEach((version,index)=>{const option=node("option",version.commit);option.value=String(index);option.selected=version.commit===item.commit;select.append(option);});
      select.onchange=()=>renderLibraryDetail({...item._versions[Number(select.value)],_versions:item._versions});
      label.append(select);host.querySelector(".insp-head").after(label);
    }
    host.querySelector("#install-item").onclick=()=>installPackageDestinations(item);
    host.querySelectorAll("[data-update-target]").forEach(button=>button.addEventListener("click",()=>{const key=decodeURIComponent(button.dataset.updateTarget),target=destinations().find(row=>destinationKey(row.node,row.directory)===key);if(target)installPackageDestinations(item,[key]);}));
    host.querySelectorAll("[data-pack-key]").forEach(button=>button.addEventListener("click",()=>{const key=decodeURIComponent(button.dataset.packKey),pack=registryAgentpacks().find(value=>libraryItemKey(value)===key);if(pack){state.libraryKind="agentpacks";pickLibrary(pack);}}));
  }else{
    const revisions=item._revisions||[],selected=revisions.find(rev=>rev.id===state.revisionSelection)||revisions[0];
    if(selected)state.revisionSelection=selected.id;
    if(!selected){host.innerHTML=`${back}<div class="empty">${esc(item._error||"Revision details are unavailable.")}</div>`;host.querySelector("#lib-back").onclick=()=>$("library").classList.remove("show-detail");return;}
    const index=revisions.indexOf(selected),previous=revisions[index+1],packageItems=(selected.package_ids||[]).map(id=>[...state.libraryByNode.values()].flatMap(value=>value.packages||[]).find(pkg=>pkg._node===item._node&&pkg.id===id)).filter(Boolean);
    const previousItems=(previous?.package_ids||[]).map(id=>[...state.libraryByNode.values()].flatMap(value=>value.packages||[]).find(pkg=>pkg._node===item._node&&pkg.id===id)).filter(Boolean);
    const moduleSet=new Set(packageItems.map(pkg=>pkg.module)),previousMap=new Map(previousItems.map(pkg=>[pkg.module,pkg]));
    const changes=[...packageItems.filter(pkg=>!previousMap.has(pkg.module)).map(pkg=>["add","+",pkg.name||pkg.module]),...packageItems.filter(pkg=>previousMap.has(pkg.module)&&previousMap.get(pkg.module).commit!==pkg.commit).map(pkg=>["upd","~",pkg.name||pkg.module]),...previousItems.filter(pkg=>!moduleSet.has(pkg.module)).map(pkg=>["del","−",pkg.name||pkg.module])];
    const history=revisions.map((rev,i)=>`<button class="hrow ${rev.id===selected.id?"sel":""}" data-revision="${esc(rev.id)}" aria-pressed="${rev.id===selected.id}"><span class="tick"></span><span class="label"><b>${esc(rev.label||"Saved revision")}</b><small>${esc(new Date(rev.created_at).toLocaleString())} · ${esc(rev.source_node||item._machine)}</small></span><span class="meta">${i===0?'<span class="badge accent">Latest</span> ':""}${(rev.package_ids||[]).length} packages</span></button>`).join("");
    const packageRows=packageItems.map(pkg=>`<tr><td>${esc(pkg.name||pkg.module)}</td><td><code>${esc(pkg.module)}</code></td><td><code>${esc(pkg.commit)}</code></td></tr>`).join("");
    const diff=previous?(changes.map(([kind,sign,name])=>`<div class="diffline ${kind}"><span class="sign">${sign}</span>${esc(name)}</div>`).join("")||'<div class="diffline same"><span class="sign">=</span>Same packages as previous revision</div>'):"<p class=note>First saved revision.</p>";
    host.innerHTML=`${back}<div class="insp wide"><div class="insp-head"><span class="glyph">${svgIcon("folder")}</span><div class="title"><h1>${esc(item.name)}</h1><p>${esc(selected.project)} · saved on ${esc(item._machine)} · ${revisions.length} ${revisions.length===1?"revision":"revisions"}${item.heads?.length>1?" · Two versions to review":""}</p></div><div class="actions"><button id="rename-pack">Rename…</button><button class="primary" id="restore-pack">${svgIcon("install")}Restore…</button></div></div><dl class="kv"><dt>Source</dt><dd><code>${esc(selected.project)}</code> on ${esc(item._machine)}</dd><dt>Configuration</dt><dd>${selected.needs_local_configuration?'<span class="badge warn">Needs local configuration</span>':"Complete on this machine"}</dd><dt>Verified copies</dt><dd>${esc(copiesLabel(selected.id))}</dd></dl><div class="cols"><section class="sect"><header><h2>History</h2><span class="meta">Newest first</span></header><div class="history">${history}</div></section><section class="sect"><header><h2>${esc(selected.label||"Saved revision")}</h2><span class="meta">${esc(selected.mode||"default")} mode</span></header>${diff}<table class="grid compact"><thead><tr><th>Package</th><th>Module</th><th>Commit</th></tr></thead><tbody>${packageRows||'<tr><td colspan="3">No package pins</td></tr>'}</tbody></table><details class="disclose"><summary>${svgIcon("chevron")}agentpack.toml</summary><pre class="code">${esc(selected.manifest||"")}</pre></details></section></div></div>`;
    host.querySelector("#rename-pack").onclick=()=>renameAgentpack(item);
    host.querySelector("#restore-pack").onclick=()=>restoreAgentpack(item,selected);
    host.querySelectorAll("[data-revision]").forEach(button=>button.addEventListener("click",()=>{state.revisionSelection=button.dataset.revision;renderLibrary();}));
  }
  host.querySelector("#lib-back")?.addEventListener("click",()=>$("library").classList.remove("show-detail"));
}
function renderMachines(){
  const query=$("machine-search").value.trim().toLowerCase(),all=machines(),filtered=all.filter(machine=>[machine.name,machine.id].join(" ").toLowerCase().includes(query));
  if(!all.some(machine=>machine.id===state.machineSelection))state.machineSelection="";
  $("machines-summary").textContent=`${all.filter(machine=>machine.online!==false).length} online · ${all.filter(machine=>machine.online===false).length} offline`;
  const rows=$("machine-rows");rows.replaceChildren();
  for(const machine of filtered){const tr=document.createElement("tr");tr.dataset.machine=machine.id;tr.tabIndex=machine.id===state.machineSelection?0:-1;tr.className=machine.id===state.machineSelection?"sel":"";tr.innerHTML=`<td><span class="cell">${svgIcon(machine.id?"server":"laptop")}<span class="stack">${esc(machine.name)}</span></span></td><td><span class="cell"><span class="dot ${machine.online===false?"off":"ok"}"></span>${machine.online===false?"Offline":"Online"}</span></td>`;tr.addEventListener("click",()=>pickMachine(machine.id,true));rows.append(tr);}
  if(!filtered.length)rows.innerHTML='<tr><td colspan="2" class="empty">No machines match.</td></tr>';
  renderMachineDetail(all.find(machine=>machine.id===state.machineSelection)||all[0]);
}
function pickMachine(id,show=true){state.machineSelection=id;renderMachines();if(show)$("machines").classList.add("show-detail");}
function renderMachineDetail(machine){
  const host=$("machine-detail");if(!machine){host.innerHTML='<div class="empty">No machines are available.</div>';return;}
  const targets=destinations().filter(target=>target.node===machine.id),packages=knownPackages();
  const live=state.sessions.filter(session=>session.node===machine.id).length;
  const rows=targets.map(target=>{
    const sessionCount=state.sessions.filter(session=>session.node===machine.id&&session.directory===target.directory).length;
    const cells=packages.map(pkg=>{const pin=inventoryPin(target,pkg.module);if(!pin)return '<td class="pin"><span class="faint">—</span></td>';return pin.commit===pkg.commit?`<td class="pin"><code>${esc(pin.commit)}</code></td>`:`<td class="pin"><button class="link-btn mono" data-update-module="${esc(pkg.module)}" data-update-target="${esc(destinationKey(target.node,target.directory))}" title="Update to ${esc(pkg.commit)}">${esc(pin.commit)} ↑</button></td>`;}).join("");
    return `<tr><td title="${esc(target.directory)}"><span class="cell">${svgIcon("folder")}${esc(target.directory.split(/[\\/]/).filter(Boolean).pop()||target.directory)}</span></td><td class="c-sessions">${sessionCount||"—"}</td>${cells}<td class="act"><button class="icon-btn" data-install-target="${encodeURIComponent(destinationKey(target.node,target.directory))}" aria-label="Install into ${esc(target.directory)}" title="Install into this project" ${!target.online&&!target.folder_id||!target.available&&!target.folder_id?"disabled":""}>${svgIcon("install")}</button></td></tr>`;
  }).join("");
  const packageHead=packages.map(pkg=>`<th class="pin" title="${esc(pkg.module)}">${esc(pkg.name)}</th>`).join("");
  host.innerHTML=`<button class="tb-btn back" id="machine-back">${svgIcon("back")}Machines</button><div class="insp wide"><div class="insp-head"><span class="glyph">${svgIcon(machine.id?"server":"laptop")}</span><div class="title"><h1>${esc(machine.name)}</h1><p><span class="dot ${machine.online===false?"off":"ok"}" style="display:inline-block"></span> ${machine.online===false?"Offline":"Online"} · ${live} ${live===1?"session":"sessions"} · agentpack registry</p></div><div class="actions"><button id="machine-sessions">Sessions</button></div></div><section class="sect"><header><h2>Projects and pinned packages</h2><span class="meta">${targets.length} trusted folders</span></header><div class="hscroll"><table class="grid compact matrix"><thead><tr><th class="c-project">Project</th><th class="c-sessions">Sessions</th>${packageHead}<th></th></tr></thead><tbody>${rows||'<tr><td colspan="3" class="empty">No recent project folders on this machine.</td></tr>'}</tbody></table></div><p class="note">↑ marks a pin that differs from the current captured version. — means not installed. Offline installs are saved as drafts.</p></section></div>`;
  host.querySelector("#machine-back").onclick=()=>$("machines").classList.remove("show-detail");
  host.querySelector("#machine-sessions").onclick=()=>{state.node=machine.id;$("session-search").value=machine.name;renderSessions();setView("workspace");};
  host.querySelectorAll("[data-install-target]").forEach(button=>button.addEventListener("click",()=>{const key=decodeURIComponent(button.dataset.installTarget),target=targets.find(row=>destinationKey(row.node,row.directory)===key);if(target)installIntoTarget(target);}));
  host.querySelectorAll("[data-update-module]").forEach(button=>button.addEventListener("click",()=>{const key=decodeURIComponent(button.dataset.updateTarget),target=targets.find(row=>destinationKey(row.node,row.directory)===key),pkg=packages.find(value=>value.module===button.dataset.updateModule);if(target&&pkg)installPackageDestinations(pkg,[key]);}));
}
$("packages-tab").addEventListener("click",()=>{state.libraryKind="packages";state.librarySelection="";renderLibrary();});
$("setups-tab").addEventListener("click",()=>{state.libraryKind="agentpacks";state.librarySelection="";state.revisionSelection="";renderLibrary();});
$("library-search").addEventListener("input",renderLibrary);
$("machine-search").addEventListener("input",renderMachines);
$("refresh-machines").addEventListener("click",()=>{void refreshNodes();void loadWorkspaceData(true);});
function showSheet(title,body,actions,wide=true){
  const dialog=$("sheet");dialog.className=wide?"wide":"";$("sheet-title").textContent=title;$("sheet-body").innerHTML=body;$("sheet-actions").innerHTML=actions;
  $("sheet-form").onsubmit=event=>event.preventDefault();if(!dialog.open)dialog.showModal();
}
function closeSheet(){$("sheet").close();}
function packageLabel(item){return item.name||item.path||item.module;}
function packageState(item,target){
  const pin=inventoryPin(target,item.module);
  if(!target.online)return target.folder_id&&item.cache_key?{disabled:false,draft:true,text:"Offline · install as draft"}:{disabled:true,text:"Offline · exact package identity unavailable"};
  if(!target.available)return target.folder_id&&item.cache_key?{disabled:false,draft:true,text:`Folder unavailable · ${target.error||"install as draft"}`}:{disabled:true,text:target.error||"Folder unavailable"};
  if(target.error)return {disabled:true,text:`Could not check pins · ${target.error}`};
  if(pin?.commit===item.commit)return {disabled:true,text:"Installed"};
  return {disabled:false,text:pin?.commit?`Update ${pin.commit.slice(0,12)} → ${item.commit.slice(0,12)}`:"Not installed"};
}
function affectedCount(target){return state.sessions.filter(session=>session.node===target.node&&session.directory===target.directory&&session.state?.running&&session.state?.interactive!==false&&!session.stale).length;}
function draftExpectedPins(packages){
  const pins={};
  for(const pkg of packages){if(!pkg?.module||!pkg.commit||!pkg.cache_key)throw new Error(`Exact package identity is unavailable for ${pkg?.module||"this revision"}.`);pins[pkg.module]={commit:pkg.commit,cache_key:pkg.cache_key,...(pkg.content_hash?{content_hash:pkg.content_hash}:{})};}
  return pins;
}
async function saveDraft(input){await api("/api/library/drafts",input,"POST","");}
async function applyInstalls(jobs,restart,paint,done){
  const drafts=jobs.filter(job=>job.draftReason||!job.target.online),installs=jobs.filter(job=>!job.draftReason&&job.target.online);
  await mapLimit(drafts,2,async job=>{
    job.progress="Saving draft…";paint();
    try{await saveDraft({item:`${job.item.module}@${job.item.commit}`,node:job.target.node||"local",directory:job.target.directory,reason:job.draftReason||"Machine offline",folder_identity:job.target.folder_id,expected_pins:draftExpectedPins([job.item])});job.progress="Draft saved";job.drafted=true;}
    catch(error){job.progress=error.message;job.failed=true;}paint();
  });
  const perNode=new Map();for(const job of installs){const group=perNode.get(job.target.node)||[];group.push(job);perNode.set(job.target.node,group);}
  await Promise.all([...perNode.values()].map(group=>mapLimit(group,2,async job=>{
    job.progress="Installing…";paint();
    try{await api("/api/packages",{directory:job.target.directory,spec:`${job.item.module}@${job.item.commit}`,restart},"POST",job.target.node);job.progress="Installed";}
    catch(error){job.progress=error.message||"Install failed";job.failed=true;}
    paint();
  })));
  await loadDrafts();await loadInventory(true);await loadLibrary(true);done();
}
function installPackageDestinations(item,chosen=[]){
  const rows=destinations().map(target=>({target,state:packageState(item,target)}));
  let picked=new Set(chosen.filter(key=>rows.some(row=>destinationKey(row.target.node,row.target.directory)===key&&!row.state.disabled))),phase="pick",restart=true;
  const label=()=>{const jobs=[...picked].map(key=>rows.find(row=>destinationKey(row.target.node,row.target.directory)===key)).filter(Boolean),drafts=jobs.filter(job=>job.state.draft).length,installs=jobs.length-drafts;if(phase==="run")return "Installing…";if(phase==="done")return "Done";if(!jobs.length)return "Select projects to install into:";if(!installs)return `Save ${drafts} ${drafts===1?"draft":"drafts"}`;return drafts?`Install ${installs} · Draft ${drafts}`:`Install in ${installs} ${installs===1?"project":"projects"}`;};
  showSheet(`Install “${packageLabel(item)}”`,`<p>Pinned to <code>${esc(item.module)}@${esc(item.commit)}</code>. Choose every trusted destination and review its current pin.</p><div class="pick-head"><span class="seg-label">Destinations</span><span class="grow"></span><button id="install-all-online" class="tb-btn" type="button">Select all online</button></div><div class="picker" id="install-destinations"></div><div class="summary"><span id="install-summary"></span><span class="grow"></span><label><input id="install-restart" type="checkbox" checked>Restart affected agents</label></div><p class="note" id="install-note">Nothing changes until you confirm.</p>`,`<button type="button" id="sheet-cancel">Cancel</button><button type="button" class="primary" id="sheet-apply" disabled>Install</button>`);
  const paint=()=>{
    const host=$("install-destinations");host.replaceChildren();
    for(const machine of machines()){
      const group=rows.filter(row=>row.target.node===machine.id);if(!group.length)continue;
      const heading=node("div",void 0,"mhead");heading.innerHTML=`${svgIcon(machine.id?"server":"laptop")}${esc(machine.name)}<span class="meta"><span class="dot ${machine.online===false?"off":"ok"}"></span>${machine.online===false?"Offline":"Online"}</span>`;host.append(heading);
      for(const {target,state:status} of group){
        const key=destinationKey(target.node,target.directory),checked=picked.has(key),row=node("label",void 0,`dest${checked?" chosen":""}${status.disabled?" disabled":""}`);
        const input=document.createElement("input");input.type="checkbox";input.value=key;input.checked=checked;input.disabled=status.disabled||phase!=="pick";
        const name=node("span",void 0,"label");name.append(node("span",target.directory.split(/[\\/]/).filter(Boolean).pop()||target.directory));
        const statusCell=node("span",status.disabled?status.text:jobByKey.get(key)?.progress||status.text,`state${jobByKey.get(key)?.failed?" err":""}`);statusCell.title=target.directory;
        row.append(input,name,statusCell);input.addEventListener("change",()=>{input.checked?picked.add(key):picked.delete(key);paint();});host.append(row);
      }
    }
    const jobs=[...picked].map(key=>{const row=rows.find(row=>destinationKey(row.target.node,row.target.directory)===key);return {key,item,target:row?.target,draftReason:row?.state.draft?row.state.text:""};}).filter(job=>job.target);
    $("install-summary").textContent=`${jobs.reduce((sum,job)=>sum+affectedCount(job.target),0)} affected agents`;
    $("sheet-apply").textContent=phase==="run"?"Installing…":phase==="done"?"Done":label();
    $("sheet-apply").disabled=phase==="run"||phase==="pick"&&!picked.size;
    $("sheet-cancel").hidden=phase!=="pick";$("install-all-online").disabled=phase!=="pick";
    $("install-note").textContent=phase==="pick"?"Nothing changes until you confirm.":phase==="run"?"Each machine runs at most two installs at a time.":"Successful rows are installed or saved as drafts. Failed rows keep their error visible.";
  };
  const jobByKey=new Map();paint();
  $("install-all-online").onclick=()=>{for(const row of rows)if(row.target.online&&!row.state.disabled)picked.add(destinationKey(row.target.node,row.target.directory));paint();};
  $("install-restart").onchange=event=>{restart=event.target.checked;};$("sheet-cancel").onclick=closeSheet;
  $("sheet-apply").onclick=async()=>{
    if(phase==="done"){closeSheet();return;}
    phase="run";const jobs=[...picked].map(key=>{const row=rows.find(row=>destinationKey(row.target.node,row.target.directory)===key),target=row?.target;const job={key,target,item,draftReason:row?.state.draft?row.state.text:"",progress:"Queued"};jobByKey.set(key,job);return job;}).filter(job=>job.target);paint();
    await applyInstalls(jobs,restart,paint,()=>{phase="done";paint();});
  };
}
function installInto(session){
  const machine=machines().find(item=>item.id===(session.node||""))||machines()[0];
  const folders=destinations().filter(target=>target.node===machine.id&&(target.online&&target.available&&!target.error||target.folder_id));
  if(!folders.length){notice(`No available recent project folders on ${machine.name}.`);return;}
  let target=folders.find(row=>row.directory===session.directory)||folders[0],picked=new Set(),restart=true,phase="pick";
  const packages=knownPackages(),jobs=new Map();
  const paint=()=>{
    const folder=$("into-project").value;target=folders.find(row=>row.directory===folder)||folders[0];const host=$("package-choices");host.replaceChildren();
    for(const item of packages){const status=packageState(item,target),key=item.module;
      const row=node("label",void 0,`dest${picked.has(key)?" chosen":""}${status.disabled?" disabled":""}`),input=document.createElement("input");input.type="checkbox";input.value=key;input.checked=picked.has(key);input.disabled=status.disabled||phase!=="pick";
      const label=node("span",void 0,"label");label.append(node("span",packageLabel(item)),node("code",`${item.kind||"Package"} · ${item.commit.slice(0,12)}`));
      const job=jobs.get(key);row.append(input,label,node("span",job?.progress||status.text,`state${job?.failed?" err":""}`));input.onchange=()=>{input.checked?picked.add(key):picked.delete(key);paint();};host.append(row);
    }
    $("into-summary").textContent=`${affectedCount(target)} affected agents`;
    const chosen=packages.filter(item=>picked.has(item.module)).map(item=>packageState(item,target)),draftCount=chosen.filter(status=>status.draft).length,installCount=chosen.length-draftCount;
    $("into-apply").textContent=phase==="run"?"Installing…":phase==="done"?"Done":!picked.size?"Choose packages":!installCount?`Save ${draftCount} ${draftCount===1?"draft":"drafts"}`:draftCount?`Install ${installCount} · Draft ${draftCount}`:`Install ${installCount} ${installCount===1?"package":"packages"}`;
    $("into-apply").disabled=phase==="run"||phase==="pick"&&!picked.size;$("into-cancel").hidden=phase!=="pick";
  };
  showSheet(`Install into ${machine.name}`,`<p>Choose a recent project and package pins known to this gateway mesh.</p><div class="form into"><label for="into-project">Project</label><select id="into-project">${folders.map(folder=>`<option value="${esc(folder.directory)}" ${folder.directory===target.directory?"selected":""}>${esc(folder.directory)}</option>`).join("")}</select></div><div class="picker pkg-pick" id="package-choices"></div><div class="summary"><span id="into-summary"></span><span class="grow"></span><label><input id="into-restart" type="checkbox" checked>Restart affected agents</label></div><p class="note">An install error is shown on its package row; successful installs continue.</p>`,`<button type="button" id="into-cancel">Cancel</button><button type="button" class="primary" id="into-apply" disabled>Install</button>`);
  $("into-project").onchange=()=>{picked.clear();paint();};$("into-restart").onchange=event=>{restart=event.target.checked;};$("into-cancel").onclick=closeSheet;
  $("into-apply").onclick=async()=>{if(phase==="done"){closeSheet();return;}phase="run";const selected=packages.filter(item=>picked.has(item.module)).map(item=>{const status=packageState(item,target),job={key:item.module,item,target,draftReason:status.draft?status.text:"",progress:"Queued"};jobs.set(item.module,job);return job;});paint();await applyInstalls(selected,restart,paint,()=>{phase="done";paint();});};
  paint();
}
function installIntoTarget(target){
  const session={node:target.node,directory:target.directory};installInto(session);
}
function saveAgentpack(session){
  if(!session){notice("Select a session before saving an agentpack.");return;}
  const machine=machines().find(value=>value.id===(session.node||""))||machines()[0];
  if(machine.online===false){notice(`${machine.name} is offline. Save from this project when it reconnects.`);return;}
  const recent=(state.recentByNode.get(machine.id)||[]).find(folder=>folder.directory===session.directory&&folder.available!==false);
  if(!recent){notice("This project is not an available recent folder on its machine.");return;}
  const existing=registryAgentpacks().filter(pack=>pack._nodes?.includes(machine.id)&&pack._revisions?.some(revision=>revision.project===session.directory));
  const first=existing[0],suggested=first?.name||session.directory.split(/[\\/]/).filter(Boolean).pop()||"Agentpack";
  showSheet("Save agentpack",`<p>Capture <strong>${esc(session.directory)}</strong> on ${esc(machine.name)} as a new immutable revision.</p><div class="form"><label for="save-name">Name</label><div class="with-btn"><input id="save-name" maxlength="80" value="${esc(suggested)}"><button type="button" id="suggest-name">Suggest</button></div><label for="save-agentpack">Agentpack</label><select id="save-agentpack"><option value="">Create a new agentpack</option>${existing.map(pack=>`<option value="${esc(pack.id)}">${esc(pack.name)} · new revision</option>`).join("")}</select><label for="save-label">Revision note</label><input id="save-label" maxlength="80" placeholder="What changed?"><label>Mode</label><span class="static">${esc(session.mode||"default")}</span><span class="check"><input id="auto-capture" type="checkbox" checked><label for="auto-capture" style="text-align:left">Capture successful Agentpack changes automatically</label></span><span></span><p id="save-error" class="err" role="alert"></p></div><div class="callout">${svgIcon("package")}<span>Credential values and MCP secrets are kept as machine-local configuration.</span></div>`,`<button type="button" id="save-cancel">Cancel</button><button type="button" id="save-confirm" class="primary">Save to Library</button>`);
  $("save-agentpack").onchange=()=>{const pack=existing.find(value=>value.id===$("save-agentpack").value);if(pack)$("save-name").value=pack.name;};
  $("suggest-name").onclick=()=>{$("save-name").value=`${session.directory.split(/[\\/]/).filter(Boolean).pop()||"Project"} setup`;};
  $("save-cancel").onclick=closeSheet;
  $("save-confirm").onclick=async()=>{
    const name=$("save-name").value.trim();if(!name){$("save-error").textContent="Enter a name.";$("save-name").focus();return;}
    const button=$("save-confirm");button.disabled=true;button.textContent="Saving…";
    try{
      const saved=await api("/api/library/agentpacks",{directory:session.directory,name,agentpack_id:$("save-agentpack").value||undefined,label:$("save-label").value.trim()||undefined,mode:session.mode||"default",auto_capture:$("auto-capture").checked},"POST",machine.id);
      closeSheet();state.libraryKind="agentpacks";state.revisionSelection=saved.revision.id;
      await loadLibrary(true);const savedPack=registryAgentpacks().find(pack=>pack.id===saved.agentpack.id);state.librarySelection=savedPack?libraryItemKey(savedPack):`${machine.id}\0${saved.agentpack.id}`;setView("library");$("library").classList.add("show-detail");
    }catch(error){$("save-error").textContent=error.message;button.disabled=false;button.textContent="Save to Library";}
  };
}
function renameAgentpack(pack){
  showSheet("Rename agentpack",`<p>Rename the Library label. The agentpack identity and revisions stay the same.</p><div class="form"><label for="rename-name">Name</label><div class="with-btn"><input id="rename-name" maxlength="80" value="${esc(pack.name)}"><button type="button" id="rename-suggest">Suggest</button></div><span></span><p id="rename-error" class="err" role="alert"></p></div>`,`<button type="button" id="rename-cancel">Cancel</button><button type="submit" id="rename-confirm" class="primary">Rename</button>`,false);
  $("rename-name").select();$("rename-suggest").onclick=()=>{$("rename-name").value=`${pack.name} setup`;};$("rename-cancel").onclick=closeSheet;
  $("sheet-form").onsubmit=async event=>{event.preventDefault();if($("rename-confirm").disabled)return;const name=$("rename-name").value.trim();if(!name){$("rename-error").textContent="Enter a name.";return;}const button=$("rename-confirm");button.disabled=true;
    try{await api(`/api/library/agentpacks/${encodeURIComponent(pack.id)}`,{name},"PATCH",pack._node);closeSheet();state.librarySelection=libraryItemKey(pack);await loadLibrary(true);}
    catch(error){$("rename-error").textContent=error.message;button.disabled=false;}
  };
}
function renameSession(session){
  showSheet("Rename session",`<p>${esc(session.directory)} · ${esc(session.machine||"Gateway machine")}</p><div class="form"><label for="session-name">Name</label><input id="session-name" maxlength="80" required value="${esc(sessionName(session))}"><span></span><p id="session-name-error" class="err" role="alert"></p></div>`,`<button type="button" id="session-rename-cancel">Cancel</button><button type="submit" id="session-rename-confirm" class="primary">Rename</button>`,false);
  $("session-name").select();
  $("session-rename-cancel").onclick=closeSheet;
  $("sheet-form").onsubmit=async event=>{
    event.preventDefault();
    const name=$("session-name").value.trim();
    if(!name){$("session-name-error").textContent="Enter a name.";return;}
    const submit=$("session-rename-confirm");
    if(submit.disabled)return;
    submit.disabled=true;
    try{
      const updated=await api(`/api/sessions/${encodeURIComponent(session.id)}`,{name},"PATCH",session.node||"");
      const cached=state.sessionCache.get(session.node||"")||[];
      state.sessionCache.set(session.node||"",cached.map(item=>item.id===session.id?{...item,name:updated.name}:item));
      state.sessions=state.sessions.map(item=>sessionKey(item)===sessionKey(session)?{...item,name:updated.name}:item);
      closeSheet();renderSessions();notice("Session renamed.");
    }catch(error){$("session-name-error").textContent=error.message;submit.disabled=false;}
  };
}
function restoreAgentpack(pack,revision){
  const targets=destinations();let chosen="",restart=true,phase="preview",previews=new Map(),pending=new Set();
  const valid=target=>target.online&&target.available&&!target.error;
  const draftPackages=(revision.package_ids||[]).map(id=>[...state.libraryByNode.values()].flatMap(value=>value.packages||[]).find(item=>item.id===id));
  const draftable=target=>!!target.folder_id&&(!target.online||!target.available||!!target.error)&&draftPackages.every(item=>item?.cache_key);
  const render=()=>{
    const host=$("restore-targets");host.replaceChildren();
    for(const machine of machines()){
      const group=targets.filter(target=>target.node===machine.id);if(!group.length)continue;
      const heading=node("div",void 0,"mhead");heading.innerHTML=`${svgIcon(machine.id?"server":"laptop")}${esc(machine.name)}<span class="meta"><span class="dot ${machine.online===false?"off":"ok"}"></span>${machine.online===false?"Offline":"Online"}</span>`;host.append(heading);
      for(const target of group){const key=destinationKey(target.node,target.directory),preview=previews.get(key),loading=pending.has(key),canDraft=draftable(target),row=node("label",void 0,`dest${chosen===key?" chosen":""}${!valid(target)&&!canDraft?" disabled":""}`);
        const input=document.createElement("input");input.type="radio";input.name="restore-target";input.checked=chosen===key;input.disabled=!valid(target)&&!canDraft||loading||phase!=="preview";input.onchange=()=>{chosen=key;render();};
        const label=node("span",void 0,"label");label.append(node("span",target.directory));
    const stateText=preview?.error||(canDraft?`${!target.online?"Offline":!target.available?"Folder unavailable":"Pins unavailable"} · save as draft`:loading?"Checking…":preview?`${preview.diff.add.length} add · ${preview.diff.update.length} update · ${preview.diff.remove.length} remove`:target.error||"Preview unavailable");
        row.append(input,label,node("span",stateText,`state${preview?.error?" err":""}`));host.append(row);
      }
    }
    const active=previews.get(chosen),diff=$("restore-diff");
    const selectedTarget=targets.find(row=>destinationKey(row.node,row.directory)===chosen),willDraft=selectedTarget&&draftable(selectedTarget);
    if(willDraft)diff.innerHTML=`<p class="${active?.error?"err":"note"}">${esc(active?.error||"This destination cannot be checked now. Save a draft, then review the exact diff after the machine and project are available.")}</p>`;
    else if(!chosen||!active)diff.innerHTML='<p class="note">Choose an available destination to preview the changes.</p>';
    else if(active.error)diff.innerHTML=`<p class="err">${esc(active.error)}</p>`;
    else{const d=active.diff;diff.innerHTML=`${[...d.add.map(module=>["add","+","Add",module]),...d.update.map(module=>["upd","~","Update",module]),...d.remove.map(module=>["del","−","Remove",module])].map(([kind,sign,action,module])=>`<div class="diffline ${kind}"><span class="sign">${sign}</span>${action} <code>${esc(module)}</code></div>`).join("")||'<div class="diffline same"><span class="sign">=</span>No package changes</div>'}<div class="diffline same"><span class="sign">·</span>Restore mode <code>${esc(d.mode||revision.mode||"default")}</code>; manifest and lock are backed up before apply.</div>`;}
    $("restore-apply").textContent=phase==="run"?(willDraft?"Saving draft…":"Restoring…"):phase==="done"?"Done":willDraft?"Save draft":"Restore";
    const unavailable=!chosen||(!willDraft&&(!active||!!active.error));
    $("restore-apply").disabled=phase==="run"||phase==="preview"&&unavailable;
    $("restore-cancel").hidden=phase!=="preview";$("restore-status").textContent=phase==="preview"?"Nothing changes until you confirm.":phase==="run"?willDraft?"Saving a pending restore for explicit review.":"Applying this revision and restarting affected agents…":willDraft?"Draft saved. It will be re-checked before anything is applied.":"Restore completed.";
  };
  showSheet(`Restore “${pack.name}”`,`<p>Revision <strong>${esc(revision.label||"Saved revision")}</strong> · ${esc(new Date(revision.created_at).toLocaleString())}. Select one trusted project and review its current pins.</p><div class="picker" id="restore-targets"></div><section class="sect"><header><h2>Preview</h2><span class="meta">${esc(revision.mode||"default")} mode</span></header><div id="restore-diff"></div></section><label class="check"><input id="restore-restart" type="checkbox" checked>Restart affected agents after restore</label><p id="restore-status" class="note"></p>`,`<button type="button" id="restore-cancel">Cancel</button><button type="button" id="restore-apply" class="primary" disabled>Restore</button>`);
  $("restore-cancel").onclick=closeSheet;$("restore-restart").onchange=event=>{restart=event.target.checked;};
  const groups=machines().map(machine=>({machine,targets:targets.filter(target=>target.node===machine.id&&valid(target))}));
  void Promise.all(groups.map(({machine,targets:rows})=>mapLimit(rows,2,async target=>{
    const key=destinationKey(target.node,target.directory);pending.add(key);render();
    try{const diff=await api(`/api/library/agentpacks/${encodeURIComponent(pack.id)}/revisions/${encodeURIComponent(revision.id)}/restore-preview?directory=${encodeURIComponent(target.directory)}`,void 0,"GET",target.node);previews.set(key,{diff});if(!chosen)chosen=key;}
    catch(error){previews.set(key,{error:error.message});}
    pending.delete(key);render();
  })));
  $("restore-apply").onclick=async()=>{if(phase==="done"){closeSheet();return;}const target=targets.find(row=>destinationKey(row.node,row.directory)===chosen);if(!target)return;phase="run";render();
    try{if(draftable(target)){await saveDraft({item:pack.id,pack_id:pack.id,revision:revision.id,node:target.node||"local",directory:target.directory,reason:!target.online?"Machine offline":target.error||"Project could not be checked",folder_identity:target.folder_id,expected_pins:draftExpectedPins(draftPackages)});await loadDrafts();}
      else await api(`/api/library/agentpacks/${encodeURIComponent(pack.id)}/revisions/${encodeURIComponent(revision.id)}/restore`,{directory:target.directory,restart},"POST",target.node);await loadInventory(true);await loadLibrary(true);phase="done";render();}
    catch(error){previews.set(chosen,{error:error.message});phase="preview";render();}
  };
  render();
}
function renderStartFolders(preferred = "") {
  const machine = $("start-machine")?.value ?? state.node;
  const rows = state.recentByNode.get(machine) || [];
  const select = $("start-directory");
  if (!select) return;
  select.replaceChildren(...(rows.length ? rows.map(recent => { const option=node("option", recent.directory); option.value=recent.directory; option.disabled=recent.available === false; option.title=recent.unavailable_reason || recent.directory; return option; }) : [node("option", "No recent projects available")]));
  if (preferred) select.value = preferred;
}
function startDialog(recent = {}) {
  const machineSelect = $("start-machine");
  const machines = [{id:"",name:"Gateway machine",online:true}, ...state.nodes];
  machineSelect.replaceChildren(...machines.map(machine => { const option=node("option", `${machine.name}${machine.online === false ? " — offline" : ""}`); option.value=machine.id; option.disabled=machine.online === false; return option; }));
  machineSelect.value = recent.node || state.node || "";
  machineSelect.onchange = () => { renderStartFolders(); $("start-form").querySelector("[type=submit]").disabled = machineSelect.selectedOptions[0]?.disabled || !$("start-directory").value; };
  renderStartFolders(recent.directory || "");
  $("start-directory").onchange = () => { $("start-form").querySelector("[type=submit]").disabled = !$("start-directory").value || machineSelect.selectedOptions[0]?.disabled; };
  $("start-harness").value = recent.harness || "claude";
  $("start-mode").value = recent.mode || "default";
  $("start-form").querySelector("[type=submit]").disabled = machineSelect.selectedOptions[0]?.disabled || !$("start-directory").value || recent.harness === "agy";
  $("start-error").textContent = "";
  $("start-dialog").showModal();
  $("start-directory").focus();
}
$("new-session").addEventListener("click", () => startDialog());
$("cancel-start").addEventListener("click", () => $("start-dialog").close());
$("start-form").addEventListener("submit", async event => {
  event.preventDefault();
  const submit = event.target.querySelector("[type=submit]");
  if (!$("start-directory").value) { $("start-error").textContent = "Choose a recent project on an online machine."; return; }
  submit.disabled = true;
  const target = $("start-machine").value;
  try {
    const session = await api("/api/sessions", {directory:$("start-directory").value, harness:$("start-harness").value, mode:$("start-mode").value.trim()}, void 0, target);
    $("start-dialog").close(); setView("workspace"); await refresh();
    if (session?.id) openTerminal({...session,node:target,machine:state.nodes.find(item=>item.id===target)?.name || "Gateway machine"});
  } catch (error) { $("start-error").textContent=error.message; }
  finally { submit.disabled=false; }
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
  return state.socket?.readyState === WebSocket.OPEN && !!state.attachment && state.terminalState?.shared_input === true && state.terminalState?.running && state.replayComplete;
}
function updateTerminalState(value) {
  if (value) state.terminalState = value;
  const current = state.terminalState;
  if (value && state.terminal && current?.rows > 0 && current?.columns > 0 && (state.terminal.rows !== current.rows || state.terminal.cols !== current.columns)) {
    state.terminal.resize(current.columns, current.rows);
    scaleTerminal();
  }
  const writable = controls();
  $("terminal-status").textContent = !current?.running ? "Stopped" + (current?.exit_code !== void 0 ? ` (${current.exit_code})` : "") : !current.shared_input ? "Read-only" : state.replayComplete ? "Shared input" : "Replaying output";
  document.querySelectorAll("[data-key]").forEach((element) => element.disabled = !writable);
  if (state.terminal) state.terminal.options.disableStdin = !writable;
  if (current?.running && current.shared_input !== true && state.socket?.readyState === WebSocket.OPEN) terminalNotice("Start a new session to enable shared input.");
  $("connection-state").textContent = !current?.running ? "Stopped" : "Live";
  $("sb-session").textContent = state.selected ? (state.sessions.find(session => sessionKey(session) === state.selected)?.machine || "Gateway machine") : "";
}
function scaleTerminal() {
  if (!state.terminal || state.view !== "workspace") return;
  const screen = $("terminal-mount").querySelector(".xterm-screen");
  const stage = $("terminal-stage");
  if (!screen?.offsetWidth || !stage) return;
  const style = getComputedStyle(stage);
  const availableW = stage.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
  const availableH = stage.clientHeight - parseFloat(style.paddingTop) - parseFloat(style.paddingBottom);
  const fitScale = Math.min(availableW / screen.offsetWidth, availableH / screen.offsetHeight);
  const scale = state.zoom === "fit" ? Math.max(innerWidth <= 760 ? .7 : .05, fitScale) : state.zoom;
  $("terminal-mount").style.width = `${screen.offsetWidth}px`;
  $("terminal-mount").style.height = `${screen.offsetHeight}px`;
  $("terminal-mount").style.transform = `scale(${scale})`;
  $("terminal-space").style.width = `${screen.offsetWidth * scale}px`;
  $("terminal-space").style.height = `${screen.offsetHeight * scale}px`;
  $("zoom-value").textContent = `${Math.round(scale * 100)}%`;
  $("zoom-value").dataset.scale = scale;
  $("grid-label").textContent = state.terminal.cols && state.terminal.rows ? `${state.terminal.cols} × ${state.terminal.rows}` : "—";
  $("fit").classList.toggle("on", state.zoom === "fit");
  $("actual").classList.toggle("on", state.zoom === 1);
  $("terminal-stage").dataset.clamped = state.zoom === "fit" && scale > fitScale + .001;
}
$("fit").addEventListener("click", () => { state.zoom="fit"; scaleTerminal(); });
$("actual").addEventListener("click", () => { state.zoom=1; scaleTerminal(); });
$("zoom-out").addEventListener("click", () => { state.zoom=Math.max(.2, Number($("zoom-value").dataset.scale || 1)-.1); scaleTerminal(); });
$("zoom-in").addEventListener("click", () => { state.zoom=Math.min(2.5, Number($("zoom-value").dataset.scale || 1)+.1); scaleTerminal(); });
new ResizeObserver(() => requestAnimationFrame(scaleTerminal)).observe($("terminal-stage"));
function canRespond() {
  return state.replayComplete && state.terminalState?.shared_input === true && state.terminalState?.responder === state.attachment && !!state.attachment;
}
function registerReplyHandlers(terminal) {
  const consume = () => !canRespond();
  for (const id of [{ final: "c" }, { prefix: ">", final: "c" }, { final: "n" }, { prefix: "?", final: "n" }, { intermediates: "$", final: "p" }, { prefix: "?", intermediates: "$", final: "p" }]) terminal.parser.registerCsiHandler(id, consume);
  terminal.parser.registerCsiHandler({ final: "t" }, (params) => [14, 16, 18].includes(params[0]) && consume());
  terminal.parser.registerOscHandler(4, (data) => data.split(";").some((part, index) => index % 2 === 1 && part === "?") && consume());
  for (const id of [10, 11, 12]) terminal.parser.registerOscHandler(id, (data) => data.split(";").includes("?") && consume());
  terminal.parser.registerDcsHandler({ intermediates: "$", final: "q" }, consume);
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
function terminalLoading(message = "") {
  $("terminal-loading").textContent = message;
  $("terminal-loading").hidden = !message;
  $("terminal-panel").setAttribute("aria-busy", String(!!message));
}
function closeTerminal() {
  terminalLoading();
  state.retries = 0;
  state.restartToken++;
  state.attachmentGeneration++;
  const socket = state.socket;
  state.socket = null;
  socket?.close();
  state.terminal?.dispose();
  state.terminal = null;
  state.selected = null;
  state.attachment = 0;
  state.terminalState = null;
  state.replayComplete = true;
  state.firstFrameMarked = false;
  $("terminal-empty").hidden = false;
  $("terminal-mount").replaceChildren();
  $("grid-label").textContent = "—";
  $("terminal-title").textContent = "Sessions";
  $("terminal-subtitle").textContent = "Select a session";
  $("terminal-status").textContent = "Select a session to attach.";
  $("connection-state").textContent = "Disconnected";
}
function openTerminal(session, reconnecting = false, force = false) {
  if (!force && !reconnecting && state.selected === sessionKey(session) && state.terminal) {
    $("workspace").classList.remove("show-sessions");
    return;
  }
  if (reconnecting) {
    const previous = state.socket;
    state.socket = null;
    previous?.close();
  } else closeTerminal();
  const generation = ++state.attachmentGeneration;
  state.selected = sessionKey(session);
  state.replayComplete = false;
  state.firstFrameMarked = false;
  $("workspace").classList.remove("show-sessions");
  if (!reconnecting) $("terminal-stage").classList.remove("stale", "reconnecting");
  $("terminal-title").textContent = sessionName(session);
  $("terminal-subtitle").textContent = `${session.harness || "Agent"} · ${basename(session.directory)} · ${session.machine || "Gateway machine"}`;
  $("terminal-title").title = session.directory || "Unknown project";
  const terminalUnavailable = session.harness === "agy" || session.state?.interactive === false || session.stale;
  if (terminalUnavailable) {
    $("terminal-empty").hidden = false;
    const message = session.harness === "agy" ? "Desktop launcher presence · start and interact locally. Detached desktop windows are not tracked." : session.state?.interactive === false ? "Native stdio · browser input unavailable" : "Offline · showing last-known session metadata";
    $("terminal-empty").querySelector("p").textContent = message;
    $("terminal-status").textContent = session.stale ? "Offline · last-known session" : session.state?.interactive === false ? "Native stdio · input unavailable" : "Desktop session · local interaction";
    $("connection-state").textContent = session.stale ? "Offline" : "Input unavailable";
    renderSessions(); $("sessions").focus({preventScroll:true});
    return;
  }
  $("terminal-empty").querySelector("p").textContent = "Select a session to view its terminal.";
  $("terminal-empty").hidden = true;
  $("terminal-status").textContent = reconnecting ? "Replaying output" : "Connecting\u2026";
  terminalLoading(reconnecting ? "Reconnecting to terminal…" : "Loading terminal…");
  terminalNotice("");
  const url = new URL(`/api/sessions/${encodeURIComponent(session.id)}/terminal`, location.href);
  if (session.node) url.searchParams.set("node", session.node);
  url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const socket = new WebSocket(url);
  state.socket = socket;
  socket.binaryType = "arraybuffer";
  if (!reconnecting) {
    state.zoom = "fit";
    const rows = session.state?.rows || 24, columns = session.state?.columns || 80;
    state.terminal = new Terminal({ cols: columns, rows, cursorBlink: true, fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace", fontSize: 13, scrollback: 5e3, allowProposedApi: true, disableStdin: true, screenReaderMode: true, theme: terminalTheme() });
    state.terminal.open($("terminal-mount"));
    state.terminal.onData(sendInput);
    state.terminal.onBinary((data) => sendInput(Uint8Array.from(data, (character) => character.charCodeAt(0))));
    registerReplyHandlers(state.terminal);
    state.firstFrameMarked = false;
  } else {
    state.attachment = 0;
    state.replayComplete = false;
    $("terminal-stage").classList.add("stale", "reconnecting");
  }
  scaleTerminal();
  // A socket can open without the worker ever completing its attachment.
  const replayDeadline = setTimeout(() => {
    if (state.socket === socket && !state.replayComplete) socket.close();
  }, 30000);
  let pendingOutput = 0;
  let receivedFrame = false;
  let replayReceived = false;
  const finishReplay = () => {
    if (state.socket !== socket || generation !== state.attachmentGeneration || sessionKey(session) !== state.selected || !replayReceived || pendingOutput || (state.terminalState?.shared_input !== true && !receivedFrame) || state.replayComplete) return;
    state.replayComplete = true;
    clearTimeout(replayDeadline);
    terminalLoading();
    $("terminal-stage").classList.remove("stale", "reconnecting");
    updateTerminalState();
    terminalMark("replay_complete");
  };
  socket.onopen = () => { if (generation === state.attachmentGeneration && state.socket === socket) terminalMark("socketopen"); };
  socket.onmessage = (event) => {
    if (state.socket !== socket || generation !== state.attachmentGeneration || sessionKey(session) !== state.selected) return;
    if (event.data instanceof ArrayBuffer) {
      const firstFrame = !receivedFrame;
      receivedFrame = true;
      if (!state.firstFrameMarked) { state.firstFrameMarked = true; terminalMark("firstframe"); }
      pendingOutput += event.data.byteLength;
      if (pendingOutput > 16 * 1024 * 1024) {
        socket.close();
        terminalNotice("Output exceeded this browser\u2019s buffer. Reconnect to catch up.");
        return;
      }
      const length = event.data.byteLength;
      let frame = new Uint8Array(event.data);
      const written = () => { pendingOutput -= length; finishReplay(); };
      if (reconnecting && firstFrame) {
        const reset = encoder.encode("\x1bc");
        const ordered = new Uint8Array(reset.length + frame.length);
        ordered.set(reset); ordered.set(frame, reset.length); frame = ordered;
      }
      state.terminal.write(frame, written);
      return;
    }
    try {
      const message = JSON.parse(event.data);
      if (message.attachment) state.attachment = message.attachment;
      if (message.type === "attached") terminalMark("attached");
      if (message.type === "attached" && message.state?.shared_input !== true) replayReceived = true;
      if (message.type === "replay_complete") { replayReceived = true; finishReplay(); }
      if (message.type === "attached" && state.retries) {
        state.retries = 0;
        terminalNotice("");
      }
      const previouslyWritable = controls();
      if (message.type === "attached" && message.offset) terminalNotice("Earlier output was truncated. Showing the available terminal history.");
      if (message.error) terminalNotice(message.error);
      if (message.type === "ended" && message.state?.restarting) reconnectAfterRestart(session);
      if (message.state) updateTerminalState(message.state);
      if (!previouslyWritable && controls()) {
        scaleTerminal();
      }
    } catch {
      terminalNotice("The terminal sent an invalid control message.");
      socket.close();
    }
  };
  socket.onclose = () => {
    clearTimeout(replayDeadline);
    if (state.socket !== socket) return;
    state.attachment = 0;
    updateTerminalState();
    if (state.terminalState?.running === false) terminalLoading();
    if (state.terminalState?.restarting) $("terminal-status").textContent = "Restarting · waiting to reconnect";
    else if (state.terminalState?.running !== false && state.sessions.some((value) => sessionKey(value) === sessionKey(session))) {
      // Networks drop (gateway restarts, tailnet hiccups); the worker replays output, so retry quietly.
      state.retries++;
      const delay = Math.min(10000, 500 * 2 ** (state.retries - 1));
      const token = state.restartToken;
      $("terminal-status").textContent = `Connection lost · reconnecting in ${Math.ceil(delay / 1000)}s`;
      $("terminal-stage").classList.add("stale", "reconnecting");
      terminalLoading("Connection lost · reconnecting…");
      $("connection-state").textContent = "Reconnecting";
      state.replayComplete = false;
      setTimeout(() => { if (state.restartToken === token && state.selected === sessionKey(session)) openTerminal(session, true); }, delay);
    } else if (state.terminalState?.running !== false) {
      terminalLoading();
      $("terminal-status").textContent = "Disconnected · this session is no longer listed";
    }
  };
  socket.onerror = () => {
    if (state.socket === socket && state.retries >= 2) terminalNotice("Still can\u2019t reach this terminal. Retrying automatically\u2026");
  };
  renderSessions();
  $("sessions").focus({preventScroll:true});
}
async function reconnectAfterRestart(session) {
  const token = state.restartToken;
  const deadline = Date.now() + 90000;
  terminalNotice("Agent restarting. Waiting to reconnect…");
  while (Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 250));
    if (state.restartToken !== token || !state.authenticated) return;
    try {
      const sessions = list(await api("/api/sessions", void 0, void 0, session.node || ""), "sessions");
      if (state.restartToken !== token) return;
      const replacement = sessions.find(value => value.id === session.id);
      if (replacement?.state && !replacement.state.restarting) {
        openTerminal({ ...replacement, node: session.node, machine: session.machine }, false, true);
        terminalNotice("Agent restarted. This is a new process; conversation resume depends on the agent’s launch options.");
        return;
      }
    } catch {
      // A restarting worker may briefly be unavailable before its endpoint returns.
    }
  }
  if (state.restartToken === token) terminalNotice("The agent has not become available. Reconnect when its restart completes.");
}

let sharingEnabled=false;
const replicationMessages=new Map();
function renderReplication(){
  const host=$("replication-peers");if(!host)return;host.replaceChildren();
  if(state.replicationError){$("replication-status").textContent=state.replicationError;return;}
  $("replication-status").textContent=sharingEnabled?"Both machines must enroll each other before registry objects replicate.":"Enable Tailscale sharing before enrolling a machine.";
  const enrolled=new Map((state.replication.peers||[]).map(peer=>[peer.node,peer]));
  const peers=state.nodes;
  if(!peers.length){host.append(node("p","No allowed Tailscale peers are currently discovered.","muted"));return;}
  for(const peer of peers){
    const replica=enrolled.get(peer.id),contact=state.replication.contacts?.[peer.id],row=node("article",void 0,"list-row"),details=node("div"),actions=node("div",void 0,"actions");
    details.append(node("strong",peer.name),node("p",replica?`${replica.receipt_count||0} verified object receipts · ${peer.online?"online":"offline"}`:peer.online?"Not enrolled":"Offline · enrollment unavailable","muted"));
    if(contact?.error)details.append(node("p",contact.error,"hint"));else if(contact?.at)details.append(node("p",`Last contact ${new Date(contact.at).toLocaleString()}`,"hint"));
    const result=replicationMessages.get(peer.id);if(result)details.append(node("p",result,"hint"));
    if(replica){
      actions.append(button("Sync now",async()=>{const button=actions.querySelector("button");button.disabled=true;replicationMessages.set(peer.id,"Syncing verified objects…");renderReplication();try{await api("/api/library/replication/sync",{peer:peer.id},"POST","");replicationMessages.set(peer.id,"Sync complete.");await loadReplication();}catch(error){replicationMessages.set(peer.id,error.message);renderReplication();}}));
      actions.append(button("Remove",async()=>{try{await api(`/api/library/replication/peers/${encodeURIComponent(peer.id)}`,void 0,"DELETE","");replicationMessages.set(peer.id,"Enrollment removed.");await loadReplication();}catch(error){replicationMessages.set(peer.id,error.message);renderReplication();}}));
    }else actions.append(button("Enroll",async()=>{const button=actions.querySelector("button");button.disabled=true;replicationMessages.set(peer.id,"Enrolling…");renderReplication();try{await api("/api/library/replication/peers",{peer:peer.id},"POST","");replicationMessages.set(peer.id,"Enrolled. The other machine must enroll this gateway too.");await loadReplication();}catch(error){replicationMessages.set(peer.id,error.message);renderReplication();}}));
    for(const action of actions.querySelectorAll("button"))action.disabled||=!peer.online||!sharingEnabled;
    row.append(details,actions);host.append(row);
  }
}
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
    renderReplication();
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
  try { await api("/api/node/stop",{}); closeTerminal(); state.authenticated=false; $("settings-running").textContent="Stopped"; $("gateway-state").textContent="Gateway stopped"; $("gateway-state").parentElement.classList.remove("live"); $("settings-status").textContent="Gateway stopped. CLI sessions keep running. Run agentpack gateway start on the gateway machine to reconnect."; $("toggle-sharing").disabled=true; $("stop-all-agents").disabled=true; $("stop-gateway").disabled=true; }
  catch(error) { $("settings-status").textContent=error.message; }
});
function toggleSidebar() {
  const workspace = $("workspace");
  const mobile = matchMedia("(max-width:760px)").matches;
  const collapsed = mobile ? workspace.classList.toggle("show-sessions") : workspace.classList.toggle("no-sidebar");
  const visible = mobile ? collapsed : !collapsed;
  $("toggle-sessions").setAttribute("aria-pressed", String(visible));
  $("toggle-sessions").setAttribute("aria-label", visible ? "Hide sessions" : "Show sessions");
  requestAnimationFrame(scaleTerminal);
}
function syncMobileWorkspace() {
  const workspace = $("workspace");
  if (innerWidth <= 760 && !state.selected) workspace.classList.add("show-sessions");
  if (innerWidth > 760) workspace.classList.remove("show-sessions");
  if (innerWidth <= 760) {
    const visible = workspace.classList.contains("show-sessions");
    $("toggle-sessions").setAttribute("aria-pressed", String(visible));
    $("toggle-sessions").setAttribute("aria-label", visible ? "Hide sessions" : "Show sessions");
  }
}
window.addEventListener("resize", syncMobileWorkspace);
syncMobileWorkspace();
$("toggle-sessions").addEventListener("click", toggleSidebar);
document.addEventListener("keydown", event => {
  if ((event.metaKey || event.ctrlKey) && event.key === "\\" && !$("app").hidden) { event.preventDefault(); toggleSidebar(); }
  if ((event.metaKey || event.ctrlKey) && ["1","2","3"].includes(event.key)) { event.preventDefault(); setView(({1:"workspace",2:"library",3:"machines"})[event.key]); }
});
const splitter = $("splitter");
function resizeSidebar(width) { state.sidebarWidth=Math.round(Math.max(220,Math.min(420,width))); document.documentElement.style.setProperty("--sidebar-w", `${state.sidebarWidth}px`); splitter.setAttribute("aria-valuenow", state.sidebarWidth); }
splitter.addEventListener("pointerdown", event => splitter.setPointerCapture(event.pointerId));
splitter.addEventListener("pointermove", event => { if (splitter.hasPointerCapture(event.pointerId)) resizeSidebar(event.clientX); });
splitter.addEventListener("pointerup", event => splitter.releasePointerCapture(event.pointerId));
splitter.addEventListener("dblclick", () => resizeSidebar(264));
splitter.addEventListener("keydown", event => { if (["ArrowLeft","ArrowRight"].includes(event.key)) { event.preventDefault(); resizeSidebar(state.sidebarWidth + (event.key === "ArrowRight" ? 16 : -16)); } });
const sessionMenu = node("div", void 0, "menu-popup");
sessionMenu.id = "session-menu"; sessionMenu.hidden = true; document.body.append(sessionMenu);
sessionMenu.setAttribute("role", "menu");
sessionMenu.setAttribute("aria-label", "Session actions");
let menuAnchor;
function closeSessionMenu(restoreFocus=false) {
  sessionMenu.hidden=true;
  menuAnchor?.setAttribute("aria-expanded", "false");
  if(restoreFocus)menuAnchor?.focus();
}
function showSessionMenu(session, anchor, point) {
  closeSessionMenu();
  menuAnchor=anchor;
  anchor.setAttribute("aria-expanded", "true");
  sessionMenu.replaceChildren();
  const attached=!!session && !!state.terminal && sessionKey(session)===state.selected;
  const addAction = (label, icon, action, disabled=false) => {
    const item=button("", async () => { closeSessionMenu(); await action?.(); }, "mi");
    item.innerHTML=svgIcon(icon);item.append(node("span",label));
    item.setAttribute("role","menuitem");item.disabled=disabled;sessionMenu.append(item);
  };
  addAction("Rename…", "rename", () => renameSession(session), !session || session.stale);
  addAction("Restart…", "restart", () => changeSession(session,"restart"), !session || session.state?.interactive === false || session.stale);
  addAction("Stop…", "stop", () => changeSession(session,"stop"), !session?.state?.running || session.stale);
  sessionMenu.append(node("hr"));
  addAction("Reconnect", "restart", () => openTerminal(session, attached), !session || session.stale);
  addAction("Close terminal", "close", () => closeTerminal(), !attached);
  sessionMenu.append(node("hr"));
  addAction("Install package…", "install", () => installInto(session), !session || session.stale || session.harness === "agy");
  addAction("Save agentpack…", "package", () => saveAgentpack(session), !session || session.stale);
  sessionMenu.hidden=false;
  const rect=anchor.getBoundingClientRect();
  sessionMenu.style.left=`${Math.max(8,Math.min(innerWidth-sessionMenu.offsetWidth-8,point?.clientX ?? rect.left))}px`;
  sessionMenu.style.top=`${Math.max(8,Math.min(innerHeight-sessionMenu.offsetHeight-8,point?.clientY ?? rect.bottom+4))}px`;
  sessionMenu.querySelector("button:not(:disabled)")?.focus();
}
$("session-actions").setAttribute("aria-haspopup","menu");
$("session-actions").addEventListener("click", () => showSessionMenu(state.sessions.find(value => sessionKey(value) === state.selected), $("session-actions")));
sessionMenu.addEventListener("keydown", event => {
  const items=[...sessionMenu.querySelectorAll("button:not(:disabled)")];
  const current=items.indexOf(document.activeElement);
  if(["ArrowDown","ArrowUp","Home","End"].includes(event.key)){
    event.preventDefault();
    const next=event.key==="Home"?0:event.key==="End"?items.length-1:(current+(event.key==="ArrowDown"?1:-1)+items.length)%items.length;
    items[next]?.focus();
  }
  if(event.key==="Escape"){event.preventDefault();closeSessionMenu(true);}
  if(event.key==="Tab")closeSessionMenu();
});
document.addEventListener("pointerdown", event => { if (!sessionMenu.contains(event.target) && !menuAnchor?.contains(event.target)) closeSessionMenu(); });
function openAppearance(){
    moreMenu.hidden=true;
    showSheet("Appearance settings", '<label for="appearance-theme">Theme</label><select id="appearance-theme"><option value="system">System</option><option value="light">Light</option><option value="dark">Dark</option></select><p class="note">Applies across this app on this browser. Includes the terminal background, text, and ANSI palette.</p><p id="appearance-status" role="status"></p>', '<button type="button" id="appearance-done">Done</button>', false);
    $("appearance-theme").value=document.documentElement.dataset.theme||"system";
    $("appearance-theme").onchange=event=>{
      const theme=event.target.value;
      if(theme==="system")delete document.documentElement.dataset.theme;else document.documentElement.dataset.theme=theme;
      applyTerminalTheme();
      try{localStorage.setItem("agentpack-theme",theme);}catch{$("appearance-status").textContent="Theme applied. This browser could not save your preference.";}
    };
    $("appearance-done").onclick=closeSheet;
 }
const appearanceButton=button("",openAppearance,"tb-btn icon");
appearanceButton.setAttribute("aria-label","Terminal appearance");
appearanceButton.title="Terminal appearance settings";
appearanceButton.innerHTML='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><circle cx="12" cy="12" r="8"/><path d="M12 4a8 8 0 0 1 0 16Z" fill="currentColor" stroke="none"/></svg>';
$("more-tools").before(appearanceButton);
const moreMenu=node("div", void 0, "menu-popup"); moreMenu.hidden=true; document.body.append(moreMenu);
$("more-tools").addEventListener("click", () => {
  moreMenu.replaceChildren();
  moreMenu.append(button("Appearance settings", openAppearance, "mi"));
  for (const [label,view] of [["Recent folders","recent"],["Packages","packages"],["Gateway settings","settings"]]) moreMenu.append(button(label, () => { moreMenu.hidden=true; setView(view); }, "mi"));
  const rect=$("more-tools").getBoundingClientRect(); moreMenu.style.left=`${rect.left}px`; moreMenu.style.top=`${rect.bottom+4}px`; moreMenu.hidden=false;
});
document.addEventListener("pointerdown", event => { if (!moreMenu.contains(event.target) && event.target !== $("more-tools")) moreMenu.hidden=true; });
$("library-save").addEventListener("click",()=>saveAgentpack(state.sessions.find(session=>sessionKey(session)===state.selected)));
const keys = { escape: "\x1B", tab: "	", interrupt: "", up: "\x1B[A", down: "\x1B[B" };
document.querySelectorAll("[data-key]").forEach((element) => element.addEventListener("click", () => {
  sendInput(keys[element.dataset.key]);
  state.terminal?.focus();
}));
$("refresh").addEventListener("click", () => {
  notice("");
  refreshNodes();
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
    const found = list(await api("/api/nodes"), "nodes").map(remote => ({id:remote.id,name:remote.name || remote.id,online:remote.online !== false}));
    const current = new Set(found.map(remote => remote.id));
    state.nodes = [...found, ...state.nodes.filter(remote => !current.has(remote.id)).map(remote => ({...remote,online:false}))];
    for (const machine of state.nodes.filter(remote => remote.online === false)) {
      state.sessionCache.set(machine.id, (state.sessionCache.get(machine.id)||[]).map(session => ({...session,stale:true})));
    }
    renderSessions(); await refresh(); renderStartFolders();
    if (state.view === "library" || state.view === "machines") await loadWorkspaceData(true);
  } catch (error) { notice(error.message); }
}
setInterval(() => { if (!document.hidden) refreshNodes(); }, 15000);
setView("workspace");
try { const theme=localStorage.getItem("agentpack-theme"); if(["light","dark"].includes(theme))document.documentElement.dataset.theme=theme; } catch {}
applyTerminalTheme();
bootstrap().then(refreshNodes);
