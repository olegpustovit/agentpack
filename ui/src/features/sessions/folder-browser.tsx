import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Folder, CornerLeftUp } from "lucide-react"
import { SearchField, SegmentedControl, SegmentedControlItem, SourceList, SourceRow } from "@/components/kit/list"
import { Button } from "@/components/ui/button"
import type { Machine } from "@/features/machines/queries"
import { api, ApiError, routeMissing } from "@/lib/api"
import type { ProjectRow } from "@/lib/api-types"
import { basename } from "@/lib/format"
import { cn } from "@/lib/utils"
import { object, string } from "@/lib/validate"
import { isFolderPath, matchingFolders, parentFolderInput, splitFolderPath } from "./folder-path"
import { foldersQuery } from "./queries"
import { placeKey, stubRow } from "./rules"

type FolderChoice = { name: string; go?: string; row?: ProjectRow; git?: string; pack?: boolean; recent?: boolean }

export function useFolderBrowser(machine: Machine | undefined, needsUpdate: boolean, browsing: boolean, query: string, projects: ProjectRow[]) {
  const [selection, setSelection] = useState({ key: "", index: 0 })
  const [showHidden, setShowHidden] = useState(false)
  const machineId = machine?.id ?? ""
  const machineName = machine?.name ?? "this machine"
  const absolute = isFolderPath(query)
  const { dir, segment } = splitFolderPath(query)
  const listing = useQuery({ ...foldersQuery(machineId, dir), enabled: browsing && absolute && machine?.online === true && !needsUpdate })
  const data = listing.data
  const queryClient = useQueryClient()
  const creation = useMutation({
    mutationFn: async ({ machine, parent, name }: { machine: string; dir: string; parent: string; name: string }) => {
      const created = object(await api<unknown>("/api/folders", { machine, body: { parent, name } }), "created folder")
      return string(created.path, "created folder path")
    },
    onSettled: (_, error, { machine, dir }) => {
      if (!error || (error instanceof ApiError && error.status === 409)) {
        void queryClient.invalidateQueries({ queryKey: foldersQuery(machine, dir).queryKey, exact: true })
      }
    },
  })
  const key = `${machineId}\0${query}`
  const choices: FolderChoice[] = []
  const error = browsing && !absolute
    ? "Enter an absolute path or start with ~/."
    : machine && !machine.online
      ? `${machineName} is offline. Choose an online machine to browse.`
      : (needsUpdate || routeMissing(listing.error))
        ? `Update Agentpack on ${machineName} to browse its folders`
        : listing.error instanceof ApiError && listing.error.status === 404
          ? `No folder ${dir} on ${machineName}.`
          : listing.error?.message
  if (browsing && data && !error) {
    const recent = new Map(projects.map((row) => [placeKey(machineId, row.directory), row]))
    if (!segment && data.row) {
      choices.push({ name: basename(data.path) || data.path, row: data.row, git: data.row.git?.branch, recent: recent.has(placeKey(machineId, data.path)) })
    }
    for (const entry of matchingFolders(data.entries, segment, showHidden)) {
      const path = data.path ? withSlash(data.path) + entry.name : entry.name
      choices.push({ ...entry, go: withSlash(dir + entry.name), recent: recent.has(placeKey(machineId, path)) })
    }
  }
  const active = selection.key === key && choices.length ? Math.min(selection.index, choices.length - 1) : 0
  return {
    active, browsing, choices, creation, listing: data, dir, segment, error, machineId, machineName,
    showHidden, setShowHidden, loading: listing.isFetching,
    retry: listing.isError && !(listing.error instanceof ApiError && listing.error.status === 404) ? listing.refetch : undefined,
    moved: selection.key === key,
    select: (index: number) => setSelection({ key, index }),
    reset: () => setSelection({ key: "", index: 0 }),
  }
}

