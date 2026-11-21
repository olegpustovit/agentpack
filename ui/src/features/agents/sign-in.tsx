import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Copy, ExternalLink, Lock } from "lucide-react"
import { Button, buttonVariants } from "@/components/ui/button"
import { Spinner } from "@/components/kit/feedback"
import { FieldError } from "@/components/kit/forms"
import { api, routeMissing } from "@/lib/api"
import { harnessName } from "@/lib/format"
import { useCopied } from "@/lib/hud"
import { useMachines } from "@/features/machines/queries"
import { agentPath, agentsQuery, readStep, type AgentMethod, type LoginStep } from "./queries"

const pasteHint = {
  code: "Paste the code the page shows after you approve.",
  url: "Paste the address the page lands on after you approve. An error page there is fine.",
}

function failure(error: unknown, action: string): string {
  if (routeMissing(error)) return `Update Agentpack on this machine to ${action}.`
  return error instanceof Error ? error.message : String(error)
}

// Signs an agent in on a machine through the agent's own login: the machine prints a link (and a
// code), the person opens it here, and pastes back what the page gives them when asked.
export function SignIn({
  machine,
  machineName,
  harness,
  methods,
  onSignedIn,
}: {
  machine: string
  machineName: string
  harness: string
  methods: AgentMethod[]
  onSignedIn?: () => void
}) {
  const queryClient = useQueryClient()
  const [step, setStep] = useState<LoginStep>()
  const [pasted, setPasted] = useState("")
  const name = harnessName(harness)
  const start = useMutation({
    mutationFn: async (method: string) => readStep(await api<unknown>(agentPath(harness), { machine, body: { method } })),
    onSuccess: setStep,
  })
  // The sign-in runs on the machine, possibly a peer whose events don't reach this browser, so
  // its state is polled while the person is signing in.
  const status = useQuery({
    queryKey: ["agent-login", machine, harness, step?.id],
    queryFn: async () => {
      const next = readStep(await api<unknown>(`${agentPath(harness)}/${encodeURIComponent(step!.id)}`, { machine }))
      if (next.state === "done") {
        void queryClient.invalidateQueries({ queryKey: agentsQuery(machine).queryKey })
        onSignedIn?.()
      }
      return next
    },
    enabled: step?.state === "waiting",
    refetchInterval: (query) => (!query.state.error && (query.state.data?.state === "waiting" || !query.state.data) ? 2000 : false),
    retry: false,
  })
  const submit = useMutation({
    mutationFn: async (input: string) => readStep(await api<unknown>(`${agentPath(harness)}/${encodeURIComponent(step!.id)}`, { machine, body: { input } })),
    onSuccess: () => setPasted(""),
  })
  // A gateway restart or a newer sign-in ends this one; its machine no longer knows it.
  const current: LoginStep | undefined = status.error && step ? { ...step, state: "failed", error: routeMissing(status.error) ? failure(status.error, `sign ${name} in`) : "it ended on the machine" } : status.data ?? step
  const cancel = () => {
    if (step?.state === "waiting" && current?.state === "waiting") {
      void api(`${agentPath(harness)}/${encodeURIComponent(step.id)}`, { machine, method: "DELETE" }).catch(() => {})
    }
    setStep(undefined)
    start.reset()
    submit.reset()
  }

  if (current?.state === "done") {
    return (
      <p role="status" className="flex items-center gap-(--s2) text-(length:--fs-sm) text-(--fg2)">
        <Check aria-hidden className="size-4 text-(--ok-word)" />
        Signed in to {name} on {machineName}
      </p>
    )
  }
  if (!current) {
    return (
      <div className="flex flex-col gap-(--s3)">
        <div className="flex flex-wrap justify-end gap-(--s2)">
          {methods.map((method, index) => (
            <Button key={method.id} type="button" size="sm" variant={index === 0 ? "primary" : "default"} disabled={start.isPending} onClick={() => start.mutate(method.id)}>
              {start.isPending && start.variables === method.id ? <Spinner /> : null}
              {methods.length === 1 ? `Sign in with ${method.label}` : method.label}
            </Button>
          ))}
        </div>
        {start.error && <FieldError>{failure(start.error, `sign ${name} in`)}</FieldError>}
      </div>
    )
  }
  if (current.state === "failed") {
    return (
      <div className="flex flex-col gap-(--s3)">
        <FieldError>{current.error ? `Sign-in failed: ${current.error}` : "Sign-in failed."}</FieldError>
        <div className="flex justify-end">
          <Button type="button" size="sm" onClick={cancel}>Try again</Button>
        </div>
      </div>
    )
  }
  const host = current.url ? new URL(current.url).host : ""
  return (
    <div className="flex flex-col gap-(--s4)">
      <ol className="grid gap-(--s4) text-(length:--fs)">
        <li className="grid grid-cols-[20px_1fr] items-start gap-(--s3)">
          <span aria-hidden className="grid size-5 place-items-center rounded-full bg-(--track) text-(length:--fs-xs) text-(--fg2) tabular-nums">1</span>
          <div className="flex min-w-0 flex-col items-start gap-(--s2)">
            <span>Open the sign-in page{host && <span className="text-(--fg3)"> · {host}</span>}</span>
            {current.url && (
              <a href={current.url} target="_blank" rel="noreferrer noopener" className={buttonVariants({ size: "sm" })}>
                <ExternalLink aria-hidden />
                Open sign-in page
              </a>
            )}
          </div>
        </li>
        {current.code && (
          <li className="grid grid-cols-[20px_1fr] items-start gap-(--s3)">
            <span aria-hidden className="grid size-5 place-items-center rounded-full bg-(--track) text-(length:--fs-xs) text-(--fg2) tabular-nums">2</span>
            <div className="flex min-w-0 flex-col items-start gap-(--s2)">
              <span>Enter this code there</span>
              <span className="flex items-center gap-(--s3)">
                <span data-slot="login-code" className="text-[22px] font-semibold tracking-[.12em] tabular-nums select-all">{current.code}</span>
                <CopyCodeButton code={current.code} />
              </span>
            </div>
          </li>
        )}
        {current.input && (
          <li className="grid grid-cols-[20px_1fr] items-start gap-(--s3)">
            <span aria-hidden className="grid size-5 place-items-center rounded-full bg-(--track) text-(length:--fs-xs) text-(--fg2) tabular-nums">2</span>
            <form
              className="flex min-w-0 flex-col gap-(--s2)"
              onSubmit={(event) => {
                event.preventDefault()
                if (pasted.trim()) submit.mutate(pasted.trim())
              }}
            >
              <label htmlFor={`signin-${harness}`}>{pasteHint[current.input]}</label>
              <div className="flex gap-(--s2)">
                <input
                  id={`signin-${harness}`}
                  value={pasted}
                  onChange={(event) => setPasted(event.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder={current.input === "url" ? "https://…" : "Code"}
                  className="h-(--ctl-h) min-w-0 flex-1 rounded-(--r-ctl) bg-(--track) px-(--s4) text-(length:--fs) text-(--fg) outline-none placeholder:text-(--fg3) focus:ring-2 focus:ring-(--accent-soft)"
                />
                <Button type="submit" size="sm" variant="primary" disabled={!pasted.trim() || submit.isPending}>Continue</Button>
              </div>
              {submit.error && <FieldError>{failure(submit.error, `sign ${name} in`)}</FieldError>}
            </form>
          </li>
        )}
      </ol>
      <div className="flex items-center gap-(--s3) text-(length:--fs-sm) text-(--fg2)">
        <Spinner />
        <span className="flex-1">Waiting for the sign-in on {machineName}</span>
        <Button type="button" size="sm" onClick={cancel}>Cancel</Button>
      </div>
    </div>
  )
}

// A chat whose agent isn't signed in: the start failure becomes this card, and signing in starts the chat.
function CopyCodeButton({ code }: { code: string }) {
  const [copied, copy] = useCopied()
  return (
    <Button type="button" size="sm" variant="ghost" onClick={() => copy(code)}>
      {copied ? <Check aria-hidden /> : <Copy aria-hidden />}
      {copied ? "Copied" : "Copy code"}
    </Button>
  )
}

export function SignInCard({ machine, harness, methods, reason, onSignedIn }: { machine: string; harness: string; methods?: AgentMethod[]; reason?: string; onSignedIn: () => void }) {
  const machineName = useMachines().find((item) => item.id === machine)?.name ?? machine
  // Older gateways didn't put the methods in the notice; ask the machine then.
  const agents = useQuery({ ...agentsQuery(machine), enabled: !methods?.length })
  const known = methods?.length ? methods : agents.data?.find((item) => item.harness === harness)?.methods
  const name = harnessName(harness)
  return (
    <div
      data-slot="signin-card"
      role="group"
      aria-label={`Sign in to ${name}`}
      className="rounded-(--r-sheet) border-[0.5px] border-(--warn) bg-(--window) px-(--s5) pt-(--s5) pb-(--s4) shadow-(--shadow-card)"
    >
      <div className="flex items-start gap-(--s4)">
        <span aria-hidden className="grid size-7 shrink-0 place-items-center rounded-[7px] bg-[color-mix(in_oklch,var(--warn)_20%,transparent)] text-(--warn-word)">
          <Lock className="size-4" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="pt-(--s1) text-(length:--fs) font-semibold text-(--fg)">{reason || `${name} isn't signed in on ${machineName}.`}</p>
          <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">Sign in to continue this chat.</p>
          <div className="mt-(--s4)">
            {known?.length ? (
              <SignIn machine={machine} machineName={machineName} harness={harness} methods={known} onSignedIn={onSignedIn} />
            ) : agents.isPending ? (
              <Spinner />
            ) : (
              <p className="text-(length:--fs-sm) text-(--fg2)">{routeMissing(agents.error) ? `Update Agentpack on ${machineName} to sign in from here.` : `Run ${name} in a terminal on ${machineName} to sign in.`}</p>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
