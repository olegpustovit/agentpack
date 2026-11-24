import { useEffect, useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { Announcement } from "@/components/kit/feedback"
import { Field, FieldError, FieldGrid, FieldLabel } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { statusQuery } from "@/features/status/queries"
import { ApiError, api } from "@/lib/api"
import type { Status } from "@/lib/api-types"
import { AuthCard } from "./auth-card"
import { loginErrors, type LoginErrors, type LoginFields } from "./model"
import { consentPath, returnToLabel, safeReturnTo } from "./return-to"

const joinCheckTimeout = 60_000

function JoinProgress({ status }: { status: UseQueryResult<Status> }) {
  const [attempt, setAttempt] = useState(0)
  const [timedOut, setTimedOut] = useState(false)
  useEffect(() => {
    const timer = window.setTimeout(() => setTimedOut(true), joinCheckTimeout)
    return () => window.clearTimeout(timer)
  }, [attempt])

  const phase = status.data?.join_phase
  const node = status.data?.node_name
  const title = timedOut ? "Join check timed out" : phase === "waiting_for_accounts" ? "Waiting for accounts" : phase === "waiting_for_approval" ? "Waiting for approval" : "Joining your mesh"
  const detail = phase === "waiting_for_accounts"
    ? "The existing gateway approved this machine. Its accounts are syncing."
    : "Approve this machine in the existing gateway’s Replication settings."
  const checkedAt = new Date(status.dataUpdatedAt)

  return (
    <>
      <div className="flex flex-col gap-(--s2)">
        <h1 className="text-(length:--fs-lg) font-semibold">{title}</h1>
        {node && <p className="text-(--fg2)"><bdi>{node}</bdi> · this machine</p>}
      </div>
      <p>{detail}</p>
      {timedOut && <p role="status">The join is still pending. Retry to check its progress.</p>}
      {status.isError && <FieldError>Couldn’t check the gateway’s join status.</FieldError>}
      <p className="text-(length:--fs-sm) text-(--fg2)">Last checked <time dateTime={checkedAt.toISOString()}>{checkedAt.toLocaleTimeString()}</time></p>
      {(timedOut || status.isError) && (
        <Button type="button" disabled={!timedOut && status.isFetching} onClick={() => {
          setTimedOut(false)
          setAttempt((count) => count + 1)
          void status.refetch()
        }}>
          Retry
        </Button>
      )}
    </>
  )
}

export function LoginView({ returnTo }: { returnTo?: string }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const status = useQuery({ ...statusQuery, refetchInterval: (query) => query.state.data?.joining ? 1000 : false })
  const [fields, setFields] = useState<LoginFields>({ email: "", password: "", confirm: "", currentPassword: "" })
  const [errors, setErrors] = useState<LoginErrors>({})
  const claim = status.data?.claimed === false
  const legacy = claim && status.data?.legacy === true
  const next = safeReturnTo(returnTo)
  const login = useMutation({
    mutationFn: () => api(claim ? "/api/claim" : "/api/login", {
      body: { email: fields.email.trim(), ...(legacy ? { current_password: fields.currentPassword } : { password: fields.password }) },
    }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: statusQuery.queryKey })
      if (next?.startsWith(consentPath)) window.location.replace(next)
      else if (next) await navigate({ href: next, replace: true })
      else await navigate({ to: "/sessions", replace: true })
    },
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!status.data || status.data.joining || login.isPending) return
    const next = loginErrors(status.data, fields)
    setErrors(next)
    if (!Object.keys(next).length) login.mutate()
  }

  function change(key: keyof LoginFields, value: string) {
    setFields({ ...fields, [key]: value })
    setErrors({ ...errors, [key]: undefined })
    login.reset()
  }

  const failure = login.error instanceof ApiError ? login.error.message : login.error && "Couldn't reach the gateway. Check the connection and try again."
  const serverField = claim && failure?.includes("email") ? "email" : legacy ? "currentPassword" : "password"
  const visible = { ...errors, ...(failure ? { [serverField]: failure } : {}) }
  const labels = { email: legacy ? "Email for the owner account" : "Email", password: "Password", confirm: "Confirm password", currentPassword: "Current gateway password" }
  const keys: (keyof LoginFields)[] = legacy ? ["email", "currentPassword"] : claim ? ["email", "password", "confirm"] : ["email", "password"]
  const node = status.data?.node_name
  const ready = status.data?.join_phase === "ready"

  if (status.data?.joining) {
    return <AuthCard><JoinProgress key={status.data.join_phase} status={status} /></AuthCard>
  }

  return (
    <AuthCard>
      <form noValidate onSubmit={submit} className="flex flex-col gap-(--s5)">
        <div className="flex flex-col gap-(--s2)">
          <h1 className="text-(length:--fs-lg) font-semibold">{claim ? <>Claim {node ? <bdi>{node}</bdi> : "your gateway"}</> : ready ? "Ready to sign in" : "Sign in to Agentpack"}</h1>
          {claim && node && <p className="text-(length:--fs-sm) text-(--fg2)">This machine</p>}
          <p className="text-(--fg2)">{claim ? "Create the first owner account for this gateway." : ready ? "Use your existing Agentpack account on this mesh." : "Use your email and password to open your sessions."}</p>
          {claim && (
            <p id="claim-password-requirement" className="text-(length:--fs-sm) text-(--fg2)">
              {legacy ? "Use the current gateway password (12 to 72 bytes). It will become the password for your owner account." : "Use a password that is 12 to 72 bytes long."}
            </p>
          )}
        </div>
        {next && (
          <dl className="grid gap-(--s1) rounded-(--r-row) bg-(--track) px-(--s4) py-(--s3)">
            <dt className="text-(length:--fs-sm) text-(--fg2)">{claim ? "After claiming" : "After sign-in"}</dt>
            <dd className="font-semibold"><bdi>{returnToLabel(next)}</bdi></dd>
          </dl>
        )}
        {status.isError ? <><FieldError>Couldn’t check gateway status.</FieldError><Button type="button" disabled={status.isFetching} onClick={() => void status.refetch()}>Retry</Button></> : !status.data ? <Announcement>Connecting to gateway…</Announcement> : <>
          <FieldGrid>
            {keys.map((key) => {
              const help = claim && key === (legacy ? "currentPassword" : "password") ? "claim-password-requirement" : undefined
              const error = visible[key] ? `login-${key}-error` : undefined
              return (
                <Field key={key} className="flex-col items-stretch gap-(--s2)" data-invalid={visible[key] ? "true" : undefined}>
                  <FieldLabel className="w-auto text-left" htmlFor={`login-${key}`}>{labels[key]}</FieldLabel>
                  <Input id={`login-${key}`} type={key === "email" ? "email" : "password"} value={fields[key]} onChange={(event) => change(key, event.target.value)} autoComplete={key === "email" ? "username" : key === "currentPassword" || !claim ? "current-password" : "new-password"} disabled={login.isPending} aria-invalid={!!visible[key]} aria-describedby={[help, error].filter(Boolean).join(" ") || undefined} />
                  {error && <FieldError id={error}>{visible[key]}</FieldError>}
                </Field>
              )
            })}
          </FieldGrid>
          <Button type="submit" variant="primary" disabled={login.isPending}>{login.isPending ? claim ? "Claiming…" : "Signing in…" : claim ? "Claim gateway" : "Sign in"}</Button>
        </>}
      </form>
    </AuthCard>
  )
}
