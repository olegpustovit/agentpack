import { useSyncExternalStore } from "react"
import type { QueryClient } from "@tanstack/react-query"
import { hud } from "@/lib/hud"
import { onSignOut } from "@/lib/persist"
import { machineInfoQuery, machineUpdateQuery, type MachineUpdateStatus } from "./queries"

export type UpdatePhase = "requested" | "updating" | "downloading" | "restarting" | "reconnecting" | "verified" | "failed" | "timed_out"

export type UpdateJob = {
  machineId: string
  machineName: string
  targetVersion: string
  phase: UpdatePhase
  error?: string
  startedAt: number
}

export const UPDATE_DEADLINE_MS = 5 * 60 * 1000
const POLL_MS = 2000
const STORAGE_KEY = "agentpack:machine-updates"

const jobs = new Map<string, UpdateJob>()
const timers = new Map<string, { poll: ReturnType<typeof setInterval>; deadline: ReturnType<typeof setTimeout> }>()
const listeners = new Set<() => void>()

function emit() {
  try {
    if (jobs.size) sessionStorage.setItem(STORAGE_KEY, JSON.stringify([...jobs.values()]))
    else sessionStorage.removeItem(STORAGE_KEY)
  } catch {
    // The current tab still tracks the job when storage is blocked.
  }
  for (const listener of listeners) listener()
}

function stopTimers(machineId: string) {
  const active = timers.get(machineId)
  if (!active) return
  clearInterval(active.poll)
  clearTimeout(active.deadline)
  timers.delete(machineId)
}

function clearJobs() {
  for (const id of timers.keys()) stopTimers(id)
  jobs.clear()
  emit()
}

onSignOut(clearJobs)

export function getUpdateJob(machineId: string): UpdateJob | undefined {
  return jobs.get(machineId)
}

export function isUpdateJobActive(job: UpdateJob | undefined): boolean {
  return !!job && job.phase !== "verified" && job.phase !== "failed" && job.phase !== "timed_out"
}

export function updateJobLabel(job: UpdateJob): string {
  switch (job.phase) {
    case "requested":
      return "Requested…"
    case "updating":
      return "Updating…"
    case "downloading":
      return "Downloading…"
    case "restarting":
      return "Restarting…"
    case "reconnecting":
      return "Reconnecting…"
    case "verified":
      return `Verified ${job.targetVersion}`
    case "timed_out":
      return "Timed out"
    case "failed":
      return "Update failed"
  }
}

export function startUpdateJob(
  queryClient: QueryClient,
  input: { machineId: string; machineName: string; targetVersion: string },
) {
  const job: UpdateJob = {
    machineId: input.machineId,
    machineName: input.machineName,
    targetVersion: input.targetVersion,
    phase: "requested",
    startedAt: Date.now(),
  }
  jobs.set(job.machineId, job)
  trackUpdate(queryClient, job)
  emit()
}

function trackUpdate(queryClient: QueryClient, job: UpdateJob) {
  stopTimers(job.machineId)
  let polling = false
  const pollJob = async () => {
    if (polling) return
    polling = true
    const status = await queryClient.fetchQuery({ ...machineUpdateQuery(job.machineId), retry: false }).catch(() => undefined)
    const info = !status || status.state === "idle"
      ? await queryClient.fetchQuery({ ...machineInfoQuery(job.machineId), staleTime: 0, retry: false }).catch(() => undefined)
      : undefined
    polling = false
    // A late reply from a dismissed or replaced job cannot update the new one.
    if (timers.get(job.machineId)?.poll !== poll) return
    syncUpdateJob(job.machineId, { status, pollError: !status, version: info?.version })
    if (getUpdateJob(job.machineId)?.phase === "verified") {
      for (const queryKey of [["identity"], ["topology"], ["nodes"]]) void queryClient.invalidateQueries({ queryKey })
    }
  }
  const poll = setInterval(() => void pollJob(), POLL_MS)
  const deadline = setTimeout(() => {
    const current = jobs.get(job.machineId)
    if (!current || !isUpdateJobActive(current)) return
    stopTimers(job.machineId)
    jobs.set(job.machineId, { ...current, phase: "timed_out" })
    hud.error(`${current.machineName} hasn't reported a new version`)
    emit()
  }, Math.max(0, UPDATE_DEADLINE_MS - (Date.now() - job.startedAt)))
  timers.set(job.machineId, { poll, deadline })
}

export function restoreUpdateJobs(queryClient: QueryClient) {
  if (jobs.size) return
  try {
    const saved = JSON.parse(sessionStorage.getItem(STORAGE_KEY) ?? "[]") as UpdateJob[]
    for (const job of saved) {
      if (typeof job?.machineId !== "string" || !Number.isFinite(job.startedAt)) continue
      jobs.set(job.machineId, job)
      if (isUpdateJobActive(job)) trackUpdate(queryClient, job)
    }
  } catch {
    // An older tab or blocked storage can leave no usable job history.
  }
  if (jobs.size) emit()
}

// Reconciles an active update job with the latest polled status and reported version.
export function syncUpdateJob(
  machineId: string,
  state: { status?: MachineUpdateStatus; pollError?: boolean; version?: string },
) {
  const current = jobs.get(machineId)
  if (!current || !isUpdateJobActive(current)) return

  const versionKey = (version: string) => version.replace(/^v/, "").split("+")[0]
  if (state.version && versionKey(state.version) === versionKey(current.targetVersion)) {
    stopTimers(machineId)
    jobs.set(machineId, { ...current, targetVersion: state.version, phase: "verified", error: undefined })
    hud.success(`${current.machineName} is running ${state.version}`)
    emit()
    return
  }

  const status = state.status
  const matchesJob = !status?.version || versionKey(status.version) === versionKey(current.targetVersion)
  if (matchesJob && status?.state === "failed") {
    stopTimers(machineId)
    const error = status.error || "Update failed"
    jobs.set(machineId, { ...current, phase: "failed", error })
    hud.error(`Couldn't update ${current.machineName}: ${error}`)
    emit()
    return
  }

  let nextPhase: UpdatePhase = current.phase
  if (state.pollError) nextPhase = "reconnecting"
  else if (matchesJob && status?.phase) nextPhase = status.phase
  else if (matchesJob && status?.state === "running") nextPhase = "updating"
  else if (status?.state === "idle" && current.phase !== "requested") nextPhase = "reconnecting"

  if (nextPhase !== current.phase) {
    jobs.set(machineId, { ...current, phase: nextPhase })
    emit()
  }
}

export function dismissUpdateJob(machineId: string) {
  stopTimers(machineId)
  if (jobs.delete(machineId)) emit()
}

export function resetUpdateJobsForTest() {
  clearJobs()
}

export function useUpdateJob(machineId: string): UpdateJob | undefined {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    () => jobs.get(machineId),
    () => undefined,
  )
}
