import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { QueryClient } from "@tanstack/react-query"
import {
  dismissUpdateJob,
  getUpdateJob,
  isUpdateJobActive,
  resetUpdateJobsForTest,
  startUpdateJob,
  syncUpdateJob,
  UPDATE_DEADLINE_MS,
  updateJobLabel,
} from "./update-jobs"

describe("update-jobs", () => {
  beforeEach(() => {
    vi.useFakeTimers()
    resetUpdateJobsForTest()
  })
  afterEach(() => {
    resetUpdateJobsForTest()
    vi.useRealTimers()
  })

  it("tracks actual phases and verifies only the exact requested version", () => {
    const queryClient = new QueryClient()
    startUpdateJob(queryClient, { machineId: "n2", machineName: "birch", targetVersion: "0.15.0" })
    expect(getUpdateJob("n2")?.phase).toBe("requested")
    expect(isUpdateJobActive(getUpdateJob("n2"))).toBe(true)

    syncUpdateJob("n2", { status: { state: "running", error: "" }, version: "0.14.0" })
    expect(updateJobLabel(getUpdateJob("n2")!)).toBe("Updating…")

    syncUpdateJob("n2", { status: { state: "running", error: "", phase: "downloading" } })
    expect(updateJobLabel(getUpdateJob("n2")!)).toBe("Downloading…")

    syncUpdateJob("n2", { status: { state: "idle", error: "", phase: "restarting" }, version: "0.14.1" })
    expect(updateJobLabel(getUpdateJob("n2")!)).toBe("Restarting…")

    syncUpdateJob("n2", { pollError: true, version: "0.14.0" })
    expect(updateJobLabel(getUpdateJob("n2")!)).toBe("Reconnecting…")

    syncUpdateJob("n2", { status: { state: "idle", error: "" }, version: "v0.15.0+abc" })
    expect(getUpdateJob("n2")?.phase).toBe("verified")
    expect(updateJobLabel(getUpdateJob("n2")!)).toBe("Verified v0.15.0+abc")
    expect(isUpdateJobActive(getUpdateJob("n2"))).toBe(false)

    dismissUpdateJob("n2")
    expect(getUpdateJob("n2")).toBeUndefined()
  })

  it("marks timed_out after the deadline and supports failed status", () => {
    const queryClient = new QueryClient()
    vi.spyOn(queryClient, "fetchQuery").mockRejectedValue(new Error("offline"))
    startUpdateJob(queryClient, { machineId: "n2", machineName: "birch", targetVersion: "0.15.0" })
    vi.advanceTimersByTime(UPDATE_DEADLINE_MS)
    expect(getUpdateJob("n2")?.phase).toBe("timed_out")
    expect(updateJobLabel(getUpdateJob("n2")!)).toBe("Timed out")

    startUpdateJob(queryClient, { machineId: "n3", machineName: "cedar", targetVersion: "0.15.0" })
    syncUpdateJob("n3", { status: { state: "failed", error: "checksum mismatch" } })
    expect(getUpdateJob("n3")).toMatchObject({ phase: "failed", error: "checksum mismatch" })
  })
})
