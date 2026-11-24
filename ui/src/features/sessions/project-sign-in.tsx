import { useEffect, useRef, useState } from "react"
import { FieldError } from "@/components/kit/forms"
import { Button, buttonVariants } from "@/components/ui/button"
import { startSignIn, waitForSignIn } from "@/features/settings/connections-model"
import { errorText } from "@/lib/api"

function providerURL(value: string) {
  const url = new URL(value)
  if ((url.protocol !== "http:" && url.protocol !== "https:") || url.username || url.password) {
    throw new Error("The provider returned an invalid sign-in link.")
  }
  return url.href
}

export function useProjectSignIn(refresh: () => Promise<unknown>) {
  const [pending, setPending] = useState<{ name: string; authorizeURL?: string }>()
  const [error, setError] = useState("")
  const active = useRef<AbortController | null>(null)
  useEffect(() => () => active.current?.abort(), [])

  async function connect(name: string, url: string, label: string, onConnected?: (id: string) => void) {
    const tab = window.open("about:blank", "_blank")
    if (tab) tab.opener = null
    const request = new AbortController()
    active.current = request
    setError("")
    setPending({ name })
    function openProvider(authorizeURL: string) {
      if (tab && !tab.closed) tab.location.href = authorizeURL
      else setPending({ name, authorizeURL })
    }
    try {
      const started = await startSignIn({ url, label })
      request.signal.throwIfAborted()
      openProvider(providerURL(started.authorize_url))
      const id = await waitForSignIn(started.login, request.signal)
      await refresh()
      notify(id)
    } catch (error) {
      if (!request.signal.aborted) setError(errorText(error))
    }
    tab?.close()
    if (!request.signal.aborted) setPending(undefined)

    function notify(id: string) {
      if (!request.signal.aborted) onConnected?.(id)
    }
  }

  const cancel = () => {
    active.current?.abort()
    setPending(undefined)
  }
  return { pending, error, connect, cancel }
}

export function ProjectSignInStatus({ signIn }: { signIn: ReturnType<typeof useProjectSignIn> }) {
  const { pending, error, cancel } = signIn
  return (
    <>
      {error && <FieldError>{error}</FieldError>}
      {pending && (
        <div className="flex flex-wrap items-center gap-(--s3)">
          <p role="status" className="wrap-anywhere text-(--fg2)">Waiting for {pending.name} sign-in…</p>
          {pending.authorizeURL && <a href={pending.authorizeURL} target="_blank" rel="noopener noreferrer" className={buttonVariants({ size: "sm" })}>Open sign-in link</a>}
          <Button type="button" size="sm" onClick={cancel}>Cancel sign-in</Button>
        </div>
      )}
    </>
  )
}
