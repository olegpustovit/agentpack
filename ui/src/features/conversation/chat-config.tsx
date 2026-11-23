import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { useEffect, useState } from "react"
import { Check, ChevronDown } from "lucide-react"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/kit/overlays"
import { PopupSelect } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { pendingChatClaim } from "@/features/sessions/start"
import { ompSettingsQuery, quickRoles } from "@/features/settings/omp-model"
import type { SessionInfo } from "@/lib/api-types"
import { api } from "@/lib/api"
import { hud } from "@/lib/hud"
import { agentSnapshotQuery, conversationErrorMessage, conversationNeedsUpdate } from "./queries"
import { configRole, dangerousMode, parseConfigOptions } from "./config-options"
import type { ThreadState } from "./model"

// The agent's options (permissions, model, effort and the rest) as the log, the worker or the last
// snapshot reports them, with values set here shown until the agent confirms them.
export function useChatConfig({ machine, sessionId, info, parent, live, starting, logConfig }: {
  machine: string
  sessionId: string
  info?: SessionInfo
  parent: Pick<ThreadState, "agentConfig" | "agentMode">
  live: boolean
  starting: boolean
  logConfig: boolean
}) {
  const queryClient = useQueryClient()
  const [configValues, setConfigValues] = useState<Record<string, string | boolean>>({})
  const { data: legacyConfig, error: configError } = useQuery({
    queryKey: ["conversation-config", machine, sessionId],
    // Only a running agent has options; a stopped chat has no worker, and its 404 is not a version gap.
    enabled: live && !logConfig,
    queryFn: () => api<unknown>(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/config-options`, { machine }),
    retry: false,
  })
  const snapshot = useQuery(agentSnapshotQuery(machine, info)).data
  const configRaw = parent.agentConfig ?? legacyConfig ?? snapshot?.config
  const configOptions = parseConfigOptions(configRaw).map((option) =>
    option.type === "select" && configRole(option) === "permissions" && parent.agentMode && option.items.some((item) => item.value === parent.agentMode)
      ? { ...option, value: parent.agentMode }
      : option,
  )
  const provisionalConfig = starting && configOptions.length > 0
  const selectOptions = configOptions.filter((option) => option.type === "select")
  const permissionsOption = selectOptions.find((option) => configRole(option) === "permissions")
  const modelOption = selectOptions.find((option) => configRole(option) === "model")
  // An Oh My Pi chat can switch to a role's model (and thinking), as set on the machine running it.
  const ompSettings = useQuery({ ...ompSettingsQuery(machine), enabled: live && info?.harness === "omp", retry: false }).data
  const roles = ompSettings && modelOption ? quickRoles(ompSettings, modelOption.items.map((item) => item.value)) : []
  const effortOption = selectOptions.find((option) => configRole(option) === "effort")
  const extraOptions = configOptions.filter((option) => configRole(option) === "other")
  const setConfig = useMutation({
    mutationFn: async ({ key, value }: { key: string; value: string | boolean }) => {
      const claimed = await pendingChatClaim(sessionId)
      return api(`/api/sessions/${encodeURIComponent(claimed?.id ?? sessionId)}/conversation/config`, {
        machine,
        method: "POST",
        body: { key, value },
      })
    },
    onSuccess: (_, { key }) => {
      if (!starting && !logConfig) {
        return queryClient.invalidateQueries({ queryKey: ["conversation-config", machine, sessionId] }).then(() =>
          setConfigValues((values) => withoutKey(values, key)),
        )
      }
    },
    onError: (error, { key, value }) => {
      setConfigValues((values) => withoutKey(values, key))
      hud.error(
        conversationErrorMessage(machine, `set ${key}`, error),
        conversationNeedsUpdate(error) ? undefined : () => setConfigValue(key, value),
      )
    },
  })
  function setConfigValue(key: string, value: string | boolean) {
    setConfigValues((current) => ({ ...current, [key]: value }))
    setConfig.mutate({ key, value })
  }
  useEffect(() => {
    if (live && configRaw !== undefined) setConfigValues((values) => Object.keys(values).length ? {} : values)
  }, [configRaw, live, parent.agentMode])
  return { configError, snapshot, configValues, setConfigValue, setting: setConfig.isPending, provisionalConfig, permissionsOption, modelOption, effortOption, extraOptions, roles }
}

// The composer's permissions select and model / effort / options menu.
export function ChatConfigControls({ config, harness, starting, needsYou, needsUpdate }: {
  config: ReturnType<typeof useChatConfig>
  harness?: string
  starting: boolean
  needsYou: boolean
  needsUpdate: boolean
}) {
  const navigate = useNavigate()
  const { configValues, setConfigValue, provisionalConfig, permissionsOption, modelOption, effortOption, extraOptions, roles } = config
  return (
    <>
    {provisionalConfig && <span className="text-(length:--fs-xs) text-(--fg3)">Provisional</span>}
    {permissionsOption && (
      <PopupSelect
        label="Permissions"
        value={typeof configValues[permissionsOption.id] === "string" ? configValues[permissionsOption.id] as string : permissionsOption.value}
        onValueChange={(value) => setConfigValue(permissionsOption.id, value)}
        // Stays usable while the agent waits on you: that is when switching to bypass helps.
        disabled={config.setting || needsUpdate}
        items={permissionsOption.items.map((item) => ({ ...item, danger: dangerousMode(item) }))}
      />
    )}
    {(modelOption || effortOption || extraOptions.length > 0) && (
      <Menu>
        <MenuTrigger
          render={
            <Button
              type="button"
              size="sm"
              variant="ghost"
              aria-label={effortOption ? "Model and effort" : modelOption?.name ?? "Options"}
              title="Choose model and effort"
              disabled={config.setting || (!starting && needsYou) || needsUpdate}
              className="max-w-48 gap-(--s2)"
            >
              <span className="min-w-0 truncate">
                {modelOption
                  ? modelOption.items.find((item) => item.value === (typeof configValues[modelOption.id] === "string" ? configValues[modelOption.id] : modelOption.value))?.label ?? modelOption.value
                  : effortOption ? "Effort" : "Options"}
              </span>
              {effortOption && (
                <span className="shrink-0 text-(--fg3)">
                  {effortOption.items.find((item) => item.value === (typeof configValues[effortOption.id] === "string" ? configValues[effortOption.id] : effortOption.value))?.label ?? effortOption.value}
                </span>
              )}
              <ChevronDown aria-hidden />
            </Button>
          }
        />
        <MenuContent align="end" className={roles.length > 0 ? "min-w-72" : undefined}>
          {roles.length > 0 && modelOption && (
            <>
              <p className="px-[11px] pt-(--s2) pb-(--s1) text-(length:--fs-xs) font-semibold text-(--fg3)">Roles</p>
              {roles.map((role) => (
                <MenuItem
                  key={role.id}
                  icon={<span aria-hidden className="size-[15px]" />}
                  disabled={config.setting || (!starting && needsYou) || needsUpdate}
                  onClick={() => {
                    setConfigValue(modelOption.id, role.selector)
                    if (role.level && effortOption?.items.some((item) => item.value === role.level)) setConfigValue(effortOption.id, role.level)
                  }}
                >
                  {role.name}
                  <span className="ms-auto ps-(--s4) text-(--fg3)">{[modelOption.items.find((item) => item.value === role.selector)?.label ?? role.selector, role.level].filter(Boolean).join(" · ")}</span>
                </MenuItem>
              ))}
              <div role="separator" className="mx-[11px] my-[5px] h-px bg-(--menu-sep)" />
            </>
          )}
          {modelOption && (
            <>
              <p className="px-[11px] pt-(--s2) pb-(--s1) text-(length:--fs-xs) font-semibold text-(--fg3)">Model</p>
              {modelOption.items.map((item) => {
                const selected = (typeof configValues[modelOption.id] === "string" ? configValues[modelOption.id] : modelOption.value) === item.value
                return (
                  <MenuItem
                    key={item.value}
                    role="menuitemradio"
                    aria-checked={selected}
                    icon={selected ? <Check aria-hidden /> : <span aria-hidden className="size-[15px]" />}
                    disabled={config.setting || (!starting && needsYou) || needsUpdate}
                    onClick={() => setConfigValue(modelOption.id, item.value)}
                  >
                    {item.label}
                  </MenuItem>
                )
              })}
            </>
          )}
          {effortOption && (
            <>
              {modelOption && <div role="separator" className="mx-[11px] my-[5px] h-px bg-(--menu-sep)" />}
              <p className="px-[11px] pt-(--s2) pb-(--s1) text-(length:--fs-xs) font-semibold text-(--fg3)">Effort</p>
              {effortOption.items.map((item) => {
                const selected = (typeof configValues[effortOption.id] === "string" ? configValues[effortOption.id] : effortOption.value) === item.value
                return (
                  <MenuItem
                    key={item.value}
                    role="menuitemradio"
                    aria-checked={selected}
                    icon={selected ? <Check aria-hidden /> : <span aria-hidden className="size-[15px]" />}
                    disabled={config.setting || (!starting && needsYou) || needsUpdate}
                    onClick={() => setConfigValue(effortOption.id, item.value)}
                  >
                    {item.label}
                  </MenuItem>
                )
              })}
            </>
          )}
          {extraOptions.map((option) => {
            const current = configValues[option.id] ?? option.value
            return (
              <div key={option.id}>
                <div role="separator" className="mx-[11px] my-[5px] h-px bg-(--menu-sep)" />
                {option.type === "boolean" ? (
                  <MenuItem
                    role="menuitemcheckbox"
                    aria-checked={current === true}
                    icon={current === true ? <Check aria-hidden /> : <span aria-hidden className="size-[15px]" />}
                    disabled={config.setting || (!starting && needsYou) || needsUpdate}
                    onClick={() => setConfigValue(option.id, current !== true)}
                  >
                    {option.name}
                  </MenuItem>
                ) : (
                  <>
                    <p className="px-[11px] pt-(--s2) pb-(--s1) text-(length:--fs-xs) font-semibold text-(--fg3)">{option.name}</p>
                    {option.items.map((item) => (
                      <MenuItem
                        key={item.value}
                        role="menuitemradio"
                        aria-checked={current === item.value}
                        icon={current === item.value ? <Check aria-hidden /> : <span aria-hidden className="size-[15px]" />}
                        disabled={config.setting || (!starting && needsYou) || needsUpdate}
                        onClick={() => setConfigValue(option.id, item.value)}
                      >
                        {item.label}
                      </MenuItem>
                    ))}
                  </>
                )}
              </div>
            )
          })}
          {harness === "omp" && (
            <>
              <div role="separator" className="mx-[11px] my-[5px] h-px bg-(--menu-sep)" />
              <MenuItem onClick={() => void navigate({ to: "/settings/$pane", params: { pane: "omp" } })}>Oh My Pi settings…</MenuItem>
            </>
          )}
        </MenuContent>
      </Menu>
    )}
    </>
  )
}

function withoutKey<T>(values: Record<string, T>, key: string): Record<string, T> {
  const rest = { ...values }
  delete rest[key]
  return rest
}
