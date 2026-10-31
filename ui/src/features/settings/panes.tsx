import { useState, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Banner, RowSkeleton } from "@/components/kit/feedback"
import { Field, FieldGrid, FieldLabel } from "@/components/kit/forms"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { StatusDot } from "@/components/kit/status-dot"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { identityQuery } from "@/features/machines/queries"
import { allowListLabel, sharingLabel } from "./model"
import { settingsQuery } from "./queries"
import { useGatewayActions } from "./actions"
import { applyTheme, currentTheme, themes, type Theme } from "./theme"

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
      <FieldGrid>
        <Row label="Gateway">
          <StatusDot state={settings.data.running ? "running" : "stopped"} />
        </Row>
        <Row label="Platform">{settings.data.platform}</Row>
        <Row label="Version">{identity.data.node.version}</Row>
        <Row label="Hostname">{identity.data.node.name}</Row>
      </FieldGrid>
    </Pane>
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
  return (
    <Pane title="Sharing">
      <FieldGrid>
        <Row label="Tailscale sharing" htmlFor="sharing">
          <Switch
            id="sharing"
            checked={enabled}
            disabled={sharing.isPending}
            onCheckedChange={(next) => sharing.mutate(next)}
          />
          <span className="text-(length:--fs-sm) text-(--fg2)">{sharingLabel(enabled)}</span>
        </Row>
        <Row label="Allow list">{allowListLabel(settings.data.allowed_peers)}</Row>
      </FieldGrid>
    </Pane>
  )
}

const themeLabels: Record<Theme, string> = { system: "System", light: "Light", dark: "Dark" }

export function AppearancePane() {
  const [theme, setTheme] = useState<Theme>(currentTheme)
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
        </Row>
      </FieldGrid>
    </Pane>
  )
}
