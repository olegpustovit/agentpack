import { useId, useState } from "react"
import { ExternalLink, Lock } from "lucide-react"
import { Kbd } from "@/components/kit/feedback"
import { FieldError } from "@/components/kit/forms"
import { Button, buttonVariants } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api } from "@/lib/api"
import { useIntent } from "@/lib/intent"
import type { ElicitationField, ElicitationPayload, PermissionPayload, ThreadItem } from "./model"
import { conversationErrorMessage, conversationNeedsUpdate } from "./queries"

const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad)/.test(navigator.platform)

// Answer cards for permission and elicitation requests. The sessions home renders them too,
// so this file must stay free of markdown, diff and highlight imports (check-size.ts enforces it).
type RequestCardProps = {
  item: ThreadItem
  machine: string
  sessionId: string
  readOnly?: boolean
  onAnswer?: (active: boolean) => void
}

function RequestFailure({ machine, action, error, retry }: { machine: string; action: string; error: Error; retry?: () => void }) {
  return (
    <div className="mt-(--s3) flex flex-wrap items-center gap-(--s2)">
      <FieldError>{conversationErrorMessage(machine, action, error)}</FieldError>
      {retry && !conversationNeedsUpdate(error) && <Button type="button" size="sm" onClick={retry}>Retry</Button>}
    </div>
  )
}

