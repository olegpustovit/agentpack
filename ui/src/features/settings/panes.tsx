import { lazy, useState, useSyncExternalStore, type ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ChevronRight } from "lucide-react"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { FormGroup, FormRow } from "@/components/kit/forms"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { identityQuery } from "@/features/machines/queries"
import { useListedSessions } from "@/features/sessions/queries"
import { api, errorText, LOCAL, routeMissing } from "@/lib/api"
import { plural } from "@/lib/format"
import { hud } from "@/lib/hud"
import { terminalPool } from "@/features/terminal/pool"
import { screenReaderEnabled, setScreenReaderEnabled } from "@/features/terminal/screen-reader"
import { allowListLabel, sharingLabel } from "./model"
import { agentDefaultsQuery, settingsQuery, type AgentDefaults } from "./queries"
import { useGatewayActions } from "./actions"
import { PublicAccessBanner, PublicAccessRows } from "./public-access"
import { applyTheme, currentTheme, themes, type Theme } from "./theme"
import { AccountGroup } from "./account-pane"
import { sections, useBadges } from "./sections"

export const DiagnosticsPane = lazy(() => import("./diagnostics-pane").then((module) => ({ default: module.DiagnosticsPane })))
export const OmpPane = lazy(() => import("./omp-pane").then((module) => ({ default: module.OmpPane })))
export const ConnectionsPane = lazy(() => import("./connections-pane").then((module) => ({ default: module.ConnectionsPane })))

// The settings view shows the title and scope; a pane stacks its groups.
export function Pane({ title, children }: { title: string; children: ReactNode }) {
  return <section aria-label={title} className="flex min-w-0 flex-col gap-(--s7)">{children}</section>
}

// A failed load says what it was and offers a retry; the pane never shows stale data as fresh.
export function LoadError({ what, error, retry }: { what: string; error: Error; retry: () => void }) {
  return (
    <Banner tone="danger" action={<Button variant="ghost" onClick={retry}>Retry</Button>}>
      {`Couldn't load ${what}: ${error.message}`}
    </Banner>
  )
}

export function GeneralPane() {
  const settings = useQuery(settingsQuery)
  const identity = useQuery(identityQuery)
  const badges = useBadges()
  const failed = settings.error ?? identity.error
  if (failed) {
    return (
      <LoadError
        what="gateway details"
        error={failed}
        retry={() => {
          void settings.refetch()
          void identity.refetch()
        }}
      />
    )
  }
  if (!settings.data || !identity.data) return <RowSkeleton lines={2} />
  return (
    <Pane title="General">
      <AccountGroup />
      <AppearanceGroup />
      <FormGroup>
        {sections.filter((item) => item.parent === "general").map((item) => (
          <Link key={item.id} to="/settings/$pane" params={{ pane: item.id }} className={linkRow}>
            <FormRow label={<span className="flex items-center gap-(--s3)"><item.icon aria-hidden className="size-(--fs) text-(--fg2)" />{item.label}</span>} className="border-t-0">
              {badges[item.id]}
              <ChevronRight aria-hidden className="size-(--fs) text-(--fg3)" />
            </FormRow>
          </Link>
        ))}
      </FormGroup>
      <MachineControls machine={identity.data.node.name} version={`${identity.data.node.version} · ${settings.data.platform}`} stopped={!settings.data.running} canManage={settings.data.can_manage !== false} />
    </Pane>
  )
}

const linkRow = "block outline-none hover:bg-(--hover) focus-visible:bg-(--hover) [&+&]:border-t-[0.5px] [&+&]:border-(--sep-soft)"

