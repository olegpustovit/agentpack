import { useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { FieldError } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { statusQuery } from "@/features/status/queries"
import { ApiError, api } from "@/lib/api"
import { safeReturnTo } from "./return-to"

// Centered 340 px card. Before the root account exists, it asks for the setup token and creates it.
export function LoginView({ returnTo }: { returnTo?: string }) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const setup = useQuery(statusQuery).data?.setup_required === true
  const [password, setPassword] = useState("")
  const [token, setToken] = useState("")
  const login = useMutation({
    mutationFn: () => api(setup ? "/api/setup" : "/api/login", { body: setup ? { password, token } : { password } }),
    onSuccess: async () => {
      // The route guard reads the cached status, so refresh it before the navigation below checks it.
      await queryClient.invalidateQueries({ queryKey: statusQuery.queryKey })
      const next = safeReturnTo(returnTo)
      if (next) window.location.assign(next)
      else void navigate({ to: "/sessions" })
    },
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    login.mutate()
  }

  const failure =
    login.error instanceof ApiError ? login.error.message : login.error && "Couldn't reach the gateway. Check the connection and try again."

  return (
    <main className="grid min-h-dvh place-items-center bg-(--window) p-(--s6) text-(--fs) text-(--fg)">
      <form
        onSubmit={submit}
        className="flex w-85 max-w-full flex-col gap-(--s5) rounded-(--r-sheet) bg-(--chrome) p-(--s7) shadow-(--shadow-pop)"
      >
        <div className="flex flex-col gap-(--s2)">
          <p className="text-(--fs-xs) font-medium text-(--fg3)">Agentpack gateway</p>
          <h1 className="text-(--fs) font-semibold">{setup ? "Set up your gateway" : "Welcome back"}</h1>
          <p className="text-(--fs-sm) text-(--fg2)">
            {setup ? "Create the root account for this gateway." : "Sign in to your sessions."}
          </p>
        </div>
        {setup && (
          <div className="flex flex-col gap-(--s2)">
            <label htmlFor="setup-token" className="text-(--fs-sm) text-(--fg2)">
              Setup token
            </label>
            <Input
              id="setup-token"
              value={token}
              onChange={(event) => setToken(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              required
            />
            <p className="text-(--fs-xs) text-(--fg3)">
              Run <code>agentpack gateway setup</code> on the server to get this token.
            </p>
          </div>
        )}
        <div className="flex flex-col gap-(--s2)">
          <label htmlFor="password" className="text-(--fs-sm) text-(--fg2)">
            Root account password
          </label>
          <Input
            id="password"
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            autoComplete={setup ? "new-password" : "current-password"}
            required
          />
        </div>
        <Button type="submit" disabled={login.isPending}>
          {login.isPending ? (setup ? "Creating account…" : "Signing in…") : setup ? "Create root account" : "Sign in"}
        </Button>
        {failure && <FieldError>{failure}</FieldError>}
      </form>
    </main>
  )
}
