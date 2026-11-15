import { useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { Announcement } from "@/components/kit/feedback"
import { Field, FieldError, FieldGrid, FieldLabel } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { statusQuery } from "@/features/status/queries"
import { ApiError, api } from "@/lib/api"
import { loginErrors, type LoginErrors, type LoginFields } from "./model"
import { safeReturnTo } from "./return-to"

export function LoginView({ returnTo }: { returnTo?: string }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const status = useQuery({ ...statusQuery, refetchInterval: (query) => query.state.data?.joining ? 1000 : false })
  const [fields, setFields] = useState<LoginFields>({ email: "", password: "", confirm: "", currentPassword: "" })
  const [errors, setErrors] = useState<LoginErrors>({})
  const claim = status.data?.claimed === false
  const legacy = claim && status.data?.legacy === true
  const login = useMutation({
    mutationFn: () => api(claim ? "/api/claim" : "/api/login", {
      body: claim ? { email: fields.email.trim(), password: fields.password, ...(legacy ? { current_password: fields.currentPassword } : {}) } : { email: fields.email.trim(), password: fields.password },
    }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: statusQuery.queryKey })
      const next = safeReturnTo(returnTo)
      if (next) window.location.assign(next)
      else void navigate({ to: "/sessions" })
    },
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!status.data || status.data.joining) return
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
  const serverField = legacy && failure?.includes("current password") ? "currentPassword" : claim && failure?.includes("email") ? "email" : "password"
  const visible = { ...errors, ...(failure ? { [serverField]: failure } : {}) }
  const labels = { email: "Email", password: "Password", confirm: "Confirm password", currentPassword: "Current gateway password" }
  const keys: (keyof LoginFields)[] = ["email", ...(legacy ? ["currentPassword" as const] : []), "password", ...(claim ? ["confirm" as const] : [])]

  return (
    <main className="flex h-dvh overflow-y-auto bg-(--window) p-(--s6) text-(length:--fs) text-(--fg)">
      <form noValidate onSubmit={submit} className="m-auto flex w-85 max-w-full shrink-0 flex-col gap-(--s5) rounded-(--r-sheet) bg-(--chrome) p-(--s7) shadow-(--shadow-pop)">
        <div className="flex flex-col gap-(--s2)">
          <h1 className="text-(length:--fs) font-semibold">{status.data?.joining ? "Joining your mesh" : claim ? "Claim your gateway" : "Sign in to Agentpack"}</h1>
          <p className="text-(length:--fs-sm) text-(--fg2)">{status.data?.joining ? "Waiting for existing accounts. Approve this machine in the other gateway’s Replication settings." : claim ? legacy ? "Confirm the current gateway password to become its owner. Legacy claiming keeps that password." : "Create the first owner account for this gateway." : "Use your email and password to open your sessions."}</p>
        </div>
        {status.error ? <><FieldError>Couldn't reach the gateway: {status.error.message}</FieldError><Button type="button" onClick={() => void status.refetch()}>Retry</Button></> : !status.data ? <Announcement>Connecting to gateway…</Announcement> : status.data.joining ? <Announcement>Synchronizing accounts…</Announcement> : <>
          <FieldGrid>
            {keys.map((key) => (
              <Field key={key} className="flex-col items-stretch gap-(--s2)" data-invalid={visible[key] ? "true" : undefined}>
                <FieldLabel className="w-auto text-left" htmlFor={`login-${key}`}>{labels[key]}</FieldLabel>
                <Input id={`login-${key}`} type={key === "email" ? "email" : "password"} value={fields[key]} onChange={(event) => change(key, event.target.value)} autoComplete={key === "email" ? "username" : key === "currentPassword" || !claim ? "current-password" : "new-password"} disabled={login.isPending} aria-invalid={!!visible[key]} aria-describedby={visible[key] ? `login-${key}-error` : undefined} />
                {visible[key] && <FieldError id={`login-${key}-error`}>{visible[key]}</FieldError>}
              </Field>
            ))}
          </FieldGrid>
          <Button type="submit" disabled={login.isPending}>{login.isPending ? claim ? "Claiming…" : "Signing in…" : claim ? "Claim gateway" : "Sign in"}</Button>
        </>}
      </form>
    </main>
  )
}
