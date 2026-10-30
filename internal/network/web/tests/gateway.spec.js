import { test, expect } from "@playwright/test";
const session = {id:"local-one",directory:"/work/my-project",harness:"claude",mode:"default",state:{running:true,rows:24,columns:80,exit_code:0}};
const second = {...session,id:"local-two",directory:"/work/another-project",harness:"codex"};
async function mockAPI(page,{authenticated=true,setup=false}={}) {
  const requests=[]; let loggedIn=authenticated, setupNeeded=setup;
  await page.route("**/api/**",async route=>{
    const req=route.request(),url=new URL(req.url()),body=req.method()==="GET"?undefined:req.postDataJSON();
    requests.push({path:url.pathname,query:url.searchParams,body});
    let data={};
    if(url.pathname==="/api/status") data={setup_required:setupNeeded,authenticated:loggedIn};
    else if(url.pathname==="/api/login") {loggedIn=true;data={};}
    else if(url.pathname==="/api/setup") {expect(body).toMatchObject({token:"private-token",password:"a-long-test-password"});setupNeeded=false;loggedIn=true;data={};}
    else if(url.pathname==="/api/logout") data={};
    else if(url.pathname==="/api/nodes") data=[];
    else if(url.pathname==="/api/sessions") data=req.method()==="POST"?{...session,...body,id:"created"}:[session,second];
    else if(url.pathname==="/api/recent") data=[{directory:"/work/previous",harness:"codex",mode:"default",available:true}];
    else if(url.pathname==="/api/packages") data=req.method()==="POST"?{}:[{module:"github.com/acme/tools",commit:"abcd1234"}];
    else if(url.pathname==="/api/settings") data={running:true,platform:"linux",sharing_enabled:true,allowed_peers:["tag:server"]};
    else if(url.pathname==="/api/limits") data={limits:[{harness:"codex",state:"available",source:"codex app-server account/rateLimits/read",windows:[{id:"codex/primary",label:"Codex 5h",used_percent:28,remaining_percent:72,reset_at:"2026-10-30T16:10:00Z"}]},{harness:"grok",state:"error",source:"grok agent stdio _x.ai/billing",error:"grok billing: authentication required",windows:[]}]};
    else if(url.pathname==="/api/settings/sharing") data={sharing_enabled:body.enabled};
    await route.fulfill({json:data});
  });
  return requests;
}
async function attachTerminal(page,{terminalState={},holdReplay=false,initialQuery=""}={}) {
  const controls=[]; const sockets=[];
  await page.routeWebSocket("**/api/sessions/*/terminal",ws=>{
    sockets.push(ws);
    ws.send(JSON.stringify({type:"attached",attachment:42,state:{...session.state,shared_input:true,responder:42,...terminalState}}));
    ws.send(Buffer.from(`READY\r\n${initialQuery}`));
    if(!holdReplay && terminalState.shared_input !== false) ws.send(JSON.stringify({type:"replay_complete"}));
    ws.onMessage(message=>{
      controls.push(message);
    });
  });
  controls.sockets=sockets;
  return controls;
}
test("setup, login and settings retain their gateway behavior",async({page})=>{
  await mockAPI(page,{authenticated:false,setup:true});
  await page.goto("/");
  await expect(page.getByRole("heading",{name:"Set up your gateway"})).toBeVisible();
  await page.getByLabel("Setup token").fill("private-token");
  await page.getByLabel("Root account password").fill("a-long-test-password");
  await page.getByRole("button",{name:"Create root account"}).click();
  await expect(page.locator("[data-session=local-one]")).toBeVisible();
  await page.getByRole("button",{name:"Gateway menu"}).click(); await page.getByRole("button",{name:"Gateway settings"}).click();
  await expect(page.getByText("tag:server",{exact:true})).toBeVisible();
});
test("host grid stays fixed while fit, zoom and browser size change",async({page})=>{
  const requests=await mockAPI(page); const controls=await attachTerminal(page);
  await page.goto("/"); await page.locator("[data-session=local-one]").click();
  await expect(page.locator(".xterm-screen")).toBeVisible();
  await expect(page.locator("#grid-label")).toHaveText("80 × 24");
  await page.setViewportSize({width:1024,height:700}); await page.getByRole("button",{name:"1:1"}).click();
  await page.getByRole("button",{name:"Zoom in"}).click(); await page.getByRole("button",{name:"Fit"}).click();
  await page.setViewportSize({width:1440,height:1000}); await page.waitForTimeout(100);
  await expect(page.locator("#grid-label")).toHaveText("80 × 24");
  expect(controls.some(value=>value.action==="resize")).toBeFalsy();
  expect(requests.some(value=>value.path==="/api/sessions"&&value.query.has("node"))).toBeFalsy();
});
test("sidebar stays left, resizes, collapses and selection follows arrow keys",async({page})=>{
  await mockAPI(page); await attachTerminal(page); await page.goto("/");
  const sidebar=page.locator(".sessions"),splitter=page.locator("#splitter");
  expect(await sidebar.evaluate(el=>el.getBoundingClientRect().left)).toBeLessThan(await page.locator(".term-col").evaluate(el=>el.getBoundingClientRect().left));
  const box=await splitter.boundingBox(); await page.mouse.move(box.x+1,box.y+20); await page.mouse.down(); await page.mouse.move(340,box.y+20); await page.mouse.up();
  await expect.poll(()=>page.locator(".workspace").evaluate(el=>getComputedStyle(el).gridTemplateColumns)).toContain("340px");
  await page.locator("[data-session=local-one]").click(); await page.keyboard.press("ArrowDown");
  await expect(page.locator("#terminal-title")).toHaveText("another-project");
  await page.getByRole("button",{name:"Hide sessions"}).click();
  await expect(page.locator(".workspace")).toHaveClass(/no-sidebar/);
});
test("session hierarchy has icons and keeps collapsed groups through refresh",async({page})=>{
  await mockAPI(page);await page.goto("/");
  const group=page.locator("#sessions details.group").first();
  await expect(group.locator("summary svg")).toHaveCount(2);
  await expect(group.locator(".subhead svg")).toHaveCount(2);
  await expect(group.locator(".srow").first()).toBeVisible();
  await expect.poll(()=>group.evaluate(el=>el.querySelector(".srow .label").getBoundingClientRect().left-el.querySelector(".subhead span").getBoundingClientRect().left)).toBeGreaterThan(0);
  await group.locator("summary").click();await expect(page.locator("[data-session=local-one]")).toBeHidden();
  await page.locator("#refresh").click();await expect(group).not.toHaveAttribute("open");
  await group.locator("summary").click();await expect(page.locator("[data-session=local-one]")).toBeVisible();
});
test("context rename saves on the correct host and survives refresh and reload",async({page})=>{
  await mockAPI(page);await attachTerminal(page);
  let name="",renamed;
  await page.route("**/api/nodes",route=>route.fulfill({json:[{id:"peer-1",name:"Build server",online:true}]}));
  await page.route("**/api/sessions?node=peer-1",route=>route.fulfill({json:[{...session,id:"remote-one",name}]}));
  await page.route("**/api/sessions/remote-one?node=peer-1",route=>{renamed=route.request().postDataJSON();name=renamed.name;return route.fulfill({json:{...session,id:"remote-one",name}});});
  await page.goto("/");
  const row=page.locator("[data-session=remote-one]");await row.click();
  await row.click({button:"right"});await page.getByRole("menuitem",{name:"Rename…"}).click();
  await page.getByLabel("Name",{exact:true}).fill("Fix sidebar");await page.getByLabel("Name",{exact:true}).press("Enter");
  await expect(page.locator("#sheet")).toBeHidden();await expect(row).toContainText("Fix sidebar");await expect(page.locator("#terminal-title")).toHaveText("Fix sidebar");
  expect(renamed).toEqual({name:"Fix sidebar"});
  await page.locator("#refresh").click();await expect(row).toContainText("Fix sidebar");
  await page.reload();await expect(row).toContainText("Fix sidebar");
  await page.getByLabel("Filter sessions").fill("Fix sidebar");await expect(page.locator(".srow")).toHaveCount(1);
});
test("row actions rename without attaching, preserve errors, cancel, and stop the chosen session",async({page})=>{
  const requests=await mockAPI(page);let attempts=0;
  await page.route("**/api/sessions/local-two",route=>{attempts++;return route.fulfill({status:500,body:"Could not save the name"});});
  await page.goto("/");
  await page.getByRole("button",{name:"Actions for another-project"}).click();
  await page.getByRole("menuitem",{name:"Rename…"}).click();await page.getByLabel("Name",{exact:true}).fill("Keep this draft");
  await page.getByRole("button",{name:"Rename",exact:true}).click();
  await expect(page.locator("#session-name-error")).toHaveText("Could not save the name");await expect(page.getByLabel("Name",{exact:true})).toHaveValue("Keep this draft");
  await page.keyboard.press("Escape");await expect(page.locator("#sheet")).toBeHidden();expect(attempts).toBe(1);
  const row=page.locator("[data-session=local-two]");await row.focus();await page.keyboard.press("Shift+F10");await expect(page.getByRole("menuitem",{name:"Rename…"})).toBeFocused();
  await page.keyboard.press("Escape");await expect(page.locator("#session-menu")).toBeHidden();await expect(row).toBeFocused();
  await row.click({button:"right"});page.once("dialog",dialog=>dialog.accept());await page.getByRole("menuitem",{name:"Stop…"}).click();
  await expect.poll(()=>requests.some(item=>item.path==="/api/sessions/local-two/stop")).toBeTruthy();
  expect(requests.some(item=>item.path==="/api/sessions/local-one/stop")).toBeFalsy();await expect(page.locator("#terminal-title")).toHaveText("Sessions");
});
test("Library keeps installed packages when an older peer has no Library API",async({page})=>{
  await mockAPI(page);
  await page.route("**/api/nodes",route=>route.fulfill({json:[{id:"peer-1",name:"Build server",online:true}]}));
  await page.route("**/api/recent?node=peer-1",route=>route.fulfill({json:[{directory:"/srv/app",available:true}]}));
  await page.route("**/api/packages?**",route=>route.fulfill({json:[{module:"github.com/acme/installed",commit:"abc123",kind:"plugin",name:"Installed tools"}]}));
  await page.route("**/api/library/packages**",route=>route.fulfill({json:[{id:"saved-one",module:"github.com/acme/saved",commit:"def456",name:"Saved tools"}]}));
  await page.route("**/api/library/agentpacks**",route=>route.fulfill({status:404,body:"404 page not found"}));
  await page.goto("/");await page.getByRole("button",{name:"Library",exact:true}).click();
  await expect(page.locator("#library-list")).toContainText("Installed tools");await expect(page.locator("#library-list")).toContainText("Saved tools");
  await page.locator("#library-list button").filter({hasText:"Installed tools"}).click();
  await expect(page.locator("#library-detail")).toContainText("github.com/acme/installed");await expect(page.locator("#library-detail .installed")).toContainText("Build server");
  await expect(page.locator("#library-list")).not.toContainText("404 page not found");
  await expect(page.locator("#library-summary")).not.toContainText("unavailable");
  await expect(page.locator("#library-list")).toContainText("Update Agentpack on Build server");
});
test("legacy worker stays read-only and offers no control toggle",async({page})=>{
  await mockAPI(page); const received=await attachTerminal(page,{terminalState:{shared_input:false,responder:0}}); await page.goto("/");
  await page.locator("[data-session=local-one]").click();
  await expect(page.locator("#terminal-status")).toHaveText("Read-only");
  await expect(page.locator("#terminal-notice")).toHaveText("Start a new session to enable shared input.");
  await expect(page.getByRole("button",{name:/Take control|Release control/})).toHaveCount(0);
  await page.locator(".xterm-screen").click(); await page.keyboard.press("Control+C");
  expect(received.some(value=>typeof value!=="string"&&Buffer.from(value).equals(Buffer.from([3])))).toBeFalsy();
});
test("390px switches between the session list and terminal panes",async({page})=>{
  await mockAPI(page); await attachTerminal(page); await page.setViewportSize({width:390,height:844}); await page.goto("/");
  await expect(page.locator(".sessions")).toBeVisible();
  await page.getByRole("button",{name:"Hide sessions"}).click();
  await expect(page.locator(".workspace")).not.toHaveClass(/show-sessions/);
  await expect(page.locator(".term-col")).toBeVisible();
  await page.getByRole("button",{name:"Show sessions"}).click();
  await page.getByRole("button",{name:"Actions for my-project"}).click();
  await page.getByRole("menuitem",{name:"Rename…"}).click();
  await expect(page.getByLabel("Name",{exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Cancel",exact:true}).click();
  await page.locator("[data-session=local-one]").click();
  await expect(page.locator(".term-col")).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBeTruthy();
});
test("sessions render per machine as results arrive and preserve rows after a peer goes offline",async({page})=>{
  await mockAPI(page);
  let online=true;
  await page.route("**/api/nodes",route=>route.fulfill({json:[{id:"peer-1",name:"Build server",online}]}));
  await page.route("**/api/sessions?node=peer-1",async route=>{
    await new Promise(resolve=>setTimeout(resolve,online?700:100));
    await route.fulfill({json:[{...session,id:"remote-one",directory:"/srv/release",state:{...session.state,columns:120,rows:36}}]});
  });
  await page.route("**/api/recent?node=peer-1",route=>route.fulfill({json:[]}));
  await page.goto("/");
  await expect(page.locator("[data-session=local-one]")).toBeVisible();
  await expect(page.locator("[data-session=remote-one]")).toHaveCount(0);
  await expect(page.locator("[data-session=remote-one]")).toBeVisible();
  online=false; await page.getByRole("button",{name:"Refresh"}).click();
  await expect(page.locator("[data-session=remote-one]")).toHaveClass(/offline/);
  await expect(page.locator("[data-session=remote-one]")).toHaveAttribute("title",/Offline/);
});
test("project grouping keeps identical folder names on different machines distinct",async({page})=>{
  await mockAPI(page);
  await page.route("**/api/nodes",route=>route.fulfill({json:[{id:"peer-1",name:"Build server",online:true}]}));
  await page.route("**/api/sessions?node=peer-1",route=>route.fulfill({json:[{...session,id:"remote-one",directory:"/srv/my-project"}]}));
  await page.goto("/"); await page.getByRole("button",{name:"Project",exact:true}).click();
  await expect(page.locator(".group-label")).toHaveCount(3);
  expect(await page.locator(".group-label").allTextContents()).toContain("my-project");
  expect(await page.locator(".group-label").evaluateAll(rows=>rows.map(row=>row.title))).toContain("/srv/my-project");
});
test("new session only offers recent folders and routes start to the chosen machine",async({page})=>{
  const requests=await mockAPI(page); await page.goto("/");
  await page.getByRole("button",{name:"New session"}).click();
  await expect(page.locator("#start-directory option")).toHaveText(["/work/previous"]);
  await page.locator("#start-form button[type=submit]").click();
  await expect(page.locator("#start-dialog")).toBeHidden();
  expect(requests.some(value=>value.path==="/api/sessions"&&value.body?.directory==="/work/previous")).toBeTruthy();
});
test("existing package installation remains available from More",async({page})=>{
  const requests=await mockAPI(page); await page.goto("/");
  await page.getByRole("button",{name:"Gateway menu"}).click(); await page.getByRole("button",{name:"Packages",exact:true}).click();
  await page.locator("#package-directory").fill("/work/my-project"); await page.getByRole("button",{name:"Load packages"}).click();
  await expect(page.getByText("github.com/acme/tools")).toBeVisible();
  await page.locator("#package-spec").fill("acme/skill"); await page.getByRole("button",{name:"Install package",exact:true}).click();
  await expect(page.getByText("Package installed. Affected agents have been restarted.")).toBeVisible();
  expect(requests.some(value=>value.path==="/api/packages"&&value.body?.spec==="acme/skill")).toBeTruthy();
});
test("shared input works without ownership and detaching never stops the agent",async({page})=>{
  const requests=await mockAPI(page); const received=await attachTerminal(page);
  await page.goto("/"); await page.locator("[data-session=local-one]").click();
  await expect(page.locator("#terminal-status")).toHaveText("Shared input");
  await page.locator(".xterm-screen").click(); await page.keyboard.press("Control+C");
  await expect.poll(()=>received.some(data=>typeof data!=="string"&&Buffer.from(data).equals(Buffer.from([3])))).toBeTruthy();
  await page.getByRole("button",{name:"Session actions"}).click();
  await expect(page.getByRole("menuitem",{name:/Take control|Release control/})).toHaveCount(0);
  await page.getByRole("menuitem",{name:"Close terminal"}).click();
  expect(requests.some(value=>value.path.endsWith("/stop"))).toBeFalsy();
});
test("replay blocks input and query replies until rendered, then only the responder answers",async({page})=>{
  const received=await attachTerminal(page,{terminalState:{responder:7},holdReplay:true,initialQuery:"\x1b[6n"});
  await mockAPI(page); await page.goto("/"); await page.locator("[data-session=local-one]").click();
  await expect(page.locator("#terminal-status")).toHaveText("Replaying output");
  await page.locator(".xterm-screen").click(); await page.keyboard.press("Control+C");
  await page.waitForTimeout(50);
  const text=value=>typeof value==="string"?value:Buffer.from(value).toString();
  expect(received).toHaveLength(0);
  received.sockets[0].send(JSON.stringify({type:"replay_complete"}));
  await expect(page.locator("#terminal-status")).toHaveText("Shared input");
  received.sockets[0].send(Buffer.from("\x1b[6n"));
  await page.locator(".xterm-screen").click(); await page.keyboard.press("Control+C");
  await expect.poll(()=>received.some(value=>typeof value!=="string"&&Buffer.from(value).equals(Buffer.from([3])))).toBeTruthy();
  expect(received.some(value=>text(value).includes("\x1b[2;1R"))).toBeFalsy();
  await page.locator(".xterm-helper-textarea").evaluate(el=>{const clipboard=new DataTransfer();clipboard.setData("text/plain","paste");el.dispatchEvent(new ClipboardEvent("paste",{clipboardData:clipboard,bubbles:true}));});
  await expect.poll(()=>received.some(value=>text(value).includes("paste"))).toBeTruthy();
  received.sockets[0].send(JSON.stringify({type:"state",state:{...session.state,shared_input:true,responder:42}}));
  received.sockets[0].send(Buffer.from("\x1b[6n"));
  await expect.poll(()=>received.some(value=>text(value).includes("\x1b[2;1R"))).toBeTruthy();
});
test("clicking the selected row preserves its terminal and compact menu aligns icons and labels",async({page})=>{
  await mockAPI(page); const received=await attachTerminal(page); await page.goto("/"); const row=page.locator("[data-session=local-one]"); await row.click();
  await expect(page.locator("#terminal-status")).toHaveText("Shared input");
  await page.getByRole("button",{name:"1:1"}).click();
  await page.evaluate(()=>window.savedTerminal=document.querySelector(".xterm"));
  const scale=await page.locator("#zoom-value").textContent(); await row.click();
  await expect.poll(()=>received.sockets.length).toBe(1);
  expect(await page.evaluate(()=>window.savedTerminal===document.querySelector(".xterm"))).toBeTruthy();
  await expect(page.locator("#zoom-value")).toHaveText(scale);
  await page.getByRole("button",{name:"Session actions"}).click();
  const item=page.getByRole("menuitem",{name:"Rename…"});
  expect(await item.evaluate(el=>getComputedStyle(el).justifyContent)).toBe("flex-start");
  expect(await item.locator("svg").evaluate(icon=>icon.getBoundingClientRect().left)).toBeLessThan(await item.locator("span").evaluate(label=>label.getBoundingClientRect().left));
  await expect(page.getByRole("menuitem",{name:/Take control|Release control/})).toHaveCount(0);
  await page.getByRole("menuitem",{name:"Reconnect"}).click(); await expect.poll(()=>received.sockets.length).toBe(2);
  expect(await page.evaluate(()=>window.savedTerminal===document.querySelector(".xterm"))).toBeTruthy();
  await page.getByRole("button",{name:"Actions for another-project"}).click(); await page.getByRole("menuitem",{name:"Reconnect"}).click();
  await expect.poll(()=>received.sockets.length).toBe(3); await expect(page.locator("#terminal-title")).toHaveText("another-project");
});
test("backend strings are text and API failures stay visible",async({page})=>{
  await mockAPI(page);
  await page.route("**/api/sessions",route=>route.fulfill({json:[{...session,directory:"/work/<img src=x onerror=alert(1)>"}]}));
  await page.goto("/");
  await expect(page.locator(".srow")).toContainText("<img src=x onerror=alert(1)>");
  await expect(page.locator("img")).toHaveCount(0);
  await page.getByRole("button",{name:"Gateway menu"}).click(); await page.getByRole("button",{name:"Packages",exact:true}).click();
  await page.route("**/api/packages?**",route=>route.fulfill({status:500,json:{error:"Project is not available"}}));
  await page.locator("#package-directory").fill("/missing"); await page.getByRole("button",{name:"Load packages"}).click();
  await expect(page.getByText("Project is not available")).toBeVisible();
});
test("external login return URLs are rejected",async({page})=>{
  await mockAPI(page,{authenticated:false}); await page.goto("/?return_to=%2F%2Fevil.example%2F");
  await page.getByLabel("Root account password").fill("a-long-test-password"); await page.getByRole("button",{name:"Sign in",exact:true}).click();
  await expect(page.locator("[data-session=local-one]")).toBeVisible();
  expect(new URL(page.url()).hostname).toBe("127.0.0.1");
});
test("native stdio and Antigravity sessions keep their local-only restrictions",async({page})=>{
  await mockAPI(page);
  await page.route("**/api/sessions",route=>route.fulfill({json:[{...session,state:{...session.state,interactive:false}}]}));
  await page.goto("/"); await page.locator("[data-session=local-one]").click();
  await expect(page.getByText("Native stdio · browser input unavailable")).toBeVisible();
  await expect(page.getByRole("button",{name:/Take control|Release control/})).toHaveCount(0);
  await page.getByRole("button",{name:"Session actions"}).click();
  await expect(page.getByRole("menuitem",{name:"Restart…"})).toBeDisabled();
  await expect(page.getByRole("menuitem",{name:"Stop…"})).toBeEnabled();
  page.on("dialog",dialog=>dialog.accept()); await page.getByRole("menuitem",{name:"Stop…"}).click();
  await expect(page.locator("#notice")).toHaveText("Agent stopped.");
  await page.route("**/api/sessions",route=>route.fulfill({json:[{...session,harness:"agy",state:{...session.state,interactive:false}}]}));
  await page.getByRole("button",{name:"Refresh status"}).click(); await page.locator("[data-session=local-one]").click();
  await expect(page.getByText("Desktop launcher presence · start and interact locally. Detached desktop windows are not tracked.")).toBeVisible();
  await page.getByRole("button",{name:"New session"}).click();
  await expect(page.locator("#start-harness option[value=agy]")).toHaveCount(0);
  await page.getByRole("button",{name:"Close new session"}).click();
});
test("peer sessions load independently and terminal links retain the node",async({page})=>{
  await mockAPI(page);
  await page.route("**/api/nodes",route=>route.fulfill({json:[{id:"n-main",name:"Build server",online:true}]}));
  const urls=[];
  await page.route("**/api/sessions?node=n-main",route=>route.fulfill({json:[{...session,id:"remote-one",directory:"/srv/project"}]}));
  await page.routeWebSocket(/\/api\/sessions\/[^/]+\/terminal/,ws=>{urls.push(ws.url());ws.send(JSON.stringify({type:"attached",attachment:42,state:{...session.state,shared_input:true,responder:42}}));ws.send(JSON.stringify({type:"replay_complete"}));});
  await page.goto("/"); await expect(page.locator("[data-session=remote-one]")).toBeVisible();
  await page.locator("[data-session=remote-one]").click();
  await expect.poll(()=>urls.length).toBe(1);
  expect(new URL(urls[0]).searchParams.get("node")).toBe("n-main");
  expect((await page.locator(".group-label").allTextContents()).join(" ")).toContain("Build server");
});
test("forgetting a recent folder does not stop a session",async({page})=>{
  const requests=await mockAPI(page); let recent=[{directory:"/work/previous",harness:"codex",mode:"default",available:true}];
  await page.route("**/api/recent",route=>{
    if(route.request().method()==="DELETE"){expect(route.request().postDataJSON()).toEqual({directory:"/work/previous",harness:"codex",mode:"default"});recent=[];return route.fulfill({json:{forgotten:true}});}
    return route.fulfill({json:recent});
  });
  await page.goto("/"); await page.getByRole("button",{name:"Gateway menu"}).click(); await page.getByRole("button",{name:"Recent folders"}).click();
  await page.getByRole("button",{name:"Forget",exact:true}).click();
  await expect(page.getByText("Folders will appear here after you launch an agent.")).toBeVisible();
  expect(requests.some(value=>value.path.endsWith("/stop"))).toBeFalsy();
});
test("restarting keeps the same operation ID after ambiguous failures",async({page})=>{
  await mockAPI(page); const ids=[];
  await page.route("**/api/sessions/*/restart",async route=>{
    const id=route.request().postDataJSON().operation_id; ids.push(id);
    if(ids.length===1)return route.abort("failed");
    if(ids.length===2)return route.fulfill({status:502,json:{error:"Worker response unavailable"}});
    if(ids.length===4)return route.fulfill({status:409,json:{error:"Restart rejected"}});
    return route.fulfill({json:{restarted:true}});
  });
  page.on("dialog",dialog=>dialog.accept()); await page.goto("/"); await page.locator("[data-session=local-one]").click();
  const restart=async()=>{await page.getByRole("button",{name:"Session actions"}).click();await page.getByRole("menuitem",{name:"Restart…"}).click();};
  await restart(); await expect(page.locator("#notice")).toContainText(/Failed to fetch|Load failed/);
  await restart(); await expect(page.locator("#notice")).toHaveText("Worker response unavailable");
  await restart(); await expect(page.locator("#notice")).toHaveText("Agent restart requested."); expect(ids[1]).toBe(ids[0]);expect(ids[2]).toBe(ids[0]);
  await restart(); await expect(page.locator("#notice")).toHaveText("Restart rejected"); expect(ids[3]).not.toBe(ids[2]);
  await restart(); await expect(page.locator("#notice")).toHaveText("Agent restart requested."); expect(ids[4]).not.toBe(ids[3]);
});
test("recent folders retain unavailable reasons and only available paths can start",async({page})=>{
  await mockAPI(page);
  const lastUsed="2026-10-26T10:30:00Z";
  await page.route("**/api/recent",route=>route.fulfill({json:[
    {directory:"/work/available",harness:"codex",mode:"default",available:true,last_used:lastUsed},
    {directory:"/work/missing",harness:"codex",mode:"default",available:false,last_used:lastUsed,unavailable_reason:"Folder is missing or inaccessible"},
    {directory:"/work/replaced",harness:"claude",mode:"default",available:false,unavailable_reason:"Folder changed since its last local launch"}
  ]}));
  await page.goto("/"); await page.getByRole("button",{name:"Gateway menu"}).click(); await page.getByRole("button",{name:"Recent folders"}).click();
  const starts=page.getByRole("button",{name:"Start fresh",exact:true}); await expect(starts.nth(0)).toBeEnabled(); await expect(starts.nth(1)).toBeDisabled(); await expect(starts.nth(2)).toBeDisabled();
  await expect(page.getByText("Unavailable · Folder is missing or inaccessible")).toBeVisible();
  await expect(page.getByText("Unavailable · Folder changed since its last local launch")).toBeVisible();
  await expect(page.locator("#recent time").first()).toHaveAttribute("datetime",new Date(lastUsed).toISOString());
});
test("gateway settings stay separate from selected remote sessions",async({page})=>{
  const requests=await mockAPI(page); let enabled=true;
  const settingsQueries=[]; await page.route("**/api/settings",route=>{settingsQueries.push(new URL(route.request().url()).search);return route.fulfill({json:{running:true,platform:"linux",sharing_enabled:enabled,allowed_peers:["tag:server"]}});});
  await page.route("**/api/settings/sharing",route=>{enabled=route.request().postDataJSON().enabled;return route.fulfill({json:{sharing_enabled:enabled}});});
  await page.goto("/"); await page.locator("[data-session=local-one]").click();
  await page.getByRole("button",{name:"Gateway menu"}).click(); await page.getByRole("button",{name:"Gateway settings"}).click();
  await expect(page.getByText("tag:server",{exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Stop sharing",exact:true}).click(); await expect(page.getByRole("button",{name:"Enable sharing",exact:true})).toBeEnabled();
  expect(settingsQueries.every(value=>!value.includes("node="))).toBeTruthy();
  page.on("dialog",dialog=>dialog.accept()); await page.getByRole("button",{name:"Stop all agents on gateway"}).click();
  await expect(page.locator("#settings-status")).toHaveText("Agents on the gateway machine have been stopped.");
  expect(requests.some(value=>value.path==="/api/node/stop-agents")).toBeTruthy();
});
test("a dropped terminal reconnects and replays output",async({page})=>{
  await mockAPI(page); const sockets=[];
  await page.routeWebSocket("**/api/sessions/*/terminal",ws=>{sockets.push(ws);ws.send(JSON.stringify({type:"attached",attachment:42,state:{...session.state,shared_input:true,responder:42}}));ws.send(Buffer.from(`ATTACH ${sockets.length}\r\n`));ws.send(JSON.stringify({type:"replay_complete"}));});
  await page.goto("/"); await page.locator("[data-session=local-one]").click(); await expect(page.locator("#terminal-status")).toHaveText("Shared input");
  sockets[0].close(); await expect(page.locator("#terminal-status")).toContainText("reconnecting");
  await expect.poll(()=>sockets.length).toBe(2); await expect(page.locator("#terminal-mount")).toContainText("ATTACH 2");
  await page.getByRole("button",{name:"Session actions"}).click(); await page.getByRole("menuitem",{name:"Close terminal"}).click();
  await page.waitForTimeout(1200); expect(sockets.length).toBe(2);
});
test("a restarted process that exits immediately still replays its final output",async({page})=>{
  await mockAPI(page); let phase="running"; const sockets=[];
  const stopped={...session.state,running:false,restarting:false,exit_code:1};
  await page.route("**/api/sessions",route=>{const state=phase==="restarting"?{...session.state,running:false,restarting:true}:phase==="stopped"?stopped:session.state;if(phase==="restarting")phase="stopped";return route.fulfill({json:[{...session,state}]});});
  await page.routeWebSocket("**/api/sessions/*/terminal",ws=>{
    sockets.push(ws); const current={...(sockets.length===1?session.state:stopped),shared_input:true,responder:42};
    ws.send(JSON.stringify({type:"attached",attachment:42,state:current}));
    if(sockets.length===2){ws.send(Buffer.from("REPLACEMENT FAILED: invalid configuration\r\n"));ws.send(JSON.stringify({type:"replay_complete"}));ws.send(JSON.stringify({type:"ended",state:stopped}));}
  });
  await page.goto("/"); await page.locator("[data-session=local-one]").click();
  phase="restarting"; sockets[0].send(JSON.stringify({type:"ended",state:{...session.state,running:false,restarting:true}})); sockets[0].close();
  await expect.poll(()=>sockets.length).toBe(2);
  await expect(page.locator("#terminal-mount")).toContainText("REPLACEMENT FAILED: invalid configuration");
  await expect(page.locator("#terminal-status")).toHaveText("Stopped (1)");
  await expect(page.getByRole("button",{name:/Take control|Release control/})).toHaveCount(0);
});
test("sessions layout renders at desktop, tablet and phone sizes in both themes",async({page})=>{
  await mockAPI(page); await attachTerminal(page); await page.goto("/"); await page.locator("[data-session=local-one]").click();
  for(const [width,height,theme] of [[1440,1000,"light"],[1440,1000,"dark"],[1024,768,"light"],[1024,768,"dark"],[390,844,"light"],[390,844,"dark"]]){
    await page.emulateMedia({colorScheme:theme}); await page.setViewportSize({width,height}); await page.waitForTimeout(100);
    await page.screenshot({path:`test-results/sessions-${width}-${theme}-${test.info().project.name}.png`});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBeTruthy();
  }
});

test("Library install routes exact pins, limits concurrency per machine, and keeps row errors",async({page})=>{
  await mockAPI(page);
  const requests=[];let active=0,maximum=0;
  const item={id:"package-1",module:"github.com/acme/tools",commit:"deadbeef1234567890",content_hash:"sha256-tree-v1:abc",kind:"plugin",name:"Tools",url:"https://github.com/acme/tools",owner:"acme",repo:"tools",cache_key:"cache-key"};
  await page.route("**/api/nodes",route=>route.fulfill({json:[{id:"peer-1",name:"Build server",online:true}]}));
  await page.route("**/api/recent**",route=>route.fulfill({json:route.request().url().includes("node=peer-1")?[]:[
    {directory:"/work/one",harness:"claude",mode:"default",available:true},
    {directory:"/work/two",harness:"claude",mode:"default",available:true},
    {directory:"/work/three",harness:"claude",mode:"default",available:true}
  ]}));
  await page.route("**/api/packages**",async route=>{
    const url=new URL(route.request().url());
    if(route.request().method()==="GET")return route.fulfill({json:url.searchParams.get("directory")==="/work/one"?[{module:item.module,commit:item.commit}]:[]});
    const body=route.request().postDataJSON();requests.push({body,node:url.searchParams.get("node")});active++;maximum=Math.max(maximum,active);
    await new Promise(resolve=>setTimeout(resolve,80));active--;
    if(body.directory==="/work/three")return route.fulfill({status:500,json:{error:"Repository is unavailable"}});
    return route.fulfill({json:{installed:true}});
  });
  await page.route("**/api/library/packages**",route=>route.fulfill({json:[item]}));
  await page.route("**/api/library/agentpacks**",route=>route.fulfill({json:[]}));
  await page.goto("/");await page.getByRole("button",{name:"Library",exact:true}).click();
  await page.locator("#library-list button").first().click();await page.locator("#install-item").click();
  await page.getByRole("button",{name:"Select all online"}).click();
  await expect(page.locator("#install-destinations")).toContainText("Installed");
  await page.getByLabel("Restart affected agents").uncheck();
  await page.getByRole("button",{name:/Install in 2 projects/}).click();
  await expect(page.locator("#sheet-apply")).toHaveText("Done");
  await expect(page.locator("#install-destinations")).toContainText("Installed");
  await expect(page.locator("#install-destinations")).toContainText("Repository is unavailable");
  expect(maximum).toBe(2);expect(requests).toHaveLength(2);
  expect(requests.every(({body,node})=>body.spec===`${item.module}@${item.commit}`&&body.restart===false&&!node)).toBeTruthy();
});

test("save, rename, preview and restore an agentpack from an attached session",async({page})=>{
  await mockAPI(page);
  const pack={id:"pack-1",name:"Project setup",revisions:["rev-1"],heads:["rev-1"]};
  const revision={id:"rev-1",agentpack_id:pack.id,manifest:'name = "project"\n',lock:"",package_ids:[],project:"/work/my-project",created_at:"2026-10-20T10:00:00Z",label:"Initial",mode:"default"};
  let renameBody,saveBody,restoreBody,previewCount=0;
  await page.route("**/api/recent",route=>route.fulfill({json:[{directory:"/work/my-project",harness:"claude",mode:"default",available:true}]}));
  await page.route("**/api/library/agentpacks",async route=>route.fulfill({json:route.request().method()==="POST"?[pack]:[pack]}));
  await page.route("**/api/library/agentpacks/pack-1",async route=>{
    if(route.request().method()==="PATCH"){renameBody=route.request().postDataJSON();pack.name=renameBody.name;return route.fulfill({json:pack});}
    return route.fulfill({json:{agentpack:pack,revisions:[revision]}});
  });
  await page.route("**/api/library/agentpacks",async route=>{
    if(route.request().method()==="POST"){saveBody=route.request().postDataJSON();return route.fulfill({json:{agentpack:pack,revision}});}
    return route.fulfill({json:[pack]});
  });
  await page.route("**/api/library/agentpacks/pack-1/revisions/rev-1/restore-preview**",route=>{previewCount++;return route.fulfill({json:{add:["github.com/acme/new"],update:[],remove:[],mode:"default"}});});
  await page.route("**/api/library/agentpacks/pack-1/revisions/rev-1/restore",route=>{restoreBody=route.request().postDataJSON();return route.fulfill({json:{restored:true}});});
  await page.goto("/");await page.locator("[data-session=local-one]").click();
  await page.getByRole("button",{name:"Library",exact:true}).click();await page.getByRole("button",{name:"Save agentpack…"}).click();
  await page.getByLabel("Revision note").fill("Initial");await page.getByRole("button",{name:"Save to Library"}).click();
  await expect(page.getByRole("heading",{name:"Project setup"})).toBeVisible();
  expect(saveBody).toMatchObject({directory:"/work/my-project",name:"Project setup",label:"Initial",mode:"default",auto_capture:true});
  await page.getByRole("button",{name:"Rename…"}).click();await page.getByLabel("Name",{exact:true}).fill("Renamed setup");await page.getByRole("button",{name:"Rename",exact:true}).click();
  await expect(page.getByRole("heading",{name:"Renamed setup"})).toBeVisible();expect(renameBody).toEqual({name:"Renamed setup"});
  await page.getByRole("button",{name:/Restore/}).click();await expect(page.getByText("Add github.com/acme/new")).toBeVisible();
  await page.getByLabel("Restart affected agents after restore").uncheck();await page.getByRole("button",{name:"Restore",exact:true}).click();
  await expect(page.locator("#restore-apply")).toHaveText("Done");expect(previewCount).toBeGreaterThan(0);expect(restoreBody).toEqual({directory:"/work/my-project",restart:false});
});

test("Machines matrix shows real package pins and project install actions",async({page})=>{
  await mockAPI(page);
  const item={id:"package-1",module:"github.com/acme/tools",commit:"abcd1234",kind:"plugin",name:"Tools"};
  await page.route("**/api/recent",route=>route.fulfill({json:[{directory:"/work/previous",harness:"codex",mode:"default",available:true}]}));
  await page.route("**/api/packages?**",route=>route.fulfill({json:[{module:item.module,commit:item.commit}]}));
  await page.route("**/api/library/packages**",route=>route.fulfill({json:[item]}));
  await page.route("**/api/library/agentpacks**",route=>route.fulfill({json:[]}));
  await page.goto("/");await page.getByRole("button",{name:"Machines",exact:true}).click();
  await expect(page.getByRole("heading",{name:"Gateway machine"})).toBeVisible();
  await expect(page.locator(".matrix")).toContainText("previous");await expect(page.locator(".matrix")).toContainText("abcd1234");
  await expect(page.getByRole("button",{name:"Install into /work/previous"})).toBeEnabled();
});

test("reconnect preserves and dims the last frame until replay is rendered",async({page})=>{
  await mockAPI(page);const sockets=[];
  await page.routeWebSocket("**/api/sessions/*/terminal",ws=>{
    sockets.push(ws);ws.send(JSON.stringify({type:"attached",attachment:40+sockets.length,state:{...session.state,shared_input:true,responder:40+sockets.length}}));
    if(sockets.length===1){ws.send(Buffer.from("LAST FRAME\r\n"));ws.send(JSON.stringify({type:"replay_complete"}));}
  });
  await page.goto("/");await page.locator("[data-session=local-one]").click();
  await expect(page.locator("#terminal-mount")).toContainText("LAST FRAME");
  expect(await page.evaluate(()=>performance.getEntriesByName("agentpack:terminal:click").length)).toBeGreaterThan(0);
  expect(await page.evaluate(()=>performance.getEntriesByName("agentpack:terminal:firstframe").length)).toBeGreaterThan(0);
  sockets[0].close();await expect(page.locator(".terminal-stage")).toHaveClass(/stale/);
  await expect(page.locator("#terminal-mount")).toContainText("LAST FRAME");
  await expect.poll(()=>sockets.length).toBe(2);
  await expect(page.locator(".terminal-stage")).toHaveClass(/stale/);
  await expect(page.locator("#terminal-loading")).toBeVisible();
  await page.getByRole("button",{name:"Refresh status"}).click();
  await expect(page.locator(".terminal-stage")).toHaveClass(/stale/);
  sockets[1].send(Buffer.from("REPLAYED FRAME\r\n"));sockets[1].send(Buffer.from("SECOND FRAME\r\n"));
  await expect(page.locator("#terminal-mount")).toContainText("REPLAYED FRAME");await expect(page.locator("#terminal-mount")).toContainText("SECOND FRAME");
  await expect(page.locator("#terminal-mount .xterm-rows")).not.toContainText("LAST FRAME");
  await expect(page.locator(".terminal-stage")).toHaveClass(/stale/);
  sockets[1].send(JSON.stringify({type:"replay_complete"}));await expect(page.locator(".terminal-stage")).not.toHaveClass(/stale/);
  expect(await page.evaluate(()=>performance.getEntriesByName("agentpack:terminal:socketopen").length)).toBeGreaterThan(0);
  expect(await page.evaluate(()=>performance.getEntriesByName("agentpack:terminal:attached").length)).toBeGreaterThan(0);
  await expect(page.locator("#terminal-loading")).toBeHidden();
  expect(await page.evaluate(()=>performance.getEntriesByName("agentpack:terminal:replay_complete").length)).toBeGreaterThan(0);
});

test("offline package rows save drafts and require a fresh review before apply",async({page})=>{
  await mockAPI(page);let online=true,recentLoaded=0;const packageRequests=[];let drafts=[];let ready=false;
  const item={id:"package-1",module:"github.com/acme/tools",commit:"abcd1234",content_hash:"sha256-tree-v1:abc",kind:"plugin",name:"Tools",cache_key:"cache-key"};
  await page.route("**/api/nodes",route=>route.fulfill({json:[{id:"peer-1",name:"Build server",online}]}));
  await page.route("**/api/recent?node=peer-1",route=>{recentLoaded++;return route.fulfill({json:[{directory:"/srv/project",folder_id:"folder-fingerprint",harness:"claude",mode:"default",available:true}]});});
  await page.route("**/api/library/packages**",route=>route.fulfill({json:[item]}));
  await page.route("**/api/library/agentpacks**",route=>route.fulfill({json:[]}));
  await page.route("**/api/library/drafts**",async route=>{
    if(route.request().method()==="POST"){const input=route.request().postDataJSON();drafts=[{...input,id:"draft-1",created_at:"2026-10-28T12:00:00Z"}];return route.fulfill({json:drafts[0]});}
    return route.fulfill({json:drafts});
  });
  await page.route("**/api/library/drafts/draft-1/review",route=>route.fulfill({json:{draft:drafts[0],ready,pins_match:true,folder_match:true,reason:ready?"Ready to install the exact pin.":"Build server is offline.",diff:{add:[],update:[item.module],remove:[],mode:"default"}}}));
  await page.route("**/api/library/drafts/draft-1",route=>{drafts=[];return route.fulfill({json:{discarded:true}});});
  await page.route("**/api/packages**",async route=>{
    const url=new URL(route.request().url());if(route.request().method()==="GET")return route.fulfill({json:[]});
    packageRequests.push({body:route.request().postDataJSON(),node:url.searchParams.get("node")});return route.fulfill({json:{installed:true}});
  });
  await page.route("**/api/library/replication/peers",route=>route.fulfill({json:{peers:[],contacts:{}}}));
  await page.goto("/");await expect(page.getByRole("button",{name:"Gateway menu"})).toBeVisible();
  await page.getByRole("button",{name:"Refresh status"}).click();await expect.poll(()=>recentLoaded).toBeGreaterThan(0);online=false;await page.getByRole("button",{name:"Refresh status"}).click();
  await page.getByRole("button",{name:"Library",exact:true}).click();await page.locator("#library-list button").first().click();await page.locator("#install-item").click();
  const offline=page.locator("#install-destinations .dest").filter({hasText:"Offline"});await offline.locator("input").check();
  await expect(page.getByRole("button",{name:"Save 1 draft"})).toBeEnabled();await page.getByRole("button",{name:"Save 1 draft"}).click();
  await expect(page.locator("#install-drafts")).toContainText("Pending installs");await page.locator("#sheet-apply").click();
  expect(drafts[0]).toMatchObject({item:`${item.module}@${item.commit}`,node:"peer-1",directory:"/srv/project",folder_identity:"folder-fingerprint",expected_pins:{[item.module]:{commit:item.commit,cache_key:"cache-key",content_hash:item.content_hash}}});
  await page.getByRole("button",{name:"Review…"}).click();await expect(page.getByText("Build server is offline.")).toBeVisible();
  await page.getByRole("button",{name:"Keep draft"}).click();online=true;ready=true;await page.getByRole("button",{name:"Refresh status"}).click();
  await page.getByRole("button",{name:"Review…"}).click();await expect(page.getByText("Ready to install the exact pin.")).toBeVisible();
  await page.getByLabel("Restart affected agents after apply").uncheck();await page.getByRole("button",{name:"Install",exact:true}).click();
  await expect(page.locator("#install-drafts")).toBeEmpty();expect(packageRequests).toHaveLength(1);
  expect(packageRequests[0]).toMatchObject({node:"peer-1",body:{directory:"/srv/project",spec:`${item.module}@${item.commit}`,restart:false}});
});

test("replication requires opt-in and reports receipts per peer",async({page})=>{
  await mockAPI(page);let enrolled=false,synced=false;
  const item={id:"package-1",module:"github.com/acme/tools",commit:"abcd1234",kind:"plugin",name:"Tools"};
  await page.route("**/api/nodes",route=>route.fulfill({json:[{id:"peer-1",name:"Build server",online:true}]}));
  await page.route("**/api/settings",route=>route.fulfill({json:{running:true,platform:"linux",sharing_enabled:true,allowed_peers:[]}}));
  await page.route("**/api/library/replication/peers",async route=>{
    if(route.request().method()==="POST")enrolled=true;
    return route.fulfill({json:{peers:enrolled?[{node:"peer-1",receipt_count:synced?1:0,digests:synced?[item.id]:[]}]:[],contacts:{}}});
  });
  await page.route("**/api/library/replication/sync",route=>{synced=true;return route.fulfill({json:{synced:true}});});
  await page.route("**/api/library/packages**",route=>route.fulfill({json:[item]}));
  await page.route("**/api/library/agentpacks**",route=>route.fulfill({json:[]}));
  await page.goto("/");await page.getByRole("button",{name:"Gateway menu"}).click();await page.getByRole("button",{name:"Gateway settings"}).click();
  await expect(page.getByText("Both machines must enroll each other before registry objects replicate.")).toBeVisible();
  await page.getByRole("button",{name:"Enroll",exact:true}).click();await expect(page.getByText("The other machine must enroll this gateway too.")).toBeVisible();
  await page.getByRole("button",{name:"Sync now"}).click();await expect(page.getByText("Sync complete.")).toBeVisible();
  await page.getByRole("button",{name:"Library",exact:true}).click();await page.locator("#library-list button").first().click();
  await expect(page.getByText(/Verified on .*Build server/)).toBeVisible();
});

test("offline restore saves the exact agentpack revision as a draft",async({page})=>{
  await mockAPI(page);let online=true,recentLoaded=0;let draftInput;
  const pack={id:"pack-1",name:"Project setup",revisions:["rev-1"],heads:["rev-1"]};
  const revision={id:"rev-1",agentpack_id:pack.id,manifest:'name = "project"\n',lock:"",package_ids:[],project:"/work/project",created_at:"2026-10-20T10:00:00Z",label:"Stable",mode:"default"};
  let drafts=[];
  await page.route("**/api/nodes",route=>route.fulfill({json:[{id:"peer-1",name:"Build server",online}]}));
  await page.route("**/api/recent?node=peer-1",route=>{recentLoaded++;return route.fulfill({json:[{directory:"/srv/project",folder_id:"folder-fingerprint",harness:"claude",mode:"default",available:true}]});});
  await page.route("**/api/library/packages**",route=>route.fulfill({json:[]}));
  await page.route("**/api/library/agentpacks",route=>route.fulfill({json:[pack]}));
  await page.route("**/api/library/agentpacks/pack-1",route=>route.fulfill({json:{agentpack:pack,revisions:[revision]}}));
  await page.route("**/api/library/agentpacks/pack-1/revisions/rev-1/restore-preview**",route=>route.fulfill({json:{add:[],update:[],remove:[],mode:"default"}}));
  await page.route("**/api/library/drafts**",async route=>{
    if(route.request().method()==="POST"){draftInput=route.request().postDataJSON();drafts=[{...draftInput,id:"draft-restore"}];return route.fulfill({json:drafts[0]});}
    return route.fulfill({json:drafts});
  });
  await page.route("**/api/library/replication/peers",route=>route.fulfill({json:{peers:[],contacts:{}}}));
  await page.goto("/");await page.getByRole("button",{name:"Refresh status"}).click();await expect.poll(()=>recentLoaded).toBeGreaterThan(0);
  online=false;await page.getByRole("button",{name:"Refresh status"}).click();await page.getByRole("button",{name:"Library",exact:true}).click();
  await page.getByRole("button",{name:"Agentpacks"}).click();await page.locator("#library-list button").first().click();await page.getByRole("button",{name:/Restore/}).click();
  await page.getByLabel("/srv/project").check();await expect(page.getByRole("button",{name:"Save draft"})).toBeEnabled();await page.getByRole("button",{name:"Save draft"}).click();
  expect(draftInput).toMatchObject({item:pack.id,pack_id:pack.id,revision:revision.id,node:"peer-1",directory:"/srv/project",folder_identity:"folder-fingerprint",expected_pins:{}});
});

test("Sessions starts active and appearance settings persist across views and reload",async({page})=>{
  await mockAPI(page); await attachTerminal(page);
  await page.goto("/");
  await page.locator("[data-session=local-one]").click();
  await expect(page.getByRole("button",{name:"Sessions",exact:true})).toHaveAttribute("aria-current","page");
  await page.getByRole("button",{name:"Terminal appearance",exact:true}).click();
  await expect(page.locator("#sheet")).toBeVisible();
  for(const theme of ["dark","light"]){
    await page.getByLabel("Theme",{exact:true}).selectOption(theme);
    await expect(page.locator("html")).toHaveAttribute("data-theme",theme);
    await expect(page.locator(".terminal-stage")).toHaveCSS("background-color",theme==="dark"?"rgb(23, 27, 34)":"rgb(255, 255, 255)");
    await expect(page.locator(".xterm-screen")).toBeVisible();
    await page.screenshot({path:`test-results/appearance-${theme}.png`,animations:"disabled"});
  }
  await page.getByRole("button",{name:"Done",exact:true}).click();
  await page.getByRole("button",{name:"Library",exact:true}).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme","light");
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme","light");
  await page.getByRole("button",{name:"Gateway menu"}).click();
  await page.getByRole("button",{name:"Appearance settings",exact:true}).click();
  await page.getByLabel("Theme",{exact:true}).selectOption("system");
  await expect(page.locator("html")).not.toHaveAttribute("data-theme",/./);
  await page.keyboard.press("Escape");
  await expect(page.locator("#sheet")).not.toBeVisible();
});

test("Library deduplicates identical pins with different saved record IDs",async({page})=>{
  await mockAPI(page);
  const pkg={id:"first-record",module:"github.com/acme/tools",commit:"abcd1234",name:"Tools",kind:"plugin"};
  await page.route("**/api/library/packages**",route=>route.fulfill({json:[pkg,{...pkg,id:"another-record"},{...pkg,id:"older-version",commit:"older123"}]}));
  await page.goto("/");
  await page.getByRole("button",{name:"Library",exact:true}).click();
  await expect(page.locator("#packages-tab")).toHaveText("Packages 1");
  await expect(page.locator("#library-list button")).toHaveCount(1);
  await page.getByLabel("Package version").selectOption("1");
  await expect(page.locator("#library-detail .title")).toContainText("older123");
});


test("a stalled terminal replay retries and clears its loader",async({page})=>{
  await mockAPI(page);const sockets=[];
  await page.routeWebSocket("**/api/sessions/*/terminal",ws=>{
    sockets.push(ws);
    if(sockets.length>1){
      ws.send(JSON.stringify({type:"attached",attachment:42,state:{...session.state,shared_input:true,responder:42}}));
      ws.send(Buffer.from("RECOVERED\r\n"));
      ws.send(JSON.stringify({type:"replay_complete"}));
    }
  });
  await page.clock.install();
  await page.goto("/");await page.locator("[data-session=local-one]").click();
  await expect(page.locator("#terminal-loading")).toHaveText("Loading terminal…");
  await expect(page.locator("#terminal-panel")).toHaveAttribute("aria-busy","true");
  await page.screenshot({path:"test-results/terminal-loading-desktop.png"});
  await page.setViewportSize({width:390,height:844});
  await page.screenshot({path:"test-results/terminal-loading-phone.png"});
  await page.clock.runFor(31000);
  await expect.poll(()=>sockets.length).toBe(2);
  await expect(page.locator("#terminal-mount")).toContainText("RECOVERED");
  await expect(page.locator("#terminal-loading")).toBeHidden();
  await expect(page.locator("#terminal-panel")).toHaveAttribute("aria-busy","false");
});

test("sessions and library show loaders until their requests finish",async({page})=>{
  await mockAPI(page);
  let releaseSessions,releaseLibrary;
  const sessionsReady=new Promise(resolve=>releaseSessions=resolve);
  const libraryReady=new Promise(resolve=>releaseLibrary=resolve);
  await page.route("**/api/packages?*",route=>route.fulfill({json:[]}));
  await page.route("**/api/sessions",async route=>{await sessionsReady;await route.fulfill({json:[session]});});
  await page.route("**/api/library/packages",async route=>{await libraryReady;await route.fulfill({json:[]});});
  await page.goto("/");
  await expect(page.locator("#session-empty")).toHaveText("Loading sessions…");
  await expect(page.locator("#sessions")).toHaveAttribute("aria-busy","true");
  releaseSessions();
  await expect(page.locator("[data-session=local-one]")).toBeVisible();
  await expect(page.locator("#sessions")).toHaveAttribute("aria-busy","false");
  await page.getByRole("button",{name:"Library",exact:true}).click();
  await expect(page.locator("#library-list")).toContainText("Loading library…");
  await expect(page.locator("#library-list")).toHaveAttribute("aria-busy","true");
  await page.emulateMedia({colorScheme:"dark"});
  await page.screenshot({path:"test-results/library-loading-dark.png"});
  releaseLibrary();
  await expect(page.locator("#library-list")).not.toContainText("Loading library…");
  await expect(page.locator("#library-list")).toHaveAttribute("aria-busy","false");
});

test("terminal renders a large replay promptly and accepts live output afterwards",async({page})=>{
  await mockAPI(page);let socket;
  const replay=Buffer.from("history line\r\n".repeat(75000)+"\x1b[2J\x1b[HREPLAY READY\r\n");
  await page.routeWebSocket("**/api/sessions/*/terminal",ws=>{
    socket=ws;
    ws.send(JSON.stringify({type:"attached",attachment:42,state:{...session.state,shared_input:true,responder:42}}));
    for(let offset=0;offset<replay.length;offset+=32768)ws.send(replay.subarray(offset,offset+32768));
    ws.send(JSON.stringify({type:"replay_complete"}));
  });
  await page.goto("/");await page.locator("[data-session=local-one]").click();
  await expect(page.locator("#terminal-loading")).toBeHidden();
  await expect(page.locator("#terminal-mount")).toContainText("REPLAY READY");
  const timing=await page.evaluate(()=>{
    const mark=name=>performance.getEntriesByName(`agentpack:terminal:${name}`).at(-1).startTime;
    return {firstFrame:mark("firstframe")-mark("click"),replayComplete:mark("replay_complete")-mark("click")};
  });
  console.info(`Terminal ${replay.length} byte replay: first frame ${Math.round(timing.firstFrame)}ms, ready ${Math.round(timing.replayComplete)}ms`);
  expect(timing.firstFrame).toBeLessThan(1500);
  expect(timing.replayComplete).toBeLessThan(5000);
  socket.send(Buffer.from("LIVE AFTER REPLAY\r\n"));
  await expect(page.locator("#terminal-mount")).toContainText("LIVE AFTER REPLAY");
});
test("limits view shows each harness with partial failures and no raw CLI output",async({page})=>{
  await mockAPI(page);
  await page.goto("/");
  await page.locator('[data-view="limits"]').first().click();
  await expect(page.locator("#limits-view")).toBeVisible();
  await expect(page.getByRole("meter",{name:"Codex 5h remaining"})).toHaveAttribute("aria-valuenow","72");
  await expect(page.locator("#limits-body")).toContainText("grok billing: authentication required");
  await expect(page.locator("#limits-body")).toContainText("codex");
});