// Machine lifecycle, not account: each confirmation names the machine, what stops and how to recover.
function MachineControls({ machine, version, stopped, canManage }: { machine: string; version: string; stopped: boolean; canManage: boolean }) {
  const actions = useGatewayActions()
  const running = useListedSessions().sessions.filter((session) => session.machine === LOCAL && session.state.running)
  const [confirm, setConfirm] = useState<"agents" | "gateway" | null>(null)
  const sessions = plural(running.length, "running session")
  const agents = plural(running.filter((session) => session.transport !== "shell").length, "running agent session")
  const confirms = {
    agents: { title: `Stop all agents on ${machine}?`, description: `${agents} on ${machine} may stop, including in-progress turns. Shells and chats owned by another device keep running.`, destructiveLabel: "Stop all agents" },
    gateway: { title: `Stop the gateway on ${machine}?`, description: `Browser access to ${machine} disconnects. ${sessions} keep running in the CLI. Run ap gateway start on ${machine} to reconnect.`, destructiveLabel: "Stop gateway" },
  }
  return (
    <FormGroup title={machine}>
      <FormRow label="agentpack">{version}</FormRow>
      {canManage && <>
        <FormRow label="Agents" description={agents}>
          <Button variant="destructive" disabled={stopped} onClick={() => setConfirm("agents")}>Stop all…</Button>
        </FormRow>
        <FormRow label="Gateway">
          <Button variant="destructive" disabled={stopped} onClick={() => setConfirm("gateway")}>Stop…</Button>
        </FormRow>
      </>}
      <ConfirmAlert open={confirm !== null} onOpenChange={(open) => !open && setConfirm(null)}>
        {confirm && (
          <ConfirmAlertContent
            {...confirms[confirm]}
            onConfirm={() => {
              setConfirm(null)
              if (confirm === "agents") actions.stopAgents.mutate()
              else actions.stopGateway.mutate()
            }}
          />
        )}
      </ConfirmAlert>
    </FormGroup>
  )
}

export function SharingPane() {
  const settings = useQuery(settingsQuery)
  const { sharing } = useGatewayActions()
  if (settings.error) {
    return <LoadError what="sharing settings" error={settings.error} retry={() => void settings.refetch()} />
  }
  if (!settings.data) return <RowSkeleton lines={2} />
  const enabled = settings.data.sharing_enabled
  const canManage = settings.data.can_manage !== false
  return (
    <Pane title="Sharing">
      {canManage && <PublicAccessBanner access={settings.data.public} />}
      {settings.data.public && (
        <FormGroup title="Internet" footer="Gateway data and MCP operations always require sign-in.">
          <PublicAccessRows access={settings.data.public} canManage={canManage} />
        </FormGroup>
      )}
      <FormGroup title="Tailnet" footer={!canManage ? "Only the gateway owner can change sharing." : undefined}>
        <FormRow label="Tailscale sharing" htmlFor="sharing" description={sharingLabel(enabled)}>
          <Switch
            id="sharing"
            checked={enabled}
            disabled={sharing.isPending || !canManage}
            onCheckedChange={(next) => sharing.mutate(next)}
          />
        </FormRow>
        <FormRow label="Allow list" description="Tailscale tags and machines that can reach this gateway.">{allowListLabel(settings.data.allowed_peers)}</FormRow>
      </FormGroup>
    </Pane>
  )
}

const themeLabels: Record<Theme, string> = { system: "System", light: "Light", dark: "Dark" }
const systemTheme = () => window.matchMedia("(prefers-color-scheme: dark)")

function subscribeToSystemTheme(notify: () => void) {
  const preference = systemTheme()
  preference.addEventListener("change", notify)
  return () => preference.removeEventListener("change", notify)
}

