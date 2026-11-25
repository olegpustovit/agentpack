import { lazy, useState, useSyncExternalStore, type ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ChevronRight } from "lucide-react"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { FormGroup, FormRow } from "@/components/kit/forms"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { StatusDot } from "@/components/kit/status-dot"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { identityQuery } from "@/features/machines/queries"
import { useListedSessions } from "@/features/sessions/queries"
import { api, errorText, LOCAL, routeMissing } from "@/lib/api"
import { harnessName, plural } from "@/lib/format"
import { hud } from "@/lib/hud"
import { terminalPool } from "@/features/terminal/pool"
import { screenReaderEnabled, setScreenReaderEnabled } from "@/features/terminal/screen-reader"
import { allowListLabel, sharingLabel } from "./model"
import { agentDefaultsQuery, settingsQuery, type AgentDefaults } from "./queries"
import { useGatewayActions } from "./actions"
import { PublicAccessBanner, PublicAccessRows } from "./public-access"
import { applyTheme, currentTheme, themes, type Theme } from "./theme"

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
      <FormGroup title="About">
        <FormRow label="Gateway"><StatusDot state={settings.data.running ? "running" : "stopped"} /></FormRow>
        <FormRow label="Version">{identity.data.node.version}</FormRow>
        <FormRow label="Platform">{settings.data.platform}</FormRow>
        <FormRow label="Hostname">{identity.data.node.name}</FormRow>
      </FormGroup>
      <FormGroup>
        <Link to="/machines/$machine" params={{ machine: LOCAL }} className={linkRow}><LinkRow label="Machine details" description="Hardware, agents, limits and updates" /></Link>
      </FormGroup>
      {settings.data.can_manage !== false && <MachineControls machine={identity.data.node.name} stopped={!settings.data.running} />}
    </Pane>
  )
}

const linkRow = "block outline-none hover:bg-(--hover) focus-visible:bg-(--hover) [&+&]:border-t-[0.5px] [&+&]:border-(--sep-soft)"

// A row that opens another screen: macOS disclosure chevron, the whole row is the link.
function LinkRow({ label, description }: { label: string; description: string }) {
  return <FormRow label={label} description={description} className="border-t-0"><ChevronRight aria-hidden className="size-(--fs) text-(--fg3)" /></FormRow>
}

// Machine lifecycle, not account: each confirmation names the machine, what stops and how to recover.
function MachineControls({ machine, stopped }: { machine: string; stopped: boolean }) {
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
    <FormGroup title="Stop" footer={<>Stopping is reversible: run <code>ap gateway start</code> on {machine} to reconnect.</>}>
      <FormRow label="Stop all agents" description={`${agents} on ${machine}. Shells and chats owned by another device keep running.`}>
        <Button variant="destructive" disabled={stopped} onClick={() => setConfirm("agents")}>Stop agents…</Button>
      </FormRow>
      <FormRow label="Stop gateway" description="Browser access disconnects. CLI sessions keep running.">
        <Button variant="destructive" disabled={stopped} onClick={() => setConfirm("gateway")}>Stop gateway…</Button>
      </FormRow>
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

export function AppearancePane() {
  const [theme, setTheme] = useState<Theme>(currentTheme)
  const dark = useSyncExternalStore(subscribeToSystemTheme, () => systemTheme().matches)
  function choose(value: unknown[]) {
    const next = themes.find((item) => item === value[0])
    if (!next) return
    applyTheme(next)
    setTheme(next)
  }
  return (
    <Pane title="Appearance">
      <FormGroup>
        <FormRow label="Theme" description={<span role="status">{`Currently ${themeLabels[theme === "system" ? dark ? "dark" : "light" : theme]}`}</span>}>
          <SegmentedControl aria-label="Theme" value={[theme]} onValueChange={choose}>
            {themes.map((item) => (
              <SegmentedControlItem key={item} value={item}>
                {themeLabels[item]}
              </SegmentedControlItem>
            ))}
          </SegmentedControl>
        </FormRow>
      </FormGroup>
    </Pane>
  )
}

export function AccessibilityPane() {
  const [screenReader, setScreenReader] = useState(screenReaderEnabled)
  return (
    <Pane title="Accessibility">
      <FormGroup>
        <FormRow label="Terminal screen reader mode" htmlFor="terminal-screen-reader" description={<span id="terminal-screen-reader-scope">Open terminals reconnect with this setting.</span>}>
          <Switch
            id="terminal-screen-reader"
            aria-describedby="terminal-screen-reader-scope"
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
    </Pane>
  )
}

// Each harness's own subagent tool, and whether agentpack can turn it off today.
const subagentCoverage = [
  { harness: "claude", tool: "Agent, Task", replaceable: true },
  { harness: "codex", tool: "Subagents", replaceable: true },
  { harness: "opencode", tool: "task", replaceable: false },
  { harness: "omp", tool: "task", replaceable: false },
  { harness: "cursor", tool: "Subagents", replaceable: false },
  { harness: "agy", tool: "invoke_subagent", replaceable: false },
]

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
  const { agentpack_tools: tools, replace_subagents: replace } = defaults.data
  const readOnly = settings.data.can_manage === false
  const disabled = save.isPending || readOnly
  return (
    <Pane title="Agents">
      <FormGroup title="New chats" footer={<>
        <p>Running chats keep what they started with. Turn agentpack tools on or off for one chat in its inspector.</p>
        {readOnly && <p>Only the gateway owner can change agent defaults.</p>}
      </>}>
        <FormRow label="agentpack tools" htmlFor="agentpack-tools" description="The agentpack MCP server: agents start helper chats, read other sessions, ask you questions and install packages.">
          <Switch id="agentpack-tools" checked={tools} disabled={disabled} onCheckedChange={(on) => save.mutate({ agentpack_tools: on })} />
        </FormRow>
        <FormRow
          label="Replace built-in subagents"
          htmlFor="replace-subagents"
          description={tools ? "Helpers run as agentpack chats you can watch, stop and approve, instead of hidden subagents." : "Needs agentpack tools: without them an agent keeps its own subagents."}
        >
          <Switch id="replace-subagents" checked={tools && replace} disabled={disabled || !tools} onCheckedChange={(on) => save.mutate({ replace_subagents: on })} />
        </FormRow>
      </FormGroup>
      <FormGroup title="Built-in subagents" footer="Where an agent keeps its own tool, agentpack’s run_helper tool asks it to start a helper chat instead. Grok has no subagents.">
        {subagentCoverage.map((item) => (
          <FormRow key={item.harness} label={harnessName(item.harness)} description={item.tool}>
            {!item.replaceable ? "Kept" : tools && replace ? "Replaced by helpers" : "On"}
          </FormRow>
        ))}
      </FormGroup>
    </Pane>
  )
}
