import { useState } from "react"
import { ExternalLink, Lock } from "lucide-react"
import { Button, buttonVariants } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api } from "@/lib/api"
import { hud } from "@/lib/hud"
import { useIntent } from "@/lib/intent"
import type { ElicitationField, ElicitationPayload, PermissionPayload, ThreadItem } from "./model"
import { conversationErrorMessage, conversationNeedsUpdate } from "./queries"

// Answer cards for permission and elicitation requests. The sessions home renders them too,
// so this file must stay free of markdown, diff and highlight imports (check-size.ts enforces it).
export function ElicitationCard({ item, machine, sessionId }: { item: ThreadItem; machine: string; sessionId: string }) {
  const ask = item.data as ElicitationPayload & { optionIds?: string[]; allowMultiple?: boolean }
  const [selected, setSelected] = useState<string[]>([])
  const answer = useIntent({
    request: (value: string, idempotencyKey) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/elicitation`, {
        machine,
        method: "POST",
        idempotencyKey,
        body: { elicitation_id: ask.elicitationId, value },
      }),
    failed: (error, _value, retry) =>
      hud.error(conversationErrorMessage(machine, "answer a question", error), conversationNeedsUpdate(error) ? undefined : retry),
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
        <div className="mt-(--s3) flex flex-wrap gap-(--s2)">
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
              onClick={() => answer.mutate(JSON.stringify(selected))}
            >
              Submit
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

function ElicitationForm({ fields, disabled, onSubmit }: { fields: ElicitationField[]; disabled: boolean; onSubmit: (value: string) => void }) {
  const [values, setValues] = useState<Record<string, string | string[]>>({})
  // One question with options and nothing else to fill answers on click.
  const quick = fields.length === 1 && !fields[0]!.multiple && (fields[0]!.options?.length ?? 0) > 0
  return (
    <div className="mt-(--s3) flex flex-col gap-(--s4)">
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
          <Button type="button" size="sm" variant="primary" disabled={disabled} onClick={() => onSubmit(JSON.stringify(values))}>
            Submit
          </Button>
        </div>
      )}
    </div>
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

// Adapters word options as "Yes"/"No"; the card uses the prototype's verbs and keeps specific labels.
function optionLabel(option: { id: string; label: string }): string {
  if (option.id.startsWith("reject") || option.label === "No") return "Reject"
  if (option.id === "allow-once" || option.label === "Yes") return "Allow once"
  if (option.id === "allow-always") return "Always allow"
  return option.label
}

export function PermissionCard({
  item,
  machine,
  sessionId,
  readOnly,
  agent,
}: {
  item: ThreadItem
  machine: string
  sessionId: string
  readOnly?: boolean
  agent?: string
}) {
  const perm = item.data as PermissionPayload
  const answer = useIntent({
    request: (optionId: string, idempotencyKey) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/permission`, {
        machine,
        method: "POST",
        idempotencyKey,
        body: { request_id: perm.requestId, option_id: optionId },
      }),
    failed: (error, _value, retry) =>
      hud.error(conversationErrorMessage(machine, "answer a permission request", error), conversationNeedsUpdate(error) ? undefined : retry),
  })
  const open = !readOnly && !perm.resolved && !perm.inactive
  const options = perm.options ?? []
  const submitted = answer.isPending || answer.isSuccess ? answer.input : undefined
  if (perm.resolved || perm.inactive || submitted !== undefined) {
    const id = perm.resolved ? perm.result?.optionId : submitted
    const chosen = options.find((option) => option.id === id)
    const answerLabel = chosen ? optionLabel(chosen) : id
    const status = perm.inactive && !perm.resolved ? "No longer active"
      : answer.isPending && !perm.resolved ? "Sending answer…"
      : `Answered: ${answerLabel ?? "—"}`
    return (
      <p data-slot="permission-card" className="flex min-h-(--row-h) items-center gap-(--s3) text-(length:--fs-sm) text-(--fg3)">
        <Lock aria-hidden className="size-3.5 shrink-0" />
        <span role="status" className="min-w-0 truncate">{status}{perm.summary ? ` · ${perm.summary}` : ""}</span>
      </p>
    )
  }
  return (
    <div
      data-slot="permission-card"
      role="group"
      aria-label={`Permission request: ${perm.tool ?? "Permission"}`}
      tabIndex={0}
      onKeyDown={(event) => {
        if (!open || !perm.requestId || answer.isPending || event.repeat || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
        if (event.target instanceof HTMLElement && event.target.closest("input, textarea, select, [contenteditable='true']")) return
        const option = event.key === "Enter" && event.target === event.currentTarget ? options[0] : /^[1-9]$/.test(event.key) ? options[Number(event.key) - 1] : undefined
        if (!option) return
        event.preventDefault()
        answer.mutate(option.id)
      }}
      className="rounded-(--r-sheet) border-[0.5px] border-(--warn) bg-(--window) px-(--s5) pt-(--s5) pb-(--s4) shadow-(--shadow-card) outline-none focus-visible:ring-2 focus-visible:ring-(--accent-soft)"
    >
      <div className="flex items-start gap-(--s4)">
        <span aria-hidden className="grid size-7 shrink-0 place-items-center rounded-[7px] bg-[color-mix(in_oklch,var(--warn)_20%,transparent)] text-(--warn-word)">
          <Lock className="size-4" />
        </span>
        <p className="pt-(--s1) text-(length:--fs) font-semibold text-(--fg)">Allow {agent || "the agent"} to {permissionVerb(perm.tool)}?</p>
      </div>
      {perm.summary && <pre data-slot="permission-command" className="my-(--s4) max-h-40 overflow-auto rounded-(--r-ctl) bg-(--code) px-(--s4) py-(--s3) font-(family-name:--mono) text-(length:--fs-sm) text-(--fg) whitespace-pre-wrap break-words">{perm.summary}</pre>}
      <div className="flex flex-wrap justify-end gap-(--s2)">
        {options.map((option, index) => ({ option, index })).reverse().map(({ option, index }) => (
          <Button key={option.id} type="button" size="sm" variant={index === 0 ? "primary" : "default"} title={index < 9 ? `Press ${index + 1}` : undefined} disabled={!perm.requestId || answer.isPending} onClick={() => answer.mutate(option.id)}>
            {optionLabel(option)}
            {index === 0 && <kbd aria-hidden className="font-(family-name:--mono) opacity-70">↩</kbd>}
          </Button>
        ))}
      </div>
    </div>
  )
}