function AppearanceGroup() {
  const [theme, setTheme] = useState<Theme>(currentTheme)
  const dark = useSyncExternalStore(subscribeToSystemTheme, () => systemTheme().matches)
  const [screenReader, setScreenReader] = useState(screenReaderEnabled)
  function choose(value: unknown[]) {
    const next = themes.find((item) => item === value[0])
    if (!next) return
    applyTheme(next)
    setTheme(next)
  }
  return (
    <FormGroup title="Appearance">
      <FormRow label="Theme" description={theme === "system" ? <span role="status">{`Currently ${dark ? "dark" : "light"}`}</span> : undefined}>
        <SegmentedControl aria-label="Theme" value={[theme]} onValueChange={choose}>
          {themes.map((item) => (
            <SegmentedControlItem key={item} value={item}>
              {themeLabels[item]}
            </SegmentedControlItem>
          ))}
        </SegmentedControl>
      </FormRow>
      <FormRow label="Terminal screen reader mode" htmlFor="terminal-screen-reader">
        <Switch
          id="terminal-screen-reader"
          checked={screenReader}
          onCheckedChange={(checked) => {
            setScreenReaderEnabled(checked)
            setScreenReader(checked)
            // The mode is chosen when a terminal opens; open sessions reconnect with it on return.
            terminalPool.disposeAll()
          }}
        />
      </FormRow>
    </FormGroup>
  )
}

export function AgentsPane() {
  const defaults = useQuery(agentDefaultsQuery)
  const settings = useQuery(settingsQuery)
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: (change: Partial<AgentDefaults>) => api<AgentDefaults>("/api/agents/defaults", { method: "PUT", body: change }),
    onSuccess: async (next) => {
      await queryClient.cancelQueries({ queryKey: agentDefaultsQuery.queryKey })
      queryClient.setQueryData(agentDefaultsQuery.queryKey, next)
      await queryClient.invalidateQueries({ queryKey: agentDefaultsQuery.queryKey })
    },
    onError: (error, change) => hud.error(`Couldn't change agent defaults: ${errorText(error)}`, () => save.mutate(change)),
  })
  if (defaults.error) {
    if (routeMissing(defaults.error)) return <Empty title="Needs update">Update agentpack on this gateway to change agent defaults.</Empty>
    return <LoadError what="agent defaults" error={defaults.error} retry={() => void defaults.refetch()} />
  }
  if (settings.error) return <LoadError what="gateway settings" error={settings.error} retry={() => void settings.refetch()} />
  if (!defaults.data || !settings.data) return <RowSkeleton lines={2} />
  const { agentpack_tools: tools, replace_subagents: replace, bypass_permissions: bypass, auto_approve_helpers: helpers } = defaults.data
  const readOnly = settings.data.can_manage === false
  const disabled = save.isPending || readOnly
  return (
    <Pane title="Agents">
      <FormGroup title="Permissions" footer={
        bypass === undefined ? "Update this gateway to change permission defaults." : "Applies when a chat starts or resumes."
      }>
        <FormRow label="Bypass permissions by default" htmlFor="bypass-permissions" description="Chats run commands and edit files without asking.">
          <Switch id="bypass-permissions" checked={bypass ?? false} disabled={disabled || bypass === undefined} onCheckedChange={(on) => save.mutate({ bypass_permissions: on })} />
        </FormRow>
        <FormRow label="Auto-approve helper chats" htmlFor="auto-approve-helpers" description="Even when bypass is off.">
          <Switch id="auto-approve-helpers" checked={helpers ?? false} disabled={disabled || helpers === undefined} onCheckedChange={(on) => save.mutate({ auto_approve_helpers: on })} />
        </FormRow>
      </FormGroup>
      <FormGroup title="New chats" footer={<>
        <p>Running chats keep what they started with.</p>
        {readOnly && <p>Only the gateway owner can change agent defaults.</p>}
      </>}>
        <FormRow label="agentpack tools" htmlFor="agentpack-tools" description="Agents start helpers, read other chats, ask you questions and install packages.">
          <Switch id="agentpack-tools" checked={tools} disabled={disabled} onCheckedChange={(on) => save.mutate({ agentpack_tools: on })} />
        </FormRow>
        <FormRow
          label="Replace built-in subagents"
          htmlFor="replace-subagents"
          description={tools ? "Helpers run as chats you can watch and stop." : "Needs agentpack tools."}
        >
          <Switch id="replace-subagents" checked={tools && replace} disabled={disabled || !tools} onCheckedChange={(on) => save.mutate({ replace_subagents: on })} />
        </FormRow>
      </FormGroup>
    </Pane>
  )
}
