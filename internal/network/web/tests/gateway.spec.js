import { test, expect } from "@playwright/test";
const session = { id: "local-one", directory: "/work/my-project", harness: "claude", mode: "default", state: { running: true, owner: 0, epoch: 1, rows: 24, columns: 80, exit_code: 0 } };
async function mockAPI(page, { setup = false, authenticated = true } = {}) {
  const requests = [];
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const body = route.request().postDataJSON();
    requests.push({ path: url.pathname, body });
    let data;
    if (url.pathname === "/api/status") data = { setup_required: setup, authenticated };
    else if (url.pathname === "/api/setup") {
      expect(body.token).toBe("private-token");
      expect(body.password).toBe("a-long-test-password");
      setup = false;
      authenticated = true;
      data = {};
    } else if (url.pathname === "/api/login") {
      authenticated = true;
      data = {};
    } else if (url.pathname === "/api/logout") {
      authenticated = false;
      data = {};
    } else if (url.pathname === "/api/sessions") data = route.request().method() === "POST" ? { ...session, ...body } : [session];
    else if (url.pathname === "/api/recent") data = [{ directory: "/work/previous", harness: "codex", mode: "default" }];
    else if (url.pathname === "/api/packages") data = route.request().method() === "POST" ? {} : [{ module: "github.com/acme/tools", commit: "abcd1234" }];
    else data = {};
    await route.fulfill({ json: data });
  });
  return requests;
}
test("private setup, recent folders, package install and responsive layout", async ({ page }) => {
  const requests = await mockAPI(page, { setup: true, authenticated: false });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Set up your gateway" })).toBeVisible();
  await page.getByLabel("Setup token").fill("private-token");
  await page.getByLabel("Root account password").fill("a-long-test-password");
  await page.getByRole("button", { name: "Create root account" }).click();
  await expect(page.getByRole("heading", { name: "Sessions", exact: true })).toBeVisible();
  await expect(page.getByText("/work/my-project", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Recent folders" }).click();
  await page.getByRole("button", { name: "Start fresh" }).click();
  await expect(page.locator("#start-directory")).toHaveValue("/work/previous");
  await expect(page.locator("#start-harness")).toHaveValue("codex");
  await page.getByRole("button", { name: "Close new session" }).click();
  await page.getByRole("button", { name: "Packages", exact: true }).click();
  await page.locator("#package-directory").fill("/work/my-project");
  await page.getByRole("button", { name: "Load packages" }).click();
  await expect(page.getByText("github.com/acme/tools")).toBeVisible();
  await page.locator("#package-spec").fill("acme/skill");
  await page.getByRole("button", { name: "Install package" }).click();
  await expect(page.getByText("Package installed. Affected agents have been restarted.")).toBeVisible();
  expect(requests.some((request) => request.path === "/api/packages" && request.body?.spec === "acme/skill")).toBeTruthy();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
  await page.screenshot({ path: `test-results/gateway-${test.info().project.name}.png`, fullPage: true });
});
test("terminal renders bytes, arbitrates input and detaches without stopping", async ({ page }) => {
  const requests = await mockAPI(page);
  let websocket;
  const received = [];
  await page.routeWebSocket("**/api/sessions/*/terminal", (ws) => {
    websocket = ws;
    ws.send(JSON.stringify({ type: "attached", attachment: 42, state: session.state }));
    ws.send(Buffer.from("\x1B[32mREADY \u96EA\x1B[0m\r\n"));
    ws.onMessage((message) => {
      received.push(message);
      if (typeof message === "string") {
        const control = JSON.parse(message);
        if (control.action === "take") ws.send(JSON.stringify({ type: "state", state: { ...session.state, owner: 42, epoch: 2 } }));
        if (control.action === "release") ws.send(JSON.stringify({ type: "state", state: { ...session.state, owner: 0, epoch: 3 } }));
      }
    });
  });
  await page.goto("/");
  await page.getByRole("button", { name: "Open terminal" }).click();
  await expect(page.locator("#terminal-status")).toHaveText("Observing \xB7 control available");
  await expect(page.locator(".xterm-screen")).toBeVisible();
  await page.getByRole("button", { name: "Take control" }).click();
  await expect(page.locator("#terminal-status")).toHaveText("You have control");
  await page.getByRole("button", { name: "Ctrl C", exact: true }).click();
  await expect.poll(() => received.some((data) => typeof data !== "string" && Buffer.from(data).equals(Buffer.from([3])))).toBeTruthy();
  await page.getByRole("button", { name: "Release control" }).click();
  await expect(page.locator("#terminal-status")).toHaveText("Observing \xB7 control available");
  await expect(page.getByRole("button", { name: "Ctrl C", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Close terminal attachment" }).click();
  await expect(page.locator("#terminal-panel")).toBeHidden();
  expect(requests.some((request) => request.path.endsWith("/stop"))).toBeFalsy();
});
test("backend strings render as text and failures are visible", async ({ page }) => {
  await mockAPI(page);
  await page.route("**/api/sessions", (route) => route.fulfill({ json: [{ ...session, directory: "<img src=x onerror=alert(1)>" }] }));
  await page.goto("/");
  await expect(page.getByText("<img src=x onerror=alert(1)>")).toBeVisible();
  expect(await page.locator("img").count()).toBe(0);
  await page.route("**/api/packages?**", (route) => route.fulfill({ status: 500, json: { error: "Project is not available" } }));
  await page.getByRole("button", { name: "Packages", exact: true }).click();
  await page.locator("#package-directory").fill("/missing");
  await page.getByRole("button", { name: "Load packages" }).click();
  await expect(page.getByText("Project is not available")).toBeVisible();
});
test('node selection scopes requests and clears the previous folder', async ({page}) => {
  await mockAPI(page);
  await page.route('**/api/nodes', route => route.fulfill({json:[{id:'n-remote',name:'Oracle VM',online:true}]}));
  const remoteRequests=[];
  await page.route('**/api/sessions?node=*', route => {remoteRequests.push(route.request().url());return route.fulfill({json:[{...session,directory:'/remote/project'}]});});
  await page.goto('/');
  await expect(page.locator('#node-select option')).toHaveCount(2);
  await page.locator('#node-select').selectOption('n-remote');
  await expect(page.getByText('/remote/project',{exact:true})).toBeVisible();
  expect(remoteRequests.every(value=>new URL(value).searchParams.get('node')==='n-remote')).toBeTruthy();
  await page.getByRole('button',{name:'Packages',exact:true}).click();
  await page.locator('#package-directory').fill('/remote/project');
  await page.locator('#node-select').selectOption('');
  await expect(page.locator('#package-directory')).toHaveValue('');
});
test('login cannot redirect to an external origin', async ({page}) => {
  await mockAPI(page,{authenticated:false});
  await page.goto('/?return_to=%2F%2Fevil.example%2F');
  await page.getByLabel('Root account password').fill('a-long-test-password');
  await page.getByRole('button',{name:'Sign in',exact:true}).click();
  await expect(page.getByRole('heading',{name:'Sessions',exact:true})).toBeVisible();
  expect(new URL(page.url()).hostname).toBe('127.0.0.1');
});
test('native stdio sessions expose metadata without terminal controls', async ({page}) => {
  await mockAPI(page);
  await page.route('**/api/sessions', route=>route.fulfill({json:[{...session,state:{...session.state,interactive:false}}]}));
  await page.goto('/');
  await expect(page.getByText('Native stdio · browser input unavailable')).toBeVisible();
  await expect(page.getByRole('button',{name:'Open terminal'})).toBeDisabled();
  await expect(page.getByRole('button',{name:'Restart',exact:true})).toBeDisabled();
  await expect(page.getByRole('button',{name:'Stop',exact:true})).toBeEnabled();
});
test('restart reconnects to a clean terminal as an observer',async({page})=>{
  await mockAPI(page);
  const sockets=[];
  await page.routeWebSocket('**/api/sessions/*/terminal',ws=>{sockets.push(ws);ws.send(JSON.stringify({type:'attached',attachment:42,state:session.state}));ws.send(Buffer.from(sockets.length===1?'OLD PROCESS\r\n':'NEW PROCESS\r\n'));});
  await page.goto('/');
  await page.getByRole('button',{name:'Open terminal'}).click();
  await expect(page.locator('#terminal-status')).toHaveText('Observing · control available');
  sockets[0].send(JSON.stringify({type:'ended',state:{...session.state,running:false,restarting:true}}));
  sockets[0].close();
  await expect.poll(()=>sockets.length).toBe(2);
  await expect(page.locator('#terminal-notice')).toContainText('new process');
  await expect(page.locator('#terminal-status')).toHaveText('Observing · control available');
  await page.screenshot({path:`test-results/terminal-${test.info().project.name}.png`,fullPage:true});
});
test('settings controls gateway sharing separately from sessions',async({page})=>{
  const requests=await mockAPI(page);
  let enabled=true;
  await page.route('**/api/settings',route=>route.fulfill({json:{running:true,platform:'linux',sharing_enabled:enabled,allowed_peers:['tag:server']}}));
  await page.route('**/api/settings/sharing',route=>{enabled=route.request().postDataJSON().enabled;return route.fulfill({json:{sharing_enabled:enabled}});});
  await page.goto('/');
  await page.getByRole('button',{name:'Settings',exact:true}).click();
  await expect(page.getByText('tag:server',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'Stop sharing',exact:true}).click();
  await expect(page.getByRole('button',{name:'Enable sharing',exact:true})).toBeEnabled();
  expect(requests.some(request=>request.path.endsWith('/stop'))).toBeFalsy();
  page.on('dialog',dialog=>dialog.accept());
  await page.getByRole('button',{name:'Stop all agents on gateway'}).click();
  await expect(page.locator('#settings-status')).toHaveText('Agents on the gateway machine have been stopped.');
  expect(requests.some(request=>request.path==='/api/node/stop-agents')).toBeTruthy();
});
test('forget removes recent history without stopping a session',async({page})=>{
  const requests=await mockAPI(page);
  let recent=[{directory:'/work/previous',harness:'codex',mode:'default'}];
  await page.route('**/api/recent',route=>{if(route.request().method()==='DELETE'){expect(route.request().postDataJSON()).toEqual(recent[0]);recent=[];return route.fulfill({json:{forgotten:true}});}return route.fulfill({json:recent});});
  await page.goto('/');
  await page.getByRole('button',{name:'Recent folders'}).click();
  await page.getByRole('button',{name:'Forget',exact:true}).click();
  await expect(page.getByText('Folders will appear here after you launch an agent.')).toBeVisible();
  expect(requests.some(request=>request.path.endsWith('/stop'))).toBeFalsy();
});
test('restart waits through shutdown grace and replays a replacement that already exited', async ({page}) => {
  await mockAPI(page);
  let phase='initial';
  let polls=0;
  const failedState={...session.state,running:false,restarting:false,exit_code:1};
  await page.route('**/api/sessions', route => {
    if (phase==='restarting') polls++;
    const current=phase==='failed'?failedState:phase==='restarting'?{...session.state,running:false,restarting:true}:session.state;
    return route.fulfill({json:[{...session,state:current}]});
  });
  const sockets=[];
  await page.routeWebSocket('**/api/sessions/*/terminal', ws => {
    sockets.push(ws);
    const current=sockets.length===1?session.state:failedState;
    ws.send(JSON.stringify({type:'attached',attachment:42,state:current}));
    if(sockets.length>1) {
      ws.send(Buffer.from('REPLACEMENT FAILED: invalid configuration\r\n'));
      ws.send(JSON.stringify({type:'ended',state:failedState}));
    }
  });
  await page.goto('/');
  await page.getByRole('button',{name:'Open terminal'}).click();
  await expect(page.locator('#terminal-status')).toHaveText('Observing · control available');
  phase='restarting';
  sockets[0].send(JSON.stringify({type:'ended',state:{...session.state,running:false,restarting:true}}));
  sockets[0].close();
  await expect.poll(()=>polls).toBeGreaterThan(0);
  // Advance wall time without delaying the test or skipping its network callbacks.
  await page.clock.setFixedTime(new Date(Date.now()+76000));
  await expect.poll(()=>polls).toBeGreaterThan(1);
  phase='failed';
  await expect.poll(()=>sockets.length).toBe(2);
  await expect(page.locator('#terminal')).toContainText('REPLACEMENT FAILED: invalid configuration');
  await expect(page.locator('#terminal-status')).toHaveText('Agent exited (1)');
  sockets[1].close();
  await expect(page.locator('#terminal-status')).toHaveText('Agent exited (1)');
  await expect(page.getByRole('button',{name:'Take control',exact:true})).toBeDisabled();
});
test('Antigravity presence does not offer remote desktop launches', async ({page}) => {
  await mockAPI(page);
  await page.route('**/api/sessions',route=>route.fulfill({json:[{...session,harness:'agy',state:{...session.state,interactive:false}}]}));
  await page.route('**/api/recent',route=>route.fulfill({json:[{directory:'/work/desktop',harness:'agy',mode:'default'}]}));
  await page.goto('/');
  await expect(page.getByText('Desktop launcher presence · start and interact locally. Detached desktop windows are not tracked.')).toBeVisible();
  await expect(page.getByRole('button',{name:'Open terminal'})).toBeDisabled();
  await expect(page.locator('#start-harness option[value="agy"]')).toHaveCount(0);
  await page.getByRole('button',{name:'Recent folders'}).click();
  await expect(page.getByText('Desktop launches must start locally.')).toBeVisible();
  await expect(page.getByRole('button',{name:'Start fresh',exact:true})).toBeDisabled();
});

test('restart retries retain intent after ambiguous errors and renew after definitive outcomes', async ({page}) => {
  await mockAPI(page);
  const ids=[];
  await page.route('**/api/sessions/*/restart', async route => {
    const body=route.request().postDataJSON();
    expect(Object.keys(body)).toEqual(['operation_id']);
    expect(body.operation_id).toMatch(/^[0-9a-f-]{36}$/);
    ids.push(body.operation_id);
    if(ids.length===1) return route.abort('failed');
    if(ids.length===2) return route.fulfill({status:502,json:{error:'Worker response unavailable'}});
    if(ids.length===4) return route.fulfill({status:409,json:{error:'Restart rejected'}});
    return route.fulfill({json:{restarted:true}});
  });
  page.on('dialog',dialog=>dialog.accept());
  await page.goto('/');
  const restart=page.getByRole('button',{name:'Restart',exact:true});
  await restart.click();
  await expect(page.locator('#notice')).toContainText('Failed to fetch');
  await restart.click();
  await expect(page.locator('#notice')).toHaveText('Worker response unavailable');
  await restart.click();
  await expect(page.locator('#notice')).toHaveText('Agent restart requested.');
  expect(ids[1]).toBe(ids[0]);
  expect(ids[2]).toBe(ids[0]);
  await restart.click();
  await expect(page.locator('#notice')).toHaveText('Restart rejected');
  expect(ids[3]).not.toBe(ids[2]);
  await restart.click();
  await expect(page.locator('#notice')).toHaveText('Agent restart requested.');
  expect(ids[4]).not.toBe(ids[3]);
});
test('recent folders show last use and disable missing or replaced directories', async ({page}) => {
  await mockAPI(page);
  const lastUsed='2026-10-26T10:30:00Z';
  await page.route('**/api/recent', route=>route.fulfill({json:[
    {directory:'/work/available',harness:'codex',mode:'default',available:true,last_used:lastUsed},
    {directory:'/work/missing',harness:'codex',mode:'default',available:false,last_used:lastUsed,unavailable_reason:'Folder is missing or inaccessible'},
    {directory:'/work/replaced',harness:'claude',mode:'default',available:false,unavailable_reason:'Folder changed since its last local launch'}
  ]}));
  await page.goto('/');
  await page.getByRole('button',{name:'Recent folders'}).click();
  const starts=page.getByRole('button',{name:'Start fresh',exact:true});
  await expect(starts.nth(0)).toBeEnabled();
  await expect(starts.nth(1)).toBeDisabled();
  await expect(starts.nth(2)).toBeDisabled();
  await expect(page.getByText('Unavailable · Folder is missing or inaccessible')).toBeVisible();
  await expect(page.getByText('Unavailable · Folder changed since its last local launch')).toBeVisible();
  await expect(page.locator('#recent time').first()).toHaveAttribute('datetime',new Date(lastUsed).toISOString());
  await expect(page.locator('#recent time').first()).toContainText('Last used');
  await expect(page.locator('#recent-folders option')).toHaveCount(1);
  await expect(page.locator('#recent-folders option')).toHaveAttribute('value','/work/available');
});
test('sidebar hides and returns with the toolbar button and Cmd/Ctrl+Backslash', async ({page}) => {
  test.skip(test.info().project.name === 'phone', 'phone layout keeps navigation visible');
  await mockAPI(page);
  await page.goto('/');
  await page.getByRole('button',{name:'Hide sidebar'}).click();
  await expect(page.locator('.sidebar')).toBeHidden();
  await page.keyboard.press('Control+Backslash');
  await expect(page.locator('.sidebar')).toBeVisible();
  await expect(page.getByRole('button',{name:'Hide sidebar'})).toBeVisible();
});
test('sessions list shows every machine without changing the selector', async ({page}) => {
  await mockAPI(page);
  await page.route('**/api/nodes', route => route.fulfill({json:[{id:'n-main',name:'main',online:true}]}));
  await page.route('**/api/sessions?node=n-main', route => route.fulfill({json:[{...session,id:'remote-one',directory:'/home/me/immigration'}]}));
  const sockets=[];
  await page.routeWebSocket(/\/api\/sessions\/[^/]+\/terminal/, ws => {sockets.push(ws.url());ws.send(JSON.stringify({type:'attached',attachment:42,state:session.state}));});
  await page.goto('/');
  await expect(page.getByText('/home/me/immigration',{exact:true})).toBeVisible();
  await expect(page.getByText('/work/my-project',{exact:true})).toBeVisible();
  await expect(page.locator('.group-label')).toHaveText(['Gateway machine','main']);
  await expect(page.locator('#node-select')).toHaveValue('');
  await expect(page.locator('#session-count')).toHaveText('2');
  await page.getByRole('button',{name:'Open terminal'}).nth(1).click();
  await expect.poll(()=>sockets.length).toBe(1);
  expect(new URL(sockets[0]).searchParams.get('node')).toBe('n-main');
  await page.screenshot({path:`test-results/all-machines-${test.info().project.name}.png`,fullPage:true});
});
test('dropped terminal connections reconnect automatically while the agent runs', async ({page}) => {
  await mockAPI(page);
  const sockets=[];
  await page.routeWebSocket(/\/api\/sessions\/[^/]+\/terminal/, ws => {sockets.push(ws);ws.send(JSON.stringify({type:'attached',attachment:42,state:session.state}));ws.send(Buffer.from(`ATTACH ${sockets.length}\r\n`));});
  await page.goto('/');
  await page.getByRole('button',{name:'Open terminal'}).click();
  await expect(page.locator('#terminal-status')).toHaveText('Observing · control available');
  sockets[0].close();
  await expect(page.locator('#terminal-status')).toContainText('reconnecting');
  await expect.poll(()=>sockets.length).toBe(2);
  await expect(page.locator('#terminal-status')).toHaveText('Observing · control available');
  await expect(page.locator('#terminal')).toContainText('ATTACH 2');
  await page.getByRole('button',{name:'Close terminal attachment'}).click();
  await page.waitForTimeout(1500);
  expect(sockets.length).toBe(2);
});
