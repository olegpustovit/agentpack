import { useDeferredValue, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow } from "@/components/kit/data"
import { ChevronsUpDown } from "lucide-react"
import { Empty, RowSkeleton } from "@/components/kit/feedback"
import { FieldError, FormGroup, FormRow, PopupSelect } from "@/components/kit/forms"
import { SearchField, SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { api, errorText, LOCAL, routeMissing } from "@/lib/api"
import { hud } from "@/lib/hud"
import {
  chatRoles,
  effectiveValue,
  formatPrice,
  formatRoleModel,
  formatTokens,
  groupSettings,
  inheritedValue,
  isChanged,
  isRoleReference,
  kindRoles,
  missingMachines,
  ompModelsQuery,
  ompSettingsQuery,
  ompTabs,
  overrideValue,
  parseRoleModel,
  roleKey,
  roleKinds,
  summarize,
  visibleSettings,
  withOverride,
  type OmpModel,
  type OmpSetting,
  type OmpSettings,
} from "./omp-model"
import { LoadError, Pane } from "./panes"

type Role = { id: string; name: string }
type Availability = { id: string; name: string; models?: OmpModel[]; needsUpdate: boolean }

// Rows under the role tables. OMP's registry keeps these off its own tabs.
const switchingRows: OmpSetting[] = [
  { key: "cycleOrder", tab: "roles", group: "", type: "array", label: "Cycle order", description: "Roles that Ctrl+P and the chat's model menu step through." },
  { key: "enabledModels", tab: "roles", group: "", type: "array", label: "Enabled models", description: "Limit pickers to these patterns. Empty shows every model a machine can use." },
  { key: "modelProviderOrder", tab: "roles", group: "", type: "array", label: "Provider order", description: "Preferred provider when several offer the same model." },
  { key: "retry.fallbackChains", tab: "roles", group: "", type: "record", label: "Fallback chains", description: "Models to switch to after provider errors, per role or model." },
  { key: "modelPresets", tab: "roles", group: "", type: "record", label: "Presets", description: "Named sets of role models and thinking levels you can switch between." },
]

// Update just the confirmed override, then refetch so edits from other machines also appear.
function useOmpSetting(key: string) {
  const queryClient = useQueryClient()
  const path = `/api/omp/settings/${encodeURIComponent(key)}` as const
  return useMutation<unknown, Error, { value?: unknown }>({
    mutationFn: ({ value }) => (value === undefined ? api(path, { method: "DELETE" }) : api(path, { method: "PUT", body: { value } })),
    onSuccess: async (_response, { value }) => {
      const queryKey = ompSettingsQuery(LOCAL).queryKey
      await queryClient.cancelQueries({ queryKey })
      queryClient.setQueryData(queryKey, (old) => old && withOverride(old, key, value))
      await queryClient.invalidateQueries({ queryKey })
    },
  })
}

export function OmpPane() {
  const settings = useQuery(ompSettingsQuery(LOCAL))
  const machines = useMachines()
  const models = useMachineQueries(ompModelsQuery)
  const [tab, setTab] = useState("roles")
  const [query, setQuery] = useState("")
  const [changedOnly, setChangedOnly] = useState(false)
  // Keep input updates responsive while broad matches mount their controls.
  const deferredQuery = useDeferredValue(query)
  if (settings.error) {
    if (routeMissing(settings.error)) return <Empty title="Needs update">Update agentpack on this gateway to manage Oh My Pi settings.</Empty>
    return <LoadError what="Oh My Pi settings" error={settings.error} retry={() => void settings.refetch()} />
  }
  if (!settings.data) return <RowSkeleton lines={2} />
  const data = settings.data
  const availability: Availability[] = machines
    .filter((machine) => machine.online)
    .map((machine) => ({ id: machine.id, name: machine.name, models: models.byMachine[machine.id]?.data, needsUpdate: models.byMachine[machine.id]?.needsUpdate ?? false }))
  const searching = deferredQuery.trim() !== ""
  const changedKeys = new Set(Object.keys(data.network))
  // "Changed" spans every tab, like VS Code's @modified filter.
  const filtering = searching || changedOnly
  const visible = visibleSettings(data.schema, changedOnly ? undefined : tab, deferredQuery)
  const items = changedOnly ? visible.filter((item) => isChanged(data, item.key)) : visible
  return (
    <Pane title="Oh My Pi">
      <div className="sticky top-0 z-1 -mx-(--s2) flex flex-wrap items-center gap-(--s3) bg-(--page) px-(--s2) py-(--s2)">
        <SegmentedControl
          aria-label="Oh My Pi settings"
          className="h-auto flex-wrap"
          value={filtering ? [] : [tab]}
          onValueChange={(values) => {
            if (typeof values[0] !== "string") return
            setTab(values[0])
            setQuery("")
            setChangedOnly(false)
          }}
        >
          {ompTabs.map((item) => (
            <SegmentedControlItem key={item.id} value={item.id}>{item.label}</SegmentedControlItem>
          ))}
        </SegmentedControl>
        <span className="flex-1" />
        <SegmentedControl aria-label="Show" value={[changedOnly ? "changed" : "all"]} onValueChange={(values) => typeof values[0] === "string" && setChangedOnly(values[0] === "changed")}>
          <SegmentedControlItem value="all">All</SegmentedControlItem>
          <SegmentedControlItem value="changed">{`Changed · ${changedKeys.size}`}</SegmentedControlItem>
        </SegmentedControl>
        <div className="flex w-56 max-[760px]:w-full">
          <SearchField aria-label="Search Oh My Pi settings" placeholder={`Search ${data.schema.length} settings`} value={query} onChange={(event) => setQuery(event.currentTarget.value)} />
        </div>
      </div>
      {!filtering && tab === "roles" ? (
        <ModelsTab settings={data} availability={availability} />
      ) : changedOnly && !searching ? (
        <>
          <RolesGroup title="Model roles" roles={[...chatRoles, ...kindRoles].filter((role) => isChanged(data, roleKey(role.id)))} settings={data} availability={availability} />
          <SettingsList settings={data} items={items.filter((item) => item.tab !== "roles")} searching query="" empty="Nothing is set for every machine yet. Each machine uses its own Oh My Pi settings." />
        </>
      ) : (
        <SettingsList settings={data} items={items} searching={filtering} query={deferredQuery} />
      )}
    </Pane>
  )
}

function SettingsList({ settings, items, searching, query, empty }: { settings: OmpSettings; items: OmpSetting[]; searching: boolean; query: string; empty?: string }) {
  if (items.length === 0) return empty === undefined ? <p className="text-(length:--fs-sm) text-(--fg2)">{`No settings match “${query.trim()}”.`}</p> : null
  return (
    <>
      {groupSettings(items, searching).map(([group, rows]) => (
        <FormGroup key={group} title={group}>
          {rows.map((item) => <SettingRow key={item.key} setting={item} settings={settings} />)}
        </FormGroup>
      ))}
    </>
  )
}

function ChangedDot() {
  return <span aria-hidden="true" title="Set for every machine" className="me-(--s2) inline-block size-1.5 shrink-0 rounded-full bg-(--accent) align-middle" />
}

function valueLabel(value: unknown) {
  if (value === undefined || value === null) return "Not set"
  if (typeof value === "boolean") return value ? "On" : "Off"
  if (typeof value === "object") return summarize(value)
  return String(value)
}

// Only a mesh override is worth a line: the control already shows the effective value.
function SettingValues({ settings, settingKey, fallback }: { settings: OmpSettings; settingKey: string; fallback?: unknown }) {
  if (overrideValue(settings, settingKey) === undefined) return null
  return <p className="mt-(--s1) break-words text-(length:--fs-xs) text-(--fg2)">{`Set for every machine · replaces ${valueLabel(inheritedValue(settings, settingKey) ?? fallback)}`}</p>
}

// Switches and menus apply at once; typed values apply on Enter or when the field loses focus.
function SettingRow({ setting, settings }: { setting: OmpSetting; settings: OmpSettings }) {
  const save = useOmpSetting(setting.key)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<{ value: unknown } | null>(null)
  const value = effectiveValue(settings, setting.key) ?? setting.default
  const hasOverride = isChanged(settings, setting.key)
  const scalar = ["boolean", "enum", "number", "string"].includes(setting.type)
  const typed = setting.type === "number" || (setting.type === "string" && !isRoleReference(value) && !isRoleReference(setting.default))
  const number = setting.type === "number"
  const invalidNumber = number && draft !== null && (String(draft.value).trim() === "" || !Number.isFinite(Number(draft.value)))
  const id = `omp-${setting.key}`
  function commit(next?: unknown) {
    if (next !== undefined && Object.is(next, value)) return setDraft(null)
    save.mutate({ value: next }, { onSuccess: () => setDraft(null) })
  }
  function commitDraft() {
    if (draft === null || invalidNumber || save.isPending) return
    commit(number ? Number(draft.value) : draft.value)
  }
  return (
    <FormRow
      label={<>{hasOverride && <ChangedDot />}{setting.label}</>}
      htmlFor={id}
      description={<>
        {setting.description}
        <SettingValues settings={settings} settingKey={setting.key} fallback={setting.default} />
      </>}
      detail={(invalidNumber || save.error) && <FieldError id={`${id}-error`}>{invalidNumber ? "Enter a valid number." : errorText(save.error)}</FieldError>}
    >
      {hasOverride && <Button variant="ghost" size="sm" className="text-(--accent)" aria-label={`Reset ${setting.label}`} disabled={save.isPending} onClick={() => commit()}>Reset</Button>}
      {scalar ? (
        <form className="contents" onSubmit={(event) => { event.preventDefault(); commitDraft() }} onBlur={() => typed && commitDraft()}>
          <SettingControl id={id} setting={setting} value={draft?.value ?? value} invalid={invalidNumber} disabled={save.isPending} edit={(next) => {
            if (save.error) save.reset()
            if (typed) setDraft({ value: next })
            else commit(next)
          }} />
        </form>
      ) : (
        <>
          <span className="max-w-48 truncate text-(length:--fs-sm)">{valueLabel(value)}</span>
          <Button id={id} size="sm" disabled={save.isPending} onClick={() => { save.reset(); setEditing(true) }}>Edit…</Button>
          <Sheet open={editing} onOpenChange={setEditing}>
            {editing && <ValueEditor setting={setting} value={value} save={save} onDone={() => setEditing(false)} />}
          </Sheet>
        </>
      )}
    </FormRow>
  )
}

function SettingControl({ id, setting, value, invalid, disabled, edit }: { id: string; setting: OmpSetting; value: unknown; invalid: boolean; disabled: boolean; edit: (value: unknown) => void }) {
  if (setting.type === "boolean") {
    return <Switch id={id} aria-label={setting.label} checked={value === true} disabled={disabled} onCheckedChange={edit} />
  }
  if (setting.type === "enum") {
    const options = setting.values ?? (value === undefined ? [] : [value])
    return (
      <div className="w-40 max-w-full">
        <PopupSelect
          label={setting.label}
          value={String(value ?? "")}
          items={options.map((option) => ({ value: String(option), label: String(option) }))}
          disabled={disabled}
          onValueChange={(next) => edit(options.find((option) => String(option) === next) ?? next)}
        />
      </div>
    )
  }
  if (setting.type === "string" && (isRoleReference(value) || isRoleReference(setting.default))) {
    const items = chatRoles.map((role) => ({ value: `@${role.id}`, label: `${role.name} role` }))
    if (typeof value === "string" && !items.some((item) => item.value === value)) items.push({ value, label: value })
    return (
      <div className="w-40 max-w-full">
        <PopupSelect label={setting.label} value={String(value ?? "")} items={items} disabled={disabled} onValueChange={edit} />
      </div>
    )
  }
  const number = setting.type === "number"
  return (
    <Input
      id={id}
      inputMode={number ? "decimal" : undefined}
      placeholder="Not set"
      className={number ? "w-30 text-right" : "w-52 max-w-full"}
      value={String(value ?? "")}
      aria-invalid={invalid}
      aria-describedby={invalid ? `${id}-error` : undefined}
      disabled={disabled}
      onChange={(event) => edit(event.currentTarget.value)}
    />
  )
}

// Lists of plain strings edit one per line; anything else edits as JSON.
function ValueEditor({ setting, value, save, onDone }: { setting: OmpSetting; value: unknown; save: ReturnType<typeof useOmpSetting>; onDone: () => void }) {
  const lines = setting.type === "array" && (value === undefined || (Array.isArray(value) && value.every((item) => typeof item === "string")))
  const [text, setText] = useState(() => (lines ? ((value as string[] | undefined) ?? []).join("\n") : JSON.stringify(value ?? (setting.type === "array" ? [] : {}), null, 2)))
  const [invalid, setInvalid] = useState("")
  function submit() {
    let next: unknown
    if (lines) next = text.split("\n").map((line) => line.trim()).filter(Boolean)
    else {
      try {
        next = JSON.parse(text)
      } catch {
        setInvalid("Enter valid JSON.")
        return
      }
    }
    setInvalid("")
    save.mutate({ value: next }, { onSuccess: onDone })
  }
  const id = `omp-edit-${setting.key}`
  return (
    <SheetContent className="w-120">
      <SheetTitle>{setting.label}</SheetTitle>
      <SheetDescription>{setting.description || (lines ? "One item per line." : "A JSON value.")}</SheetDescription>
      <form
        className="flex flex-col gap-(--s3)"
        onSubmit={(event) => {
          event.preventDefault()
          submit()
        }}
      >
        <label htmlFor={id} className="sr-only">{setting.label}</label>
        <textarea
          id={id}
          autoFocus
          rows={8}
          spellCheck={false}
          placeholder={lines ? "One item per line" : undefined}
          value={text}
          onChange={(event) => setText(event.currentTarget.value)}
          className="w-full resize-y rounded-(--r-ctl) border-[0.5px] border-(--sep) bg-(--field) px-(--s3) py-(--s2) text-(length:--fs) outline-none placeholder:text-(--fg3) focus:ring-2 focus:ring-(--accent-soft)"
        />
        {(invalid || save.error) && <FieldError>{invalid || errorText(save.error)}</FieldError>}
        <SheetActions>
          <Button type="submit" variant="primary" disabled={save.isPending}>Save</Button>
        </SheetActions>
      </form>
    </SheetContent>
  )
}

function ModelsTab({ settings, availability }: { settings: OmpSettings; availability: Availability[] }) {
  const schema = new Map(settings.schema.map((item) => [item.key, item]))
  return (
    <>
      <RolesGroup title="Chat roles" footer="Automatic lets Oh My Pi pick from the models each machine can use." roles={chatRoles} settings={settings} availability={availability} />
      <RolesGroup title="Model kinds" roles={kindRoles} settings={settings} availability={availability} />
      <FormGroup title="Switching and fallback">
        {switchingRows.map((row) => (
          <SettingRow key={row.key} setting={{ ...row, type: schema.get(row.key)?.type ?? row.type }} settings={settings} />
        ))}
      </FormGroup>
    </>
  )
}

function RolesGroup({ title, footer, roles, settings, availability }: { title: string; footer?: string; roles: Role[]; settings: OmpSettings; availability: Availability[] }) {
  const [browsing, setBrowsing] = useState<Role | undefined>()
  const catalog = new Map<string, OmpModel>()
  for (const machine of availability) for (const model of machine.models ?? []) if (!catalog.has(model.selector)) catalog.set(model.selector, model)
  if (roles.length === 0) return null
  return (
    <FormGroup title={title} footer={footer}>
      {roles.map((role) => <RoleRow key={role.id} role={role} settings={settings} catalog={catalog} availability={availability} onChoose={() => setBrowsing(role)} />)}
      <Sheet open={browsing !== undefined} onOpenChange={(open) => !open && setBrowsing(undefined)}>
        {browsing && (
          <ModelBrowser role={browsing} settings={settings} catalog={[...catalog.values()]} availability={availability} onDone={() => setBrowsing(undefined)} />
        )}
      </Sheet>
    </FormGroup>
  )
}

const levelScale = ["minimal", "low", "medium", "high", "xhigh", "max"]

function Level({ level }: { level: string }) {
  if (!level) return <span className="text-(--fg3)">—</span>
  const rank = levelScale.indexOf(level)
  return (
    <span className="inline-flex items-center gap-1 text-(length:--fs-sm) text-(--fg2)">
      {rank > 0 && levelScale.slice(1).map((step, index) => (
        <span key={step} aria-hidden className={`size-1.5 rounded-full bg-(--accent) ${index < rank ? "" : "opacity-35"}`} />
      ))}
      {level}
    </span>
  )
}

// Nothing when every machine can use it; otherwise the machines missing the provider with a way to sign in, or the ones that predate the API.
function Machines({ provider, availability }: { provider: string; availability: Availability[] }) {
  const missing = missingMachines(provider, availability)
  if (missing[0]) {
    return (
      <span className="text-(length:--fs-sm) text-(--warn-word)">
        {`Not signed in on ${missing.map((machine) => machine.name).join(", ")} · `}
        <Link className="text-(--accent)" to="/machines/$machine" params={{ machine: missing[0].id }}>Sign in…</Link>
      </span>
    )
  }
  const outdated = availability.filter((machine) => machine.needsUpdate)
  if (outdated.length > 0) return <span className="text-(length:--fs-sm) text-(--fg2)">{`Needs update on ${outdated.map((machine) => machine.name).join(", ")}`}</span>
  return null
}

// A role reads like a System Settings popup row: name, then its model as a button that opens the browser.
function RoleRow({ role, settings, catalog, availability, onChoose }: { role: Role; settings: OmpSettings; catalog: Map<string, OmpModel>; availability: Availability[]; onChoose: () => void }) {
  const reset = useOmpSetting(roleKey(role.id))
  const current = parseRoleModel(effectiveValue(settings, roleKey(role.id)))
  const name = current ? catalog.get(current.selector)?.name ?? current.selector : "Automatic"
  const changed = isChanged(settings, roleKey(role.id))
  return (
    <FormRow
      label={<>{changed && <ChangedDot />}{role.name}<span className="ms-(--s2) text-(length:--fs-xs) text-(--fg3)">{role.id}</span></>}
      description={current && <><Machines provider={current.provider} availability={availability} /><SettingValues settings={settings} settingKey={roleKey(role.id)} fallback="Automatic" /></>}
    >
      {changed && (
        <Button
          variant="ghost"
          size="sm"
          className="text-(--accent)"
          aria-label={`Reset ${role.name}`}
          disabled={reset.isPending}
          onClick={() => reset.mutate({}, { onError: (error) => hud.error(`Couldn't reset the ${role.name} role: ${errorText(error)}`, () => reset.mutate({})) })}
        >
          Reset
        </Button>
      )}
      <Button aria-label={`${role.name} model`} className="max-w-80 justify-between gap-(--s3)" onClick={onChoose}>
        <span className={current ? "truncate" : "truncate text-(--fg2)"}>{name}</span>
        {current?.level && <Level level={current.level} />}
        <ChevronsUpDown aria-hidden className="size-3 shrink-0 text-(--fg3)" />
      </Button>
    </FormRow>
  )
}

function ModelBrowser({ role, settings, catalog, availability, onDone }: { role: Role; settings: OmpSettings; catalog: OmpModel[]; availability: Availability[]; onDone: () => void }) {
  const save = useOmpSetting(roleKey(role.id))
  const current = parseRoleModel(effectiveValue(settings, roleKey(role.id)))
  const [search, setSearch] = useState("")
  const [pick, setPick] = useState(current?.selector)
  const [level, setLevel] = useState(current?.level ?? "")
  const kinds = roleKinds(role.id)
  const words = search.toLowerCase()
  const shown = catalog.filter((model) => kinds.includes(model.kind) && `${model.name} ${model.selector}`.toLowerCase().includes(words))
  const providers = [...new Set(shown.map((model) => model.provider))]
  const users = [...chatRoles, ...kindRoles].map((item) => ({ ...item, model: parseRoleModel(effectiveValue(settings, roleKey(item.id)))?.selector }))
  const picked = catalog.find((model) => model.selector === pick)
  const usable = picked?.thinking.includes(level) ? level : ""
  return (
    <SheetContent className="w-[min(760px,calc(100vw-var(--s6)*2))] max-w-none gap-(--s3) sm:max-w-none max-[760px]:w-full">
      <SheetTitle>{`${role.name} model`}</SheetTitle>
      <SheetDescription>{`Used by every Oh My Pi session for the ${role.name.toLowerCase()} role.`}</SheetDescription>
      <SearchField aria-label="Search models" placeholder="Search models" value={search} onChange={(event) => setSearch(event.currentTarget.value)} />
      <div className="flex max-h-[min(480px,50dvh)] min-h-0 flex-col">
        <DataGrid className="min-w-0">
          <DataGridHead>
            <tr>
              <DataGridHeaderCell>Model</DataGridHeaderCell>
              <DataGridHeaderCell className="w-24 text-right">Input</DataGridHeaderCell>
              <DataGridHeaderCell className="w-16 text-right">Ctx</DataGridHeaderCell>
              <DataGridHeaderCell className="w-18 text-right">Max out</DataGridHeaderCell>
              <DataGridHeaderCell className="w-24 text-right">$/M in·out</DataGridHeaderCell>
            </tr>
          </DataGridHead>
          <tbody>
            {providers.map((provider) => {
              const missing = missingMachines(provider, availability)
              return [
                <tr key={provider}>
                  <td colSpan={5} className="h-(--row-h) px-(--s3) pt-(--s3) text-(length:--fs-xs) font-semibold text-(--fg3)">
                    {provider}
                    {missing.length > 0 && <span className="font-normal">{` · not signed in on ${missing.map((machine) => machine.name).join(", ")}`}</span>}
                  </td>
                </tr>,
                ...shown.filter((model) => model.provider === provider).map((model) => {
                  const selected = model.selector === pick
                  return (
                    <DataGridRow
                      key={model.selector}
                      aria-selected={selected}
                      onClick={() => setPick(model.selector)}
                      className="group/model text-(--fg2) aria-selected:bg-(--accent-fill) aria-selected:text-(--accent-fg) aria-selected:hover:bg-(--accent-fill)"
                    >
                      <DataGridCell>
                        <button type="button" aria-pressed={selected} className="max-w-full truncate text-left text-(--fg) outline-none focus-visible:underline group-aria-selected/model:text-(--accent-fg)">
                          {model.name}
                          {/* Dated and alias ids share a name; the id tells them apart. */}
                          {shown.some((other) => other !== model && other.provider === model.provider && other.name === model.name) && (
                            <span className="ms-(--s2) text-(length:--fs-xs) text-(--fg3) group-aria-selected/model:text-(--accent-fg)">{model.selector.slice(model.provider.length + 1)}</span>
                          )}
                        </button>
                        {users.filter((item) => item.model === model.selector).map((item) => (
                          <span key={item.id} className="ms-(--s2) rounded-(--r-row) bg-(--track) px-1 text-(length:--fs-xs) font-semibold text-(--fg2) uppercase group-aria-selected/model:bg-transparent group-aria-selected/model:text-(--accent-fg)">
                            {item.id}
                          </span>
                        ))}
                      </DataGridCell>
                      <DataGridCell className="text-right tabular-nums">{model.input.includes("image") ? "text, image" : "text"}</DataGridCell>
                      <DataGridCell className="text-right tabular-nums">{formatTokens(model.contextWindow)}</DataGridCell>
                      <DataGridCell className="text-right tabular-nums">{formatTokens(model.maxTokens)}</DataGridCell>
                      <DataGridCell className="text-right tabular-nums">{formatPrice(model.cost)}</DataGridCell>
                    </DataGridRow>
                  )
                }),
              ]
            })}
          </tbody>
        </DataGrid>
        {shown.length === 0 && <p className="p-(--s3) text-(length:--fs-sm) text-(--fg2)">{catalog.length === 0 ? "No machine reports any Oh My Pi models. Sign in to a provider on a machine first." : "No models match."}</p>}
      </div>
      <div className="flex min-h-(--ctl-h) flex-wrap items-center gap-(--s3) text-(length:--fs-sm) text-(--fg2)">
        {picked && picked.thinking.length > 0 ? (
          <>
            Thinking
            <SegmentedControl aria-label="Thinking" value={[usable || "inherit"]} onValueChange={(values) => typeof values[0] === "string" && setLevel(values[0] === "inherit" ? "" : values[0])}>
              {["inherit", ...picked.thinking].map((item) => <SegmentedControlItem key={item} value={item}>{item}</SegmentedControlItem>)}
            </SegmentedControl>
          </>
        ) : (
          <span>{picked ? "This model has no thinking levels." : "Choose a model."}</span>
        )}
      </div>
      {save.error && <FieldError>{errorText(save.error)}</FieldError>}
      <SheetActions>
        <Button variant="primary" disabled={!pick || save.isPending} onClick={() => pick && save.mutate({ value: formatRoleModel(pick, usable) }, { onSuccess: onDone })}>
          {`Use for ${role.name}`}
        </Button>
      </SheetActions>
    </SheetContent>
  )
}
