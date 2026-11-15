import { expect, test } from "@playwright/test"
import { mockAPI } from "./helpers"

test("a joining gateway never offers a second owner claim and waits for inherited accounts", async ({page}) => {
  await mockAPI(page,{authenticated:false})
  let joining=true
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status",route=>route.fulfill({json:{claimed:true,joining,authenticated:false,email:"",legacy:false}}))
  await page.goto("/login")
  await expect(page.getByRole("heading",{name:"Joining your mesh"})).toBeVisible()
  await expect(page.getByRole("textbox",{name:"Email",exact:true})).toHaveCount(0)
  await expect(page.getByRole("button",{name:"Claim gateway",exact:true})).toHaveCount(0)
  await expect(page.getByText("Synchronizing accounts…")).toBeVisible()
  await page.screenshot({path:"/tmp/agentpack-mesh-join-desktop.png"})
  await page.setViewportSize({width:390,height:844})
  await page.screenshot({path:"/tmp/agentpack-mesh-join-phone.png"})
  joining=false
  await expect(page.getByRole("heading",{name:"Sign in to Agentpack"})).toBeVisible()
  await expect(page.getByRole("textbox",{name:"Email",exact:true})).toBeVisible()
  await expect(page.getByRole("button",{name:"Claim gateway",exact:true})).toHaveCount(0)
})
