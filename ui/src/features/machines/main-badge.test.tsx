import { createElement, type ReactNode } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { expect, it, vi } from "vitest"
import { MachineRow } from "./machine-list"

vi.mock("@tanstack/react-router", async (original) => ({
  ...await original<typeof import("@tanstack/react-router")>(),
  Link: ({children}: {children?: ReactNode}) => createElement("a",null,children),
  useMatchRoute: () => () => false,
}))

it("marks exactly the main machine, including while it is offline", () => {
  const main=renderToStaticMarkup(<MachineRow machine={{id:"main",name:"Archive computer",online:false,main:true}} />)
  const peer=renderToStaticMarkup(<MachineRow machine={{id:"peer",name:"Other computer",online:true,main:false}} />)
  const root=document.createElement("div")
  root.innerHTML=main
  expect(root.querySelector('[data-slot="badge"]')?.textContent).toBe("Main")
  expect(root.textContent).toContain("Offline")
  root.innerHTML=peer
  expect(root.querySelector('[data-slot="badge"]')).toBeNull()
})
