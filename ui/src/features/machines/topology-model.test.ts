import { describe, expect, it } from "vitest"
import type { TopologyPeer } from "@/lib/api-types"
import { edgeOf, layoutDiagram, machinePeers, meshState, ROW_H } from "./topology-model"

function peer(name: string, state: string, connection = "", relay = ""): TopologyPeer {
  return { id: name, name, os: "linux", state, connection, relay, sessions: 0 }
}

describe("edgeOf", () => {
  it("labels relay with its region and dashes it, and labels direct solid", () => {
    expect(edgeOf(peer("a", "reachable", "relay", "fra"))).toEqual({ label: "relay fra", dashed: true })
    expect(edgeOf(peer("b", "reachable", "direct"))).toEqual({ label: "direct", dashed: false })
  })

  it("does not call a path with unknown connection direct", () => {
    expect(edgeOf(peer("c", "reachable"))).toEqual({ label: "path unknown", dashed: false })
  })
})

describe("machinePeers", () => {
  it("lists exactly the sidebar machines with their online state, enriched from the tailnet", () => {
    const peers = [{ ...peer("mac", "reachable", "direct"), os: "macOS" }, peer("tv", "offline"), peer("phone", "no-gateway")]
    const result = machinePeers(peers, [{ id: "mac", name: "mac", online: false }, { id: "gone", name: "gone", online: false }])
    expect(result.map((p) => [p.name, p.os, p.state])).toEqual([["mac", "macOS", "offline"], ["gone", "", "offline"]])
  })
})

describe("meshState", () => {
  const now = Date.parse("2026-11-27T12:00:00Z")
  const node = (online: boolean) => ({ id: "mac", name: "mac", online })
  it("says what a machine needs to sync, in order of what to do first", () => {
    expect(meshState(node(true), undefined, undefined, undefined, true, now)).toEqual({ text: "Not in mesh", glyph: "stopped" })
    expect(meshState(node(true), { node: "mac", mesh: false }, undefined, undefined, true, now).text).toBe("Waiting for your approval")
    expect(meshState(node(true), { node: "mac", mesh: true }, { at: "2026-11-27T11:59:50Z", needs_trust: true }, undefined, true, now).text).toBe("Add this machine on mac to sync")
    expect(meshState(node(false), { node: "mac", mesh: true }, { at: "2026-11-27T02:00:00Z" }, undefined, true, now).text).toBe("Offline · last seen 10 hours ago")
    expect(meshState(node(true), { node: "mac", mesh: true }, { at: "2026-11-27T11:59:50Z", last_sync: "2026-11-27T11:59:00Z" }, undefined, true, now)).toMatchObject({ text: "Synced 1 minute ago", glyph: "running" })
    expect(meshState(node(true), { node: "mac", mesh: true }, { at: "2026-11-27T11:50:00Z", last_sync: "2026-11-27T11:50:00Z" }, undefined, true, now).text).toBe("Last synced 10 minutes ago")
  })
})

describe("layoutDiagram", () => {
  it("places rows 44 px apart with one edge per reachable peer", () => {
    const layout = layoutDiagram([peer("mac", "reachable", "direct"), peer("build", "offline"), peer("fed", "reachable", "relay", "fra")])
    expect(layout.nodes.map((node) => node.y)).toEqual([2, 2 + ROW_H, 2 + 2 * ROW_H])
    expect(layout.edges.map((edge) => edge.peer.name)).toEqual(["mac", "fed"])
    expect(layout.edges[1]).toMatchObject({ label: "relay fra", dashed: true })
  })
})
