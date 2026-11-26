import { useState, type ReactNode } from "react"
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query"
import { AgentTile } from "@/components/kit/agent-tile"
import { Section } from "@/components/kit/data"
import { RowSkeleton, Spinner } from "@/components/kit/feedback"
import { FormGroup, FormRow, PopupSelect } from "@/components/kit/forms"
import { ConfirmAlert, ConfirmAlertContent, Menu, MenuContent, MenuItem, MenuTrigger, RowMoreButton, Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { StatusDot } from "@/components/kit/status-dot"
import { Button } from "@/components/ui/button"
import { agentsQuery } from "@/features/agents/queries"
import { SignIn } from "@/features/agents/sign-in"
import { identityQuery, useMachines, type Machine } from "@/features/machines/queries"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { api, ApiError, errorText, LOCAL, routeMissing } from "@/lib/api"
import { harnessName } from "@/lib/format"
import { hud } from "@/lib/hud"
import { useNow } from "@/lib/use-now"
import { LoadError } from "./panes"
import { sharedSignInsQuery, signInStatus, vaultAgents, type SharedSignIn } from "./vault-model"

export function VaultAgents() {
  const shared = useQuery(sharedSignInsQuery)
  const machines = useMachines()
  const localNode = useQuery(identityQuery).data?.node.id
  const now = useNow(true, 60_000)
  const [signingIn, setSigningIn] = useState<string>()
  const machineID = (node: string) => (node === localNode ? LOCAL : node)
  const machineName = (node: string) => machines.find((item) => item.id === machineID(node))?.name ?? node
  const group = (children: ReactNode) => <Section title="Agent sign-ins"><FormGroup>{children}</FormGroup></Section>

  if (routeMissing(shared.error)) return group(<FormRow label={<StatusDot state="needsUpdate" />} description="Update Agentpack on this machine to see agent sign-ins." />)
  if (shared.error instanceof ApiError && shared.error.status === 403) return group(<FormRow label="Only the gateway owner can see agent sign-ins." />)
  if (shared.error) return <LoadError what="agent sign-ins" error={shared.error} retry={() => void shared.refetch()} />
  if (!shared.data) return <RowSkeleton lines={2} />
  return (
    <>
      {group(vaultAgents.map((harness) => {
        const row = shared.data.find((item) => item.harness === harness)
        return <AgentRow key={harness} harness={harness} row={row} status={signInStatus(harness, row, machineName, now)} machines={machines}
          publisher={row?.publisher ? machineID(row.publisher) : undefined} onSignIn={() => setSigningIn(harness)} />
      }))}
      <Sheet open={signingIn !== undefined} onOpenChange={(open) => !open && setSigningIn(undefined)}>
        {signingIn && <SignInSheet key={signingIn} harness={signingIn} />}
      </Sheet>
    </>
  )
}

function AgentRow({ harness, row, status, machines, publisher, onSignIn }: { harness: string; row?: SharedSignIn; status: string; machines: Machine[]; publisher?: string; onSignIn: () => void }) {
  const [open, setOpen] = useState(false)
  const phone = useIsPhone()
  const name = harnessName(harness)
  const expired = row?.state === "expired"
  const indent = "ps-[calc(18px+var(--s4))]"
  const signIn = harness !== "omp" && row?.state !== "shared"
    ? <Button size="sm" aria-label={`Sign in to ${name}…`} onClick={onSignIn}>Sign in…</Button>
    : undefined
  return (
    <FormRow
      label={<span className="flex items-center gap-(--s4)">
        <AgentTile harness={harness} state={expired ? "needsYou" : "idle"} label={expired ? `${name}, sign-in expired` : name} className="[--dot-ring:var(--window)]" />
        {name}
      </span>}
      description={<span className={`block ${indent}`}>{status}</span>}
      detail={<>
        {/* Phones put the action under the status line. */}
        {phone && signIn && <div className={indent}>{signIn}</div>}
        <details className={`w-full text-(length:--fs-sm) ${indent}`} onToggle={(event) => setOpen(event.currentTarget.open)}>
          <summary className="w-fit cursor-pointer text-(--fg2)">Machines</summary>
          {open && <ul aria-label={`${name} on each machine`} className="mt-(--s2)">{machines.map((machine) => <MachineLine key={machine.id} harness={harness} machine={machine} />)}</ul>}
        </details>
      </>}
    >
      {harness !== "omp" && row?.state === "shared"
        ? <HealthyMenu harness={harness} machines={machines} publisher={publisher} onSignIn={onSignIn} />
        : phone ? undefined : signIn}
    </FormRow>
  )
}

function HealthyMenu({ harness, machines, publisher, onSignIn }: { harness: string; machines: Machine[]; publisher?: string; onSignIn: () => void }) {
  const queryClient = useQueryClient()
  const [menu, setMenu] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const name = harnessName(harness)
  // Sign out where the sign-in came from; this machine when that one is offline.
  const target = machines.find((item) => item.id === publisher && item.online) ?? machines.find((item) => item.id === LOCAL)
  const accounts = useQuery({ ...agentsQuery(target?.id ?? LOCAL), enabled: menu && Boolean(target) })
  const canSignOut = accounts.data?.find((item) => item.harness === harness)?.signOut
  const signOut = useMutation({
    mutationFn: (machine: Machine) => api(`/api/agents/${encodeURIComponent(harness)}/logout`, { machine: machine.id, method: "POST" }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["agents"] }),
    onError: (error, machine) => hud.error(`Couldn't sign ${name} out on ${machine.name}: ${routeMissing(error) ? "needs an update" : errorText(error)}`, routeMissing(error) ? undefined : () => signOut.mutate(machine)),
  })
  return (
    <>
      <Menu open={menu} onOpenChange={setMenu}>
        <MenuTrigger render={<RowMoreButton aria-label={`${name} actions`} className="opacity-100" />} />
        <MenuContent align="end">
          <MenuItem onClick={onSignIn}>Sign in again…</MenuItem>
          {(accounts.isPending || canSignOut) && <MenuItem danger disabled={!canSignOut} onClick={() => setConfirming(true)}>Sign out…</MenuItem>}
        </MenuContent>
      </Menu>
      <ConfirmAlert open={confirming} onOpenChange={setConfirming}>
        {confirming && target && <ConfirmAlertContent
          // shortcut: same words as the Machines page; share them when this ships.
          title={`Sign ${name} out on ${target.name}?`}
          description={`Signs ${name} out on ${target.name} and stops future sharing from this machine. Enrolled peers keep their own copy until signed out there. Running chats keep their session until they stop.`}
          destructiveLabel="Sign out"
          onConfirm={() => { setConfirming(false); signOut.mutate(target) }}
        />}
      </ConfirmAlert>
    </>
  )
}