export function FolderBrowser({ browser, query, setQuery, setBrowsing, pick, filters, children, className, done }: {
  browser: ReturnType<typeof useFolderBrowser>
  query: string
  setQuery: (query: string) => void
  setBrowsing: (browse: boolean) => void
  pick: (machine: string, row: ProjectRow) => void
  filters: ReactNode
  children: ReactNode
  className?: string
  done?: () => void
}) {
  const id = useId()
  const aside = useRef<HTMLElement>(null)
  const { active, browsing, choices, creation, listing: data, dir, segment, error, machineId, machineName, moved, select, reset } = browser
  const canCreate = Boolean(!error && data?.path && segment.trim() && segment !== "." && segment !== ".." && !data.entries.some((entry) => entry.name === segment))
  const parent = segment ? dir : parentFolderInput(dir) ?? (data?.parent ? withSlash(data.parent) : undefined)
  const createError = creation.variables?.machine === machineId && creation.variables.dir === dir && creation.variables.name === segment
    ? creation.error : null

  useEffect(() => {
    if (browsing && moved && choices.length) {
      document.getElementById(`${id}-${active}`)?.scrollIntoView({ block: "nearest" })
    }
  }, [active, browsing, choices.length, id, moved])

  function commit(choice: FolderChoice) {
    reset()
    if (choice.go) setQuery(choice.go)
    else if (choice.row) pick(machineId, choice.row)
    aside.current?.querySelector("input")?.focus()
  }

  function createFolder() {
    if (!canCreate || !data || creation.isPending) return
    creation.mutate({ machine: machineId, dir, parent: data.path, name: segment }, {
      onSuccess: (path) => {
        reset()
        setQuery(withSlash(path))
        pick(machineId, stubRow(path))
      },
      onSettled: () => requestAnimationFrame(() => aside.current?.querySelector("input")?.focus()),
    })
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (!browsing) {
      if (event.key === "Enter" && !event.metaKey && !event.ctrlKey) event.preventDefault()
      return
    }
    if (event.key === "Escape") {
      event.preventDefault()
      event.stopPropagation()
      setBrowsing(false)
    } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault()
      if (choices.length) {
        const step = event.key === "ArrowDown" ? 1 : -1
        select((active + step + choices.length) % choices.length)
      }
    } else if ((event.key === "Enter" && !event.ctrlKey && !event.metaKey) || (event.key === "Tab" && !event.shiftKey)) {
      const choice = choices[active]
      if (event.key === "Tab" && !choice?.go) return
      event.preventDefault()
      if (choice) commit(choice)
    } else if (event.key === "Backspace" && !event.metaKey && !event.ctrlKey && event.currentTarget.selectionStart === query.length) {
      const parent = parentFolderInput(query)
      if (parent) {
        event.preventDefault()
        reset()
        setQuery(parent)
      }
    }
  }

  return (
    <aside ref={aside} className={cn("flex min-h-0 flex-col gap-(--s3) border-r-[0.5px] border-(--sep) bg-(--sidebar) pt-(--s5) max-[760px]:border-r-0", className)}>
      <div className="flex min-w-0 items-center gap-(--s2) px-(--s5)">
        <SegmentedControl aria-label="Find a project" className="min-w-0 flex-1" value={[browsing ? "browse" : "search"]}
          onValueChange={([value]) => { if (value) setBrowsing(value === "browse") }}>
          <SegmentedControlItem value="search" className="flex-1" disabled={creation.isPending}>Search projects</SegmentedControlItem>
          <SegmentedControlItem value="browse" className="flex-1" disabled={!machineId || creation.isPending}>Browse folders</SegmentedControlItem>
        </SegmentedControl>
        {done && <Button type="button" size="sm" className="min-[761px]:hidden" disabled={creation.isPending} onClick={done}>Done</Button>}
      </div>
      {filters}
      {!machineId && <p className="px-(--s5) text-(length:--fs-xs) text-(--fg2)">Choose a machine to browse its folders.</p>}
      <div className="px-(--s5)">
        <SearchField
          type="text"
          aria-label={browsing ? "Folder path" : "Search projects"}
          placeholder={browsing ? "Folder path" : "Search recent and found projects"}
          autoComplete="off"
          spellCheck={false}
          aria-controls={browsing ? id : undefined}
          disabled={creation.isPending}
          value={query}
          onChange={(event) => {
            reset()
            setQuery(event.target.value)
          }}
          onKeyDown={onKeyDown}
        />
      </div>
      {browsing ? (
        <SourceList id={id} aria-label="Folders" className="flex-1 px-(--s3)">
          <div className="flex flex-wrap items-center justify-between gap-(--s2) px-(--s3) pb-(--s2)">
            <p aria-label="Current folder" className="min-w-0 wrap-anywhere text-(length:--fs-xs) text-(--fg2)">{machineName} › {data?.path ?? dir}</p>
            {parent && <Button type="button" size="sm" disabled={creation.isPending} onClick={() => setQuery(parent)}>
              <CornerLeftUp aria-hidden className="size-4" /> Go up
            </Button>}
          </div>
          {error ? <>
            <p role="alert" className="px-(--s3) wrap-anywhere text-(--fg2)">{error}</p>
            {browser.retry && <Button type="button" size="sm" disabled={browser.loading} onClick={() => void browser.retry?.()}>Retry</Button>}
          </> : (
            <>
              {!data && <p role="status" className="px-(--s3) text-(--fg2)">Loading folders on {machineName}…</p>}
              {data?.entries.some((entry) => entry.hidden) && (
                <label className="flex items-center gap-(--s2) px-(--s3) py-(--s2) text-(length:--fs-xs) text-(--fg2)">
                  <input type="checkbox" checked={browser.showHidden} onChange={(event) => browser.setShowHidden(event.target.checked)} />
                  Show hidden folders
                </label>
              )}
              {choices.map((choice, index) => (
                <SourceRow key={choice.name + index} id={`${id}-${index}`} type="button" selected={active === index} disabled={creation.isPending} onClick={() => commit(choice)}>
                  <Folder aria-hidden className="size-4" />
                  <span className="min-w-0 flex-1 truncate">
                    {!choice.go && <strong>Use this folder </strong>}{choice.name}
                  </span>
                  {[choice.git, choice.pack && "agentpack", choice.recent && "project"].filter(Boolean).map((tag, i) => (
                    <span key={i} className="max-w-20 truncate rounded-(--r-ctl) bg-(--track) px-(--s2) text-(length:--fs-xs) text-(--fg2)">{tag}</span>
                  ))}
                </SourceRow>
              ))}
              {canCreate && (
                <Button type="button" size="sm" className="w-full justify-start" disabled={creation.isPending} onClick={createFolder}>
                  <span className="truncate">{creation.isPending ? "Creating folder…" : `Create folder “${segment}”`}</span>
                </Button>
              )}
              {createError && <p role="alert" className="px-(--s3) text-(--fg2)">
                {routeMissing(createError) ? `Update Agentpack on ${machineName} to create folders.` : createError.message}
              </p>}
              {data && !choices.length && <p className="px-(--s3) wrap-anywhere text-(--fg2)">{segment ? `No folder starting with “${segment}” on ${machineName}.` : "No folders here."}</p>}
              {data?.truncated && <p className="px-(--s3) text-(length:--fs-xs) text-(--fg2)">Showing the first 1,000 folders.</p>}
            </>
          )}
        </SourceList>
      ) : children}
    </aside>
  )
}

function withSlash(path: string): string {
  return /[\\/]$/.test(path) ? path : path + (/^[A-Za-z]:\\/.test(path) || path.startsWith("\\\\") ? "\\" : "/")
}
