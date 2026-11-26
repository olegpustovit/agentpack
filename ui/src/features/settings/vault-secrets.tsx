import { useState, type FormEvent, type ReactNode } from "react"
import { Plus, X } from "lucide-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Section } from "@/components/kit/data"
import { Field, FieldError, FieldGrid, FieldLabel, FormGroup, FormRow } from "@/components/kit/forms"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { api, errorText } from "@/lib/api"
import type { Connection } from "./connections-model"
import { AGENTS_MODULE, changesAgent, refusedForAgents } from "./vault-model"

// Shared secrets are the mesh's env credentials; the pane passes one row per secret.
export function SecretsGroup({ secrets, children }: { secrets: Connection[]; children: ReactNode }) {
  const [adding, setAdding] = useState(false)
  return (
    <>
      <Section title="Shared secrets" action={<Button size="sm" onClick={() => setAdding(true)}><Plus aria-hidden className="size-3.5" />Add secret…</Button>}>
        <FormGroup footer="Agents see names through MCP; values only as environment variables.">
          {secrets.length ? <ul>{children}</ul> : <FormRow label="No shared secrets" description="Add variables that local MCP servers, and agents you allow, can use on every machine." />}
        </FormGroup>
      </Section>
      <Sheet open={adding} onOpenChange={setAdding}>
        {adding && <SecretSheet taken={secrets.map((secret) => secret.label)} onDone={() => setAdding(false)} />}
      </Sheet>
    </>
  )
}

type Pair = { id: number; name: string; value: string }

// Adds a secret, or replaces one: values are write-only, so replacing asks for every value again.
export function SecretSheet({ secret, taken = [], onDone }: { secret?: Connection; taken?: string[]; onDone: () => void }) {
  const queryClient = useQueryClient()
  const given = secret?.plugins.includes(AGENTS_MODULE) ?? false
  const [label, setLabel] = useState(secret?.label ?? "")
  const [pairs, setPairs] = useState<Pair[]>(() => (secret?.names.length ? secret.names : [""]).map((name, id) => ({ id, name, value: "" })))
  const [agents, setAgents] = useState(given)
  const [tried, setTried] = useState(false)
  // The name this sheet already saved: a retry after a refused grant must not read as a duplicate.
  const [saved, setSaved] = useState("")
  const edit = (id: number, change: Partial<Pair>) => setPairs((list) => list.map((pair) => (pair.id === id ? { ...pair, ...change } : pair)))
  const names = pairs.map((pair) => pair.name.trim())
  // Saving under an existing name replaces that secret, so adding must not reuse one.
  const duplicate = !secret && label.trim() !== saved && taken.includes(label.trim())
  const repeated = new Set(names).size !== names.length
  const refused = agents ? names.filter(refusedForAgents) : []
  const warned = agents ? names.filter(changesAgent) : []
  const save = useMutation<void, Error, void>({
    mutationFn: async () => {
      const values = Object.fromEntries(pairs.map((pair) => [pair.name.trim(), pair.value]))
      const grant = (id: string, method: "POST" | "DELETE") => api(`/api/mcp/credentials/${encodeURIComponent(id)}/approvals`, { method, body: { module: AGENTS_MODULE } })
      // Access is taken away before the new values land, and given only after.
      if (secret && given && !agents) await grant(secret.id, "DELETE")
      const { id } = await api<{ id: string }>("/api/mcp/credentials", { body: { kind: "env", label: label.trim(), values } })
      setSaved(label.trim())
      if (agents && !given) await grant(id, "POST").catch((error: unknown) => { throw new Error(`Saved, but not given to agents: ${errorText(error)}`) })
    },
    onSuccess: onDone,
    // A refused grant still leaves the secret saved.
    onSettled: () => void queryClient.invalidateQueries({ queryKey: ["mcp-credentials"] }),
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    setTried(true)
    if (!duplicate && !repeated && !refused.length) save.mutate()
  }
  return (
    <SheetContent>
      <SheetTitle>{secret ? `Replace values for “${secret.label}”` : "Add secret"}</SheetTitle>
      <SheetDescription>Saved for every enrolled machine. Values are never shown again, only replaced.</SheetDescription>
      <form className="flex flex-col gap-(--s5)" onSubmit={submit}>
        {!secret && (
          <FieldGrid className="gap-(--s2) max-[760px]:gap-(--s2)">
            <Field>
              <FieldLabel htmlFor="secret-name">Name</FieldLabel>
              <Input id="secret-name" required maxLength={256} placeholder="e.g. cloudflare" autoComplete="off" value={label} onChange={(event) => setLabel(event.target.value)} />
            </Field>
            {tried && duplicate && <FieldError>{`A secret named ${label.trim()} exists. Choose another name.`}</FieldError>}
          </FieldGrid>
        )}
        <fieldset className="flex min-w-0 flex-col gap-(--s3)">
          <legend className="mb-(--s3) text-(length:--fs-sm) text-(--fg2)">Variables</legend>
          {pairs.map((pair, index) => (
            <div key={pair.id} className="flex items-center gap-(--s3) max-[760px]:not-first-of-type:mt-(--s4)">
              <div className="flex min-w-0 flex-1 gap-(--s3) max-[760px]:flex-col">
                <Input aria-label={`Variable ${index + 1}`} required pattern="[A-Za-z_][A-Za-z0-9_]*" placeholder="NAME" autoComplete="off" spellCheck={false} value={pair.name} onChange={(event) => edit(pair.id, { name: event.target.value })} />
                {/* new-password keeps password managers from filling a saved password in as the value. */}
                <Input aria-label={`Value ${index + 1}`} type="password" required autoComplete="new-password" placeholder="Value" value={pair.value} onChange={(event) => edit(pair.id, { value: event.target.value })} />
              </div>
              {pairs.length > 1 && (
                <Button type="button" variant="ghost" size="icon" className="shrink-0" aria-label={`Remove variable ${index + 1}`} onClick={() => setPairs((list) => list.filter((item) => item.id !== pair.id))}>
                  <X aria-hidden />
                </Button>
              )}
            </div>
          ))}
          {tried && repeated && <FieldError>Give each variable its own name.</FieldError>}
          {tried && refused.length > 0 && <FieldError>{`${refused.join(", ")} can’t be given to agents. Rename it or turn the switch off.`}</FieldError>}
          {save.error && <FieldError>{errorText(save.error)}</FieldError>}
          <div><Button type="button" size="sm" onClick={() => setPairs((list) => [...list, { id: (list.at(-1)?.id ?? 0) + 1, name: "", value: "" }])}><Plus aria-hidden className="size-3.5" />Add variable</Button></div>
        </fieldset>
        <FormGroup>
          <FormRow
            label="Give to agents on every machine"
            htmlFor="secret-agents"
            description="Agents can read these values and may print them into the chat."
            detail={warned.length > 0 && <p role="status" className="text-(length:--fs-sm) text-(--warn-word)">{`${warned.join(", ")} ${warned.length > 1 ? "change" : "changes"} how the agent itself runs.`}</p>}
          >
            <Switch id="secret-agents" checked={agents} onCheckedChange={setAgents} />
          </FormRow>
        </FormGroup>
        <SheetActions>
          <Button type="submit" variant="primary" disabled={save.isPending}>{save.isPending ? "Saving…" : "Save"}</Button>
        </SheetActions>
      </form>
    </SheetContent>
  )
}
