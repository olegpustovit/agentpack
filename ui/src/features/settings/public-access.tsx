import { useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Check, Copy, X } from "lucide-react"
import { Banner, Spinner } from "@/components/kit/feedback"
import { FieldError } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { apiLines, errorText } from "@/lib/api"
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
      Detected <b>{publicHost(access.candidate)}</b> forwarding to this gateway. Check the address if you own the domain.
    </Banner>
  )
}

export function PublicAccessRows({ access, canManage }: { access: PublicAccess; canManage: boolean }) {
  const queryClient = useQueryClient()
  const [results, setResults] = useState<CheckResult[] | null>(null)
  const [verified, setVerified] = useState<{ requested: string; origin: string }>()
  // A catch-all proxy can't tell us the address, so the owner types it; the checks still verify it.
  const [typed, setTyped] = useState("")
  const typedHost = typed.trim().replace(/^https?:\/\//, "").replace(/\/+$/, "")
  const origin = access.candidate ?? (access.catch_all && typedHost ? `https://${typedHost}` : undefined)
  const change = useMutation({
    mutationFn: async ({ action, address }: { action: "check" | "enable" | "disable"; address?: string }) => {
      setVerified(undefined)
      setResults(action === "disable" ? null : [])
      let status: PublicAccess | undefined
      let verifiedOrigin: string | undefined
      let failure = ""
      let failedCheck = false
      await apiLines("/api/settings/public", {
        enabled: action !== "disable",
        origin: address,
        ...(action === "check" ? { check_only: true } : {}),
      }, (line) => {
        if (!line || typeof line !== "object") return
        if ("check" in line) {
          const check = line as CheckResult
          if (!check.ok) failedCheck = true
          setResults((old) => [...(old ?? []), check])
        }
        if ("public" in line) status = line.public as PublicAccess
        if ("verified_origin" in line && typeof line.verified_origin === "string") verifiedOrigin = line.verified_origin
        if ("error" in line && typeof line.error === "string") failure = line.error
      })
      if (failure) throw new Error(failure)
      if ((action !== "check" || !verifiedOrigin) && !status && !failedCheck) {
        throw new Error("The gateway did not finish. Check the address and try again.")
      }
      return { status, verified: action === "check" && address && verifiedOrigin ? { requested: address, origin: verifiedOrigin } : undefined }
    },
    onSuccess: ({ status, verified: checked }) => {
      setVerified(checked)
      if (status) queryClient.setQueryData<Settings>(settingsQuery.queryKey, (old) => old && { ...old, public: status })
    },
  })
  const checking = change.isPending && change.variables?.action !== "disable"
  const checked = verified?.requested === origin ? verified : undefined
  const unavailable = change.isPending || access.pinned || !canManage
  const label = checking
    ? `Checking ${origin ? publicHost(origin) : "the address"}… Access is off`
    : access.enabled
      ? `On · ${access.origin}`
      : checked
        ? `Off · ${publicHost(checked.origin)} passed its checks`
        : "Off. Only this machine and your tailnet can reach it"
  return (
    <>
      <Row label="Public access" htmlFor="public-access">
        <Switch
          id="public-access"
          checked={access.enabled}
          disabled={unavailable || !access.enabled}
          onCheckedChange={(next) => { if (!next) change.mutate({ action: "disable" }) }}
        />
        <span role="status" className="min-w-0 break-all text-(length:--fs-sm) text-(--fg2)">{label}</span>
        {access.catch_all && !access.enabled && (
          <Input
            aria-label="Public address"
            placeholder="agentpack.example.com"
            value={typed}
            disabled={unavailable}
            onChange={(event) => {
              setTyped(event.currentTarget.value)
              setVerified(undefined)
              setResults(null)
              change.reset()
            }}
            className="w-64 max-w-full"
          />
        )}
        {!access.enabled && origin && canManage && !access.pinned && !checked && (
          <Button disabled={unavailable} onClick={() => change.mutate({ action: "check", address: origin })}>
            {checking ? "Checking…" : "Check address"}
          </Button>
        )}
        {access.pinned && <p className="basis-full text-(length:--fs-sm) text-(--fg2)"><code>AGENTPACK_PUBLIC_URL</code> manages this setting.</p>}
        {!canManage && !access.pinned && <p className="basis-full text-(length:--fs-sm) text-(--fg2)">Only the gateway owner can change public access.</p>}
        {checked && !access.enabled && (
          <>
            <p className="basis-full text-(length:--fs-sm) text-(--fg2)">Enable access to make the sign-in page and MCP endpoint at {publicHost(checked.origin)} reachable from the internet. Gateway data and MCP operations still require sign-in.</p>
            <Button variant="primary" disabled={unavailable} onClick={() => change.mutate({ action: "enable", address: checked.origin })}>Enable access</Button>
          </>
        )}
        {results && (
          <ul className="grid basis-full gap-(--s2) text-(length:--fs-sm)" aria-label="Public access checks">
            {checks.map(([id, title]) => {
              const result = results.find((item) => item.check === id)
              const failedEarlier = !result && results.some((item) => !item.ok)
              if (failedEarlier || !checking && !result) return null
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
        )}
        {!results && !access.enabled && !checked && (
            <p className="m-0 max-w-[60ch] text-(length:--fs-xs) text-(--fg2)">
              {access.candidate
                ? "Check the detected address before opening the sign-in page and MCP endpoint to the internet."
                : access.catch_all
                  ? "Your proxy forwards any host name, so the address isn't suggested. Type your own; it is checked before anything opens."
                  : "Put an HTTPS proxy or Tailscale Funnel in front of this gateway and open its address once; it appears here."}
            </p>
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
