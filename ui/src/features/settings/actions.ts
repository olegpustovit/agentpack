import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { replicationQuery } from "@/features/library/queries"
import { errorText } from "@/features/library/mutations"
import { nodesQuery } from "@/features/machines/queries"
import { sessionsQuery } from "@/features/sessions/queries"
import { terminalPool } from "@/features/terminal/pool"
import { ApiError, api, LOCAL } from "@/lib/api"
import type { Settings } from "@/lib/api-types"
import { hud } from "@/lib/hud"
import { settingsQuery } from "./queries"

type Peer = { id: string; name: string }

// Every gateway command reports once through the Hud: success, or failure with Retry.
export function useGatewayActions() {
  const queryClient = useQueryClient()
  const navigate = useNavigate()

  const sharing = useMutation<unknown, Error, boolean>({
    mutationFn: (enabled) => api("/api/settings/sharing", { body: { enabled } }),
    onSuccess: () => {
      hud.success("Sharing change applies within five seconds. Local CLI sessions keep running.")
      void queryClient.invalidateQueries({ queryKey: settingsQuery.queryKey })
      void queryClient.invalidateQueries({ queryKey: nodesQuery(false).queryKey })
    },
    onError: (error, enabled) =>
      hud.error(`Couldn't turn sharing ${enabled ? "on" : "off"}: ${errorText(error)}`, () => sharing.mutate(enabled)),
  })

  const refreshReplication = () => queryClient.invalidateQueries({ queryKey: replicationQuery.queryKey })

  const enroll = useMutation<unknown, Error, Peer>({
    mutationFn: (peer) => api("/api/library/replication/peers", { body: { peer: peer.id } }),
    onSuccess: () => {
      hud.success("Enrolled. The other machine must enroll this gateway too.")
      void refreshReplication()
    },
    onError: (error, peer) => hud.error(`Couldn't enroll ${peer.name}: ${errorText(error)}`, () => enroll.mutate(peer)),
  })

  const sync = useMutation<unknown, Error, Peer>({
    mutationFn: (peer) => api("/api/library/replication/sync", { body: { peer: peer.id } }),
    onSuccess: (_result, peer) => {
      hud.success(`Sync complete with ${peer.name}.`)
      void refreshReplication()
    },
    onError: (error, peer) => hud.error(`Couldn't sync ${peer.name}: ${errorText(error)}`, () => sync.mutate(peer)),
  })

  const remove = useMutation<unknown, Error, Peer>({
    mutationFn: (peer) => api(`/api/library/replication/peers/${encodeURIComponent(peer.id)}`, { method: "DELETE" }),
    onSuccess: () => {
      hud.success("Enrollment removed.")
      void refreshReplication()
    },
    onError: (error, peer) =>
      hud.error(`Couldn't remove ${peer.name}: ${errorText(error)}`, () => remove.mutate(peer)),
  })

  const stopAgents = useMutation<unknown, Error, void>({
    mutationFn: () => api("/api/node/stop-agents", { method: "POST" }),
    onSuccess: () => {
      hud.success("Agents on the gateway machine have been stopped.")
      void queryClient.invalidateQueries({ queryKey: sessionsQuery(LOCAL, false).queryKey })
    },
    onError: (error) =>
      hud.error(`Couldn't stop the agents on the gateway machine: ${errorText(error)}`, () => stopAgents.mutate()),
  })

  const stopGateway = useMutation<unknown, Error, void>({
    mutationFn: () => {
      // The gateway drops every socket when it stops; close ours first so nothing reconnects to it.
      terminalPool.disposeAll()
      return api("/api/node/stop", { method: "POST" })
    },
    onSuccess: () => {
      queryClient.setQueryData<Settings>(settingsQuery.queryKey, (old) => old && { ...old, running: false })
      hud.success(
        "Gateway stopped. CLI sessions keep running. Run agentpack gateway start on the gateway machine to reconnect.",
      )
    },
    onError: (error) => hud.error(`Couldn't stop the gateway: ${errorText(error)}`, () => stopGateway.mutate()),
  })

  const signOut = useMutation<unknown, Error, void>({
    mutationFn: () => {
      terminalPool.disposeAll()
      return api("/api/logout", { method: "POST" })
    },
    onSuccess: () => {
      queryClient.clear()
      void navigate({ to: "/login" })
    },
    // A 401 means the session already ended; the unauthorized handler has sent the user to /login.
    onError: (error) => {
      if (!(error instanceof ApiError && error.status === 401)) {
        hud.error(`Couldn't sign out: ${errorText(error)}`, () => signOut.mutate())
      }
    },
  })

  return { sharing, enroll, sync, remove, stopAgents, stopGateway, signOut }
}
