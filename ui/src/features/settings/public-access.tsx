import { useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Check, Copy, X } from "lucide-react"
import { Banner, Spinner } from "@/components/kit/feedback"
import { FieldError } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { errorText } from "@/features/library/mutations"
import { apiLines } from "@/lib/api"
import type { PublicAccess, Settings } from "@/lib/api-types"
import { useCopied } from "@/lib/hud"
import { Row } from "./panes"
import { settingsQuery } from "./queries"

type CheckResult = { check: string; ok: boolean; detail: string }

const checks = [
  ["reachable", "Reachable"],
  ["sign_in", "Sign-in required"],
  ["cookies", "Secure cookies"],
  ["mcp", "MCP sign-in works"],
] as const

export function publicHost(origin: string): string {
  return origin.replace(/^https:\/\//, "")
}

// Shown above the Sharing fields while off, once a proxy or Funnel forwards to this gateway.
export function PublicAccessBanner({ access }: { access: PublicAccess | undefined }) {
  if (!access || access.enabled || !access.candidate) return null
  return (
    <Banner className="mb-(--s5) rounded-(--r-pop) border-b-0 bg-(--accent-soft)">
      Detected <b>{publicHost(access.candidate)}</b> forwarding to this gateway through a proxy. Turn on Public access
      only if that is your own domain.
    </Banner>
  )
}

// One switch: the gateway verifies the detected address reaches itself before serving it.
export function PublicAccessRows({ access }: { access: PublicAccess }) {
  const queryClient = useQueryClient()
  const [results, setResults] = useState<CheckResult[] | null>(null)
  // A catch-all proxy can't tell us the address, so the owner types it; the checks still verify it.
  const [typed, setTyped] = useState("")
  const typedHost = typed.trim().replace(/^https?:\/\//, "").replace(/\/+$/, "")
  const origin = access.candidate ?? (access.catch_all && typedHost ? `https://${typedHost}` : undefined)
  const change = useMutation<PublicAccess | undefined, Error, boolean>({
    mutationFn: async (enabled) => {
      setResults(enabled ? [] : null)
      let status: PublicAccess | undefined
      let failure = ""
      await apiLines("/api/settings/public", { enabled, origin }, (line) => {
        if (!line || typeof line !== "object") return
        if ("check" in line) setResults((old) => [...(old ?? []), line as CheckResult])
        if ("public" in line) status = line.public as PublicAccess
        if ("error" in line && typeof line.error === "string") failure = line.error
      })
      if (failure) throw new Error(failure)
      return status
    },
    onSuccess: (status) => {
      if (status) queryClient.setQueryData<Settings>(settingsQuery.queryKey, (old) => old && { ...old, public: status })
    },
  })
  const checking = change.isPending && change.variables
  const on = access.enabled || checking
  const label = checking
    ? "Checking…"
    : access.enabled
      ? `On · ${access.origin}${access.pinned ? " · set by AGENTPACK_PUBLIC_URL" : ""}`
      : "Off. Only this machine and your tailnet can reach it"
  return (
    <>
      <Row label="Public access" htmlFor="public-access">
        <Switch
          id="public-access"
          checked={Boolean(on)}
          disabled={change.isPending || access.pinned || (!access.enabled && !origin)}
          onCheckedChange={(next) => change.mutate(next)}
        />
        <span className="text-(length:--fs-sm) text-(--fg2)">{label}</span>
        {access.catch_all && !access.enabled && (
          <Input
            aria-label="Public address"
            placeholder="agentpack.example.com"
            value={typed}
            disabled={change.isPending}
            onChange={(event) => setTyped(event.currentTarget.value)}
            className="w-64"
          />
        )}
        <div className="basis-full" />
        {results ? (
          <ul className="grid gap-(--s2) text-(length:--fs-sm)" aria-label="Public access checks">
            {checks.map(([id, title]) => {
              const result = results.find((item) => item.check === id)
              const failedEarlier = !result && results.some((item) => !item.ok)
              if (failedEarlier) return null
              return (
                <li key={id} className="flex items-center gap-(--s3)">
                  {!result ? (
                    <Spinner className="text-(--fg3)" />
                  ) : result.ok ? (
                    <Check aria-label="Passed" className="size-(--fs) text-(--ok-word)" />
                  ) : (
                    <X aria-label="Failed" className="size-(--fs) text-(--danger-word)" />
                  )}
                  <span>{title}</span>
                  {result && <span className="text-(--fg3)">{result.detail}</span>}
                </li>
              )
            })}
          </ul>
        ) : (
          !access.enabled && (
            <p className="m-0 max-w-[60ch] text-(length:--fs-xs) text-(--fg2)">
              {access.candidate
                ? "Anyone on the internet can reach the sign-in page and the MCP endpoint. Every request still needs your login."
                : access.catch_all
                  ? "Your proxy forwards any host name, so the address isn't suggested. Type your own; it is checked before anything opens."
                  : "Put an HTTPS proxy or Tailscale Funnel in front of this gateway and open its address once; it appears here."}
            </p>
          )
        )}
        {change.error && <FieldError className="basis-full">{errorText(change.error)}</FieldError>}
      </Row>
      {access.enabled && access.origin && <McpAddress address={`${access.origin}/mcp`} />}
    </>
  )
}

function McpAddress({ address }: { address: string }) {
  const [copied, copy] = useCopied()
  return (
    <Row label="MCP address">
      <code className="rounded-(--r-ctl) bg-(--code) px-(--s2) font-(family-name:--mono) text-(length:--fs-sm)">{address}</code>
      <Button onClick={() => copy(address, "Couldn't copy. Select the address and copy it manually.")}>
        {copied ? <Check aria-hidden /> : <Copy aria-hidden />}
        {copied ? "Copied" : "Copy"}
      </Button>
      <p className="m-0 max-w-[60ch] basis-full text-(length:--fs-xs) text-(--fg2)">
        Paste it into Claude.ai, ChatGPT, Claude Code or codemode. They sign in through this gateway; nothing else to
        configure.
      </p>
    </Row>
  )
}
