import { test, expect } from "@playwright/test";

const pkg={id:"pkg-tools",module:"github.com/acme/tools",commit:"abcdef0123456789",content_hash:"sha256-tree-v1:review",cache_key:"cache-tools",kind:"plugin",name:"Tools",url:"https://github.com/acme/tools",project:"/work/app",owner:"acme",repo:"tools"};
const revision={id:"rev-new",agentpack_id:"pack-app",label:"Current setup",project:"/work/app",created_at:"2026-10-28T10:00:00Z",mode:"default",manifest:'[dependencies]\n"github.com/acme/tools" = "abcdef0123456789"\n',package_ids:[pkg.id]};
const pack={id:"pack-app",name:"Application setup",heads:[revision.id,"rev-old"],revisions:[revision.id,"rev-old"]};

test("capture Library, Machines, install, restore, draft, and settings review states",async({page},testInfo)=>{
  await page.route("**/api/**",async route=>{
    const req=route.request(),url=new URL(req.url()),path=url.pathname,body=req.method()==="GET"?undefined:req.postDataJSON();let data={};
    if(path==="/api/status")data={setup_required:false,authenticated:true};
    else if(path==="/api/nodes")data=[{id:"peer-1",name:"Build server",online:true}];
    else if(path==="/api/sessions")data=[{id:"session-1",directory:"/work/app",harness:"claude",mode:"default",state:{running:true,shared_input:true,responder:1,rows:24,columns:80}}];
    else if(path==="/api/recent")data=[{directory:"/work/app",folder_id:"folder-app",harness:"claude",mode:"default",available:true}];
    else if(path==="/api/packages")data=req.method()==="GET"?[{module:pkg.module,commit:pkg.commit}]:{installed:true};
    else if(path==="/api/library/packages")data=[pkg];
    else if(path==="/api/library/agentpacks")data=[pack];
    else if(path==="/api/library/agentpacks/pack-app")data={agentpack:pack,revisions:[revision,{...revision,id:"rev-old",label:"Previous setup",created_at:"2026-10-20T10:00:00Z",package_ids:[],manifest:'[dependencies]\n'}]};
    else if(path==="/api/library/agentpacks/pack-app/revisions/rev-new/restore-preview")data={add:[],update:[],remove:[],mode:"default"};
    else if(path==="/api/library/drafts")data=[{id:"draft-1",item:`${pkg.module}@${pkg.commit}`,node:"",directory:"/work/app",reason:"Saved while Build server was offline",folder_identity:"folder-app",expected_pins:{[pkg.module]:{commit:pkg.commit,cache_key:pkg.cache_key,content_hash:pkg.content_hash}}}];
    else if(path==="/api/library/drafts/draft-1/review")data={ready:true,reason:"Ready to install the exact pin.",diff:{add:[],update:[pkg.module],remove:[],mode:"default"}};
    else if(path==="/api/library/replication/peers")data={peers:[{node:"peer-1",receipt_count:1,digests:[pkg.id,revision.id]}],contacts:{"peer-1":{at:"2026-10-28T10:00:00Z"}}};
    else if(path==="/api/settings")data={running:true,platform:"linux",sharing_enabled:true,allowed_peers:[]};
    await route.fulfill({json:data});
  });
  await page.routeWebSocket("**/api/sessions/*/terminal",ws=>{ws.send(JSON.stringify({type:"attached",attachment:1,state:{running:true,shared_input:true,responder:1,rows:24,columns:80}}));ws.send(Buffer.from("READY\r\n"));ws.send(JSON.stringify({type:"replay_complete"}));});
  await page.goto("/");
  const save=async(name)=>{await page.waitForTimeout(250);await page.screenshot({path:`test-results/review-${name}-${testInfo.project.name}.png`});};
  for(const theme of ["light","dark"]){
    await page.emulateMedia({colorScheme:theme});
    await page.getByRole("button",{name:"Sessions",exact:true}).click();
    await expect(page.locator("#workspace")).toBeVisible();
    await page.locator("[data-session=session-1]").first().click();await expect(page.locator(".xterm-screen")).toBeVisible();
    await save(`sessions-${theme}`);
    await page.locator("[data-session=session-1]").first().click({button:"right"});await save(`session-menu-${theme}`);
    await page.getByRole("menuitem",{name:"Rename…"}).click();await save(`session-rename-${theme}`);await page.getByRole("button",{name:"Cancel",exact:true}).click();
    await page.getByRole("button",{name:"Library",exact:true}).click();
    await page.locator("#library-list button").first().click();await save(`library-package-${theme}`);
    await page.getByRole("button",{name:"Install…"}).click();await expect(page.locator("#install-destinations")).toBeVisible();await save(`install-destinations-${theme}`);await page.getByRole("button",{name:"Cancel"}).click();
    await page.getByRole("button",{name:"Agentpacks"}).click();await page.locator("#library-list button").first().click();await save(`library-history-${theme}`);
    await page.getByRole("button",{name:/Restore/}).click();await expect(page.getByText("No package changes")).toBeVisible();await save(`restore-preview-${theme}`);await page.getByRole("button",{name:"Cancel"}).click();
    await page.getByRole("button",{name:"Review…"}).click();await expect(page.getByText("Ready to install the exact pin.")).toBeVisible();await save(`pending-review-${theme}`);await page.getByRole("button",{name:"Cancel"}).click();
    await page.getByRole("button",{name:"Machines",exact:true}).click();await save(`machines-${theme}`);
    await page.getByRole("button",{name:"Gateway menu"}).click();await page.getByRole("button",{name:"Gateway settings"}).click();await expect(page.locator("#replication-peers")).toContainText("Build server");await save(`replication-${theme}`);
    if(theme==="light"){
      await page.setViewportSize({width:390,height:844});await page.getByRole("button",{name:"Library",exact:true}).click();if(await page.locator("#lib-back").isVisible())await page.locator("#lib-back").click();await page.locator("#packages-tab").click();await page.locator("#library-list button").first().click();await save("mobile-library-light");
      await page.getByRole("button",{name:"Install…"}).click();await save("mobile-install-light");await page.getByRole("button",{name:"Cancel"}).click();await page.setViewportSize({width:1440,height:1000});
    }
    await page.getByRole("button",{name:"Library",exact:true}).click();
  }
});