export function ElicitationCard({ item, machine, sessionId, readOnly, onAnswer }: RequestCardProps) {
  const ask = item.data as ElicitationPayload & { optionIds?: string[]; allowMultiple?: boolean }
  const [selected, setSelected] = useState<string[]>([])
  const [retryAnswer, setRetryAnswer] = useState<() => void>()
  const answer = useIntent({
    request: (value: string, idempotencyKey) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/elicitation`, {
        machine,
        method: "POST",
        idempotencyKey,
        body: { elicitation_id: ask.elicitationId, value },
      }),
    apply: () => {
      onAnswer?.(true)
      return () => onAnswer?.(false)
    },
    failed: (_error, _value, retry) => setRetryAnswer(() => retry),
  })
  const submitted = answer.isPending || answer.isSuccess ? answer.input : undefined
  const result = ask.result
  // Older workers logged the URL as the prompt.
  const link = ask.agentElicitationId ? ask.url ?? ask.prompt : undefined
  return (
    <div data-slot="elicitation-card" className="rounded-(--r-sheet) border-[0.5px] border-(--warn) bg-(--window) px-(--s5) pt-(--s5) pb-(--s4) shadow-(--shadow-card)">
      <p className="text-(length:--fs) font-semibold text-(--fg)">{ask.prompt ?? "Input needed"}</p>
      {ask.resolved ? <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">Answered: {answerSummary(ask, result?.value)}</p> : ask.inactive ? <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">No longer active</p> : submitted !== undefined ? (
        <p role="status" className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">
          {answer.isPending ? "Sending answer…" : `Answered: ${answerSummary(ask, submitted)}`}
        </p>
      ) : readOnly ? (
        <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">Saved request · read-only</p>
      ) : ask.fields?.length ? (
        <ElicitationForm fields={ask.fields} disabled={!ask.elicitationId || answer.isPending} onSubmit={(value) => answer.mutate(value)} />
      ) : link !== undefined ? (
        <div className="mt-(--s3) flex flex-wrap items-center gap-(--s2)">
          {/^https?:\/\//i.test(link) && (
            <a href={link} target="_blank" rel="noreferrer noopener" className={buttonVariants({ size: "sm", variant: "primary" })}>
              <ExternalLink aria-hidden />
              Open {new URL(link).host}
            </a>
          )}
          <Button type="button" size="sm" disabled={!ask.elicitationId || answer.isPending} onClick={() => answer.mutate("done")}>Done</Button>
          <Button type="button" size="sm" disabled={!ask.elicitationId || answer.isPending} onClick={() => answer.mutate("decline")}>Decline</Button>
        </div>
      ) : (
        <div
          className="mt-(--s3) flex flex-wrap gap-(--s2)"
          onKeyDown={ask.allowMultiple ? (event) => {
            if (event.nativeEvent.isComposing || event.keyCode === 229) return
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey) && ask.elicitationId && !answer.isPending && selected.length > 0) {
              event.preventDefault()
              if (!event.repeat) answer.mutate(JSON.stringify(selected))
            }
          } : undefined}
        >
          {(ask.choices ?? []).map((choice, index) => {
            const value = ask.optionIds?.[index] ?? choice
            return (
              <Button
                key={value}
                type="button"
                size="sm"
                variant={ask.allowMultiple && selected.includes(value) ? "primary" : "default"}
                aria-pressed={ask.allowMultiple ? selected.includes(value) : undefined}
                disabled={!ask.elicitationId || answer.isPending}
                onClick={() => {
                  if (!ask.allowMultiple) answer.mutate(value)
                  else setSelected((current) => current.includes(value) ? current.filter((item) => item !== value) : [...current, value])
                }}
              >
                {choice}
              </Button>
            )
          })}
          {ask.allowMultiple && (
            <Button
              type="button"
              size="sm"
              variant="default"
              disabled={!ask.elicitationId || answer.isPending || selected.length === 0}
              aria-keyshortcuts="Meta+Enter Control+Enter"
              onClick={() => answer.mutate(JSON.stringify(selected))}
            >
              Submit
              <Kbd aria-hidden className="text-current opacity-70">{isMac ? "⌘↵" : "Ctrl+↵"}</Kbd>
            </Button>
          )}
        </div>
      )}
      {answer.error && <RequestFailure machine={machine} action="answer a question" error={answer.error} retry={retryAnswer} />}
    </div>
  )
}

function ElicitationForm({ fields, disabled, onSubmit }: { fields: ElicitationField[]; disabled: boolean; onSubmit: (value: string) => void }) {
  const [values, setValues] = useState<Record<string, string | string[]>>({})
  // One question with options and nothing else to fill answers on click.
  const quick = fields.length === 1 && !fields[0]!.multiple && (fields[0]!.options?.length ?? 0) > 0
  return (
    <form
      className="mt-(--s3) flex flex-col gap-(--s4)"
      onSubmit={(event) => {
        event.preventDefault()
        if (!quick && !disabled) onSubmit(JSON.stringify(values))
      }}
      onKeyDown={(event) => {
        if (event.nativeEvent.isComposing || event.keyCode === 229) return
        if (!quick && event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
          event.preventDefault()
          if (!disabled && !event.repeat) event.currentTarget.requestSubmit()
        }
      }}
    >
      {fields.map((field) => {
        const current = values[field.key]
        return (
          <div key={field.key} className="flex flex-col gap-(--s2)">
            {(field.title || field.description) && (
              <p className="text-(length:--fs-sm) text-(--fg)">
                {field.title && <span className="font-semibold">{field.title}</span>}
                {field.title && field.description && " · "}
                {field.description && <span className="text-(--fg2)">{field.description}</span>}
              </p>
            )}
            {field.options?.length ? (
              <div className="flex flex-wrap gap-(--s2)">
                {field.options.map((option) => {
                  const picked = Array.isArray(current) ? current.includes(option.value) : current === option.value
                  return (
                    <Button
                      key={option.value}
                      type="button"
                      size="sm"
                      variant={picked ? "primary" : "default"}
                      aria-pressed={quick ? undefined : picked}
                      title={option.description}
                      disabled={disabled}
                      onClick={() => {
                        if (quick) return onSubmit(JSON.stringify({ [field.key]: option.value }))
                        setValues((all) => {
                          const prior = all[field.key]
                          if (!field.multiple) return { ...all, [field.key]: prior === option.value ? "" : option.value }
                          const list = Array.isArray(prior) ? prior : []
                          return { ...all, [field.key]: list.includes(option.value) ? list.filter((item) => item !== option.value) : [...list, option.value] }
                        })
                      }}
                    >
                      {option.label}
                    </Button>
                  )
                })}
              </div>
            ) : (
              <Input
                aria-label={field.title || field.description || field.key}
                placeholder={field.title === "Other" ? "Your own answer (optional)" : undefined}
                value={typeof current === "string" ? current : ""}
                disabled={disabled}
                onChange={(event) => {
                  const text = event.currentTarget.value
                  setValues((all) => ({ ...all, [field.key]: text }))
                }}
              />
            )}
          </div>
        )
      })}
      {!quick && (
        <div>
          <Button type="submit" size="sm" variant="primary" disabled={disabled} aria-keyshortcuts="Meta+Enter Control+Enter">
            Submit
            <Kbd aria-hidden className="text-current opacity-70">{isMac ? "⌘↵" : "Ctrl+↵"}</Kbd>
          </Button>
        </div>
      )}
    </form>
  )
}

function answerSummary(ask: ElicitationPayload, value?: string): string {
  if (!value) return "—"
  if (ask.agentElicitationId) return value === "decline" ? "Declined" : "Done"
  if (!ask.fields?.length) return value
  try {
    const answers = JSON.parse(value) as Record<string, unknown>
    const parts = ask.fields.flatMap((field) => {
      const picked = ([] as unknown[]).concat(answers[field.key] ?? []).map(String).filter(Boolean)
      return picked.map((item) => field.options?.find((option) => option.value === item)?.label ?? item)
    })
    return parts.length ? parts.join(", ") : "skipped"
  } catch {
    return value
  }
}

function permissionVerb(tool?: string): string {
  switch (tool) {
    case "execute": case "terminal": case "Bash": return "run this command"
    case "edit": return "edit this file"
    case "read": return "read this file"
    case "fetch": return "fetch this page"
    default: return tool ? `use ${tool}` : "continue"
  }
}

type PermissionOption = NonNullable<PermissionPayload["options"]>[number]

function optionKind(option: PermissionOption): string | undefined {
  if (option.kind) return option.kind
  const label = option.label.toLowerCase()
  if (option.id === "allow-always" || label === "always allow") return "allow_always"
  if (option.id === "reject-always") return "reject_always"
  if (option.id === "allow-once" || label === "yes" || label === "allow once") return "allow_once"
  if (option.id.startsWith("reject") || label === "no" || label === "deny" || label === "reject") return "reject_once"
}

function optionLabel(option: PermissionOption): string {
  // Current adapters describe the actual effect in the option name. Older logs used Yes and No.
  if (option.kind) return option.label
  if (option.label === "Yes") return optionKind(option) === "allow_always" ? "Always allow" : "Allow once"
  return option.label === "No" ? "Reject" : option.label
}

// ACP leaves the scope, lifetime, and storage of remembered choices to the original agent.
function remembersPermission(kind?: string) {
  return kind === "allow_always" || kind === "reject_always"
}

function PermissionRecovery() {
  return (
    <details className="min-w-0 pl-[calc(var(--s3)+14px)] text-(length:--fs-xs) text-(--fg2)">
      <summary className="cursor-pointer text-(--accent)">Review remembered permission</summary>
      <div className="mt-(--s2) flex flex-col gap-(--s2)">
        <p>The choice is managed by the agent that asked. Its answer above describes the scope it offered.</p>
        <p>To remove a saved Claude Code rule, run <code>/permissions</code> in Claude Code from the same project on the machine that ran the chat. <a href="https://code.claude.com/docs/en/permissions#manage-permissions" target="_blank" rel="noreferrer" className="text-(--accent) underline">Claude Code permissions</a></p>
        <p>For a Codex command-prefix rule, review the rules in the Codex configuration used for the chat. <a href="https://developers.openai.com/codex/rules" target="_blank" rel="noreferrer" className="text-(--accent) underline">Codex command rules</a></p>
      </div>
    </details>
  )
}

export function PermissionCard({
  item,
  machine,
  sessionId,
  readOnly,
  agent,
  onAnswer,
}: RequestCardProps & { agent?: string }) {
  const perm = item.data as PermissionPayload
  const scopeId = useId()
  const [retryAnswer, setRetryAnswer] = useState<() => void>()
  const answer = useIntent({
    request: (optionId: string, idempotencyKey) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/permission`, {
        machine,
        method: "POST",
        idempotencyKey,
        body: { request_id: perm.requestId, option_id: optionId },
      }),
    apply: () => {
      onAnswer?.(true)
      return () => onAnswer?.(false)
    },
    failed: (_error, _value, retry) => setRetryAnswer(() => retry),
  })
  const options = perm.options ?? []
  const command = perm.summary && (
    <pre data-slot="permission-command" className="my-(--s4) max-h-40 overflow-auto rounded-(--r-ctl) bg-(--code) px-(--s4) py-(--s3) font-(family-name:--mono) text-(length:--fs-sm) text-(--fg) whitespace-pre-wrap break-words">{perm.summary}</pre>
  )
  const submitted = answer.isPending || answer.isSuccess ? answer.input : undefined
  if (perm.resolved || perm.inactive || submitted !== undefined) {
    const id = perm.resolved ? perm.result?.optionId : submitted
    const chosen = options.find((option) => option.id === id)
    const answerLabel = chosen ? optionLabel(chosen) : id
    const status = perm.inactive && !perm.resolved ? "No longer active"
      : answer.isPending && !perm.resolved ? "Sending answer…"
      : `Answered: ${answerLabel ?? "—"}`
    const remembered = chosen && (perm.resolved || answer.isSuccess) && remembersPermission(optionKind(chosen))
    return (
      <div data-slot="permission-card">
        <p className="flex min-h-(--row-h) items-center gap-(--s3) text-(length:--fs-sm) text-(--fg3)">
          <Lock aria-hidden className="size-3.5 shrink-0" />
          <span role="status" className="min-w-0 [overflow-wrap:anywhere]">{status}{perm.summary ? ` · ${perm.summary}` : ""}</span>
        </p>
        {remembered && <PermissionRecovery />}
      </div>
    )
  }
  return (
    <div
      data-slot="permission-card"
      role="group"
      aria-label={`Permission request: ${perm.tool ?? "Permission"}`}
      tabIndex={-1}
      className="rounded-(--r-sheet) border-[0.5px] border-(--warn) bg-(--window) px-(--s5) pt-(--s5) pb-(--s4) shadow-(--shadow-card) outline-none focus-visible:ring-2 focus-visible:ring-(--accent-soft)"
    >
      <div className="flex items-start gap-(--s4)">
        <span aria-hidden className="grid size-7 shrink-0 place-items-center rounded-[7px] bg-[color-mix(in_oklch,var(--warn)_20%,transparent)] text-(--warn-word)">
          <Lock className="size-4" />
        </span>
        <p className="pt-(--s1) text-(length:--fs) font-semibold text-(--fg)">Allow {agent || "the agent"} to {permissionVerb(perm.tool)}?</p>
      </div>
      {perm.summary && perm.summary.length > 180 ? (
        <details className="my-(--s3) text-(length:--fs-sm) text-(--fg2)">
          <summary className="cursor-pointer">Review request details</summary>
          {command}
        </details>
      ) : command}
      {readOnly ? (
        <p className="text-(length:--fs-sm) text-(--fg2)">Saved request · read-only</p>
      ) : (
        <div className="flex flex-wrap justify-end gap-(--s2)">
          {options.toReversed().map((option, index) => {
            const kind = optionKind(option)
            const remembered = remembersPermission(kind)
            const id = `${scopeId}-${index}`
            return (
              <div key={option.id} className="flex flex-col items-end gap-(--s1)">
                <Button
                  type="button" size="sm" variant={kind === "allow_once" ? "primary" : "default"}
                  aria-describedby={remembered ? id : undefined} disabled={!perm.requestId} onClick={() => answer.mutate(option.id)}
                >
                  {optionLabel(option)}
                </Button>
                {remembered && <p id={id} className="text-(length:--fs-xs) text-(--fg2)">Agent remembers this choice</p>}
              </div>
            )
          })}
        </div>
      )}
      {answer.error && <RequestFailure machine={machine} action="answer a permission request" error={answer.error} retry={retryAnswer} />}
    </div>
  )
}
