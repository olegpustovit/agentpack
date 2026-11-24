import { lazy, useState, useSyncExternalStore, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Section } from "@/components/kit/data"
import { Banner, RowSkeleton } from "@/components/kit/feedback"
import { Field, FieldGrid, FieldLabel } from "@/components/kit/forms"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { StatusDot } from "@/components/kit/status-dot"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { identityQuery } from "@/features/machines/queries"
import { useListedSessions } from "@/features/sessions/queries"
import { LOCAL } from "@/lib/api"
import { plural } from "@/lib/format"
import { terminalPool } from "@/features/terminal/pool"
import { screenReaderEnabled, setScreenReaderEnabled } from "@/features/terminal/screen-reader"
import { allowListLabel, sharingLabel } from "./model"
import { settingsQuery } from "./queries"
import { useGatewayActions } from "./actions"
import { PublicAccessBanner, PublicAccessRows } from "./public-access"
import { applyTheme, currentTheme, themes, type Theme } from "./theme"

export const DiagnosticsPane = lazy(() => import("./diagnostics-pane").then((module) => ({ default: module.DiagnosticsPane })))
export const OmpPane = lazy(() => import("./omp-pane").then((module) => ({ default: module.OmpPane })))
export const ConnectionsPane = lazy(() => import("./connections-pane").then((module) => ({ default: module.ConnectionsPane })))

// The selected source row names the pane, so its heading is for assistive tech only.
export function Pane({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section>
      <h2 className="sr-only">{title}</h2>
      {children}
    </section>
  )
}

export function Row({ label, htmlFor, children }: { label: string; htmlFor?: string; children: ReactNode }) {
  return (
    <Field>
      <FieldLabel htmlFor={htmlFor}>{label}</FieldLabel>
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-(--s3) break-words">{children}</div>
    </Field>
  )
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
      <Section title="This gateway">
        <FieldGrid>
          <Row label="Gateway">
            <StatusDot state={settings.data.running ? "running" : "stopped"} />
          </Row>
          <Row label="Platform">{settings.data.platform}</Row>
          <Row label="Version">{identity.data.node.version}</Row>
          <Row label="Hostname">{identity.data.node.name}</Row>
        </FieldGrid>
        <nav aria-label="Gateway details" className="flex flex-wrap gap-(--s4) py-(--s4) text-(--accent)">
          <Link to="/machines/$machine" params={{ machine: LOCAL }}>Machine details</Link>
          <Link to="/settings/$pane" params={{ pane: "sharing" }}>Connectivity</Link>
          <Link to="/settings/$pane" params={{ pane: "diagnostics" }}>Diagnostics</Link>
        </nav>
      </Section>
      {settings.data.can_manage !== false && <MachineControls machine={identity.data.node.name} stopped={!settings.data.running} />}
    </Pane>
  )
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
    <Section title="Gateway controls">
      <div className="flex flex-col gap-(--s4)">
        <div className="flex items-center justify-between gap-(--s6)">
          <p>{`Stop agent sessions on ${machine} (${agents}).`}</p>
          <Button variant="destructive" disabled={stopped} onClick={() => setConfirm("agents")}>Stop all agents…</Button>
        </div>
        <div className="flex items-center justify-between gap-(--s6)">
          <p>Stop this gateway. CLI sessions keep running. Run <code>ap gateway start</code> to reconnect.</p>
          <Button variant="destructive" disabled={stopped} onClick={() => setConfirm("gateway")}>Stop gateway…</Button>
        </div>
      </div>
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
    </Section>
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
      <FieldGrid>
        {settings.data.public && <PublicAccessRows access={settings.data.public} canManage={canManage} />}
        <Row label="Tailscale sharing" htmlFor="sharing">
          <Switch
            id="sharing"
            checked={enabled}
            disabled={sharing.isPending || !canManage}
            onCheckedChange={(next) => sharing.mutate(next)}
          />
          <span className="text-(length:--fs-sm) text-(--fg2)">{sharingLabel(enabled)}</span>
          {!canManage && <span className="text-(length:--fs-sm) text-(--fg2)">Only the gateway owner can change sharing.</span>}
        </Row>
        <Row label="Allow list">{allowListLabel(settings.data.allowed_peers)}</Row>
      </FieldGrid>
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
      <FieldGrid>
        <Row label="Theme">
          <SegmentedControl aria-label="Theme" value={[theme]} onValueChange={choose}>
            {themes.map((item) => (
              <SegmentedControlItem key={item} value={item}>
                {themeLabels[item]}
              </SegmentedControlItem>
            ))}
          </SegmentedControl>
          <span className="text-(length:--fs-sm) text-(--fg2)" role="status">{`Currently ${themeLabels[theme === "system" ? dark ? "dark" : "light" : theme]}`}</span>
        </Row>
      </FieldGrid>
    </Pane>
  )
}

export function AccessibilityPane() {
  const [screenReader, setScreenReader] = useState(screenReaderEnabled)
  return (
    <Pane title="Accessibility">
      <FieldGrid>
        <Row label="Terminal screen reader mode" htmlFor="terminal-screen-reader">
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
          <span id="terminal-screen-reader-scope" className="text-(length:--fs-sm) text-(--fg2)">Applies to this browser only. Open terminals reconnect with this setting.</span>
        </Row>
      </FieldGrid>
    </Pane>
  )
}