// One machine's own view of an agent. Loaded only once the disclosure is open.
function MachineLine({ harness, machine }: { harness: string; machine: Machine }) {
  const queryClient = useQueryClient()
  const accounts = useQuery({ ...agentsQuery(machine.id), enabled: machine.online })
  // macOS keeps some sign-ins in the keychain, read only when the person asks.
  const check = useMutation({
    mutationFn: () => queryClient.fetchQuery({ ...agentsQuery(machine.id, true), staleTime: 0, retry: false }),
    onSuccess: (data) => queryClient.setQueryData(agentsQuery(machine.id).queryKey, data),
  })
  const account = accounts.data?.find((item) => item.harness === harness)
  const state = !machine.online ? "Offline"
    : routeMissing(accounts.error) ? "Needs update"
      : accounts.error ? errorText(accounts.error)
        : !accounts.data ? undefined
          : !account?.installed ? "Not installed"
            : account.checkRequired ? "Unchecked"
              : account.signedIn ? ["Signed in", account.label].filter(Boolean).join(" · ")
                : account.error ?? "Not signed in"
  return (
    <li className="flex min-h-(--row-h) items-center gap-(--s4) border-t-[0.5px] border-(--sep-soft) first:border-t-0">
      <span className="w-36 shrink-0 truncate max-[760px]:w-28">{machine.name}</span>
      <span className="min-w-0 flex-1 truncate text-(--fg2)" title={state}>{check.error ? `Check failed: ${errorText(check.error)}` : state ?? <Spinner />}</span>
      {machine.online && account?.checkRequired && <Button size="sm" aria-label={`Check ${harnessName(harness)} on ${machine.name}`} disabled={check.isPending} onClick={() => check.mutate()}>Check</Button>}
    </li>
  )
}

// The sheet opens at once; the machine starts as the first online one with the CLI, this machine first.
function SignInSheet({ harness }: { harness: string }) {
  const name = harnessName(harness)
  const machines = useMachines().filter((item) => item.online)
  const answers = useQueries({ queries: machines.map((machine) => agentsQuery(machine.id)) })
  const [chosen, setChosen] = useState<string>()
  const accountOn = (index: number) => answers[index]?.data?.find((item) => item.harness === harness)
  // Wait for every machine ahead in line before picking one.
  const first = answers.findIndex((answer, index) => answer.isPending || accountOn(index)?.installed)
  const auto = first >= 0 && !answers[first]!.isPending ? machines[first] : undefined
  // Pin the first pick: a machine coming online later must not restart a sign-in in progress.
  if (!chosen && auto) setChosen(auto.id)
  const index = machines.findIndex((item) => item.id === (chosen ?? auto?.id))
  const machine = machines[index]
  const account = machine ? accountOn(index) : undefined
  return (
    <SheetContent>
      <SheetTitle>Sign in to {name}</SheetTitle>
      <div className="flex items-center gap-(--s4)">
        <span className="text-(--fg2)">Machine</span>
        <PopupSelect
          label="Machine"
          value={machine?.id ?? ""}
          onValueChange={setChosen}
          items={machines.map((item, at) => {
            const answer = answers[at]!
            const note = routeMissing(answer.error) ? "Needs update" : answer.error ? "Unavailable" : answer.data && !accountOn(at)?.installed ? "Not installed" : undefined
            return { value: item.id, label: item.name, detail: note, disabled: Boolean(note) }
          })}
        />
      </div>
      <SheetDescription>{`${name} sign-ins are shared with every enrolled machine. Sign-in links expire if not completed in time.`}</SheetDescription>
      {machine && answers[index]!.isPending ? <Spinner />
        : machine && account?.methods.length ? <SignIn key={machine.id} machine={machine.id} machineName={machine.name} harness={harness} methods={account.methods} />
        : machine ? <p className="text-(length:--fs-sm) text-(--fg2)">{`Run ${name} in a terminal on ${machine.name} to sign in.`}</p>
          : chosen ? <p className="text-(length:--fs-sm) text-(--fg2)">That machine is offline. Choose another one.</p>
          : first < 0 ? <p className="text-(length:--fs-sm) text-(--fg2)">{`No online machine has ${name} installed.`}</p>
            : <Spinner />}
    </SheetContent>
  )
}
