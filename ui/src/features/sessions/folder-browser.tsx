import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Folder, CornerLeftUp } from "lucide-react"
import { GroupHeader, SearchField, SourceList, SourceRow } from "@/components/kit/list"
import { Button } from "@/components/ui/button"
import type { Machine } from "@/features/machines/queries"
import { ApiError, LOCAL, routeMissing } from "@/lib/api"
import type { ProjectRow } from "@/lib/api-types"
import { basename } from "@/lib/format"
import { isFolderPath, matchingFolders, parentFolderInput, splitFolderPath } from "./folder-path"
import { foldersQuery } from "./queries"
import { placeKey, stubRow } from "./rules"

type FolderChoice = { name: string; go?: string; row?: ProjectRow; git?: string; pack?: boolean; recent?: boolean }

export function useFolderBrowser(machine: Machine | undefined, needsUpdate: boolean, query: string, projects: ProjectRow[]) {
  const [selection, setSelection] = useState({ key: "", index: 0 })
  const browsing = isFolderPath(query)
  const machineId = machine?.id ?? LOCAL
  const machineName = machine?.name ?? "this machine"
  const { dir, segment } = splitFolderPath(query)
  const listing = useQuery({ ...foldersQuery(machineId, dir), enabled: browsing && machine?.online === true && !needsUpdate })
  const data = listing.data
  const key = `${machineId}\0${query}`
  const choices: FolderChoice[] = []
  const error = machine && !machine.online
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
      if (data.parent) choices.push({ name: "..", go: parentFolderInput(dir) ?? withSlash(data.parent) })
    }
    for (const entry of matchingFolders(data.entries, segment)) {
      const path = data.path ? withSlash(data.path) + entry.name : entry.name
      const prior = recent.get(placeKey(machineId, path))
      const row = prior?.available ? prior : {
        ...stubRow(path),
        git: entry.git ? { head: "", branch: entry.git, dirty: 0 } : undefined,
      }
      choices.push({ ...entry, go: withSlash(dir + entry.name), row, recent: Boolean(prior) })
    }
  }
  const active = selection.key === key && choices.length ? Math.min(selection.index, choices.length - 1) : 0
  return {
    active, browsing, choices, listing: data, dir, segment, error, machineId,
    preview: choices[active]?.row,
    moved: selection.key === key,
    select: (index: number) => setSelection({ key, index }),
    reset: () => setSelection({ key: "", index: 0 }),
  }
}

export function FolderBrowser({ browser, query, setQuery, pick, filters, children }: {
  browser: ReturnType<typeof useFolderBrowser>
  query: string
  setQuery: (query: string) => void
  pick: (machine: string, row: ProjectRow) => void
  filters: ReactNode
  children: ReactNode
}) {
  const id = useId()
  const aside = useRef<HTMLElement>(null)
  const { active, browsing, choices, listing: data, dir, segment, error, machineId, moved, select, reset } = browser

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

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (!browsing) {
      if (event.key === "Enter" && !event.metaKey && !event.ctrlKey) event.preventDefault()
      return
    }
    if (event.key === "Escape") {
      event.preventDefault()
      event.stopPropagation()
      setQuery("")
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
    <aside ref={aside} className="flex min-h-0 flex-col gap-(--s3) border-r-[0.5px] border-(--sep) bg-(--sidebar) pt-(--s4) max-[760px]:border-r-0 max-[760px]:border-b-[0.5px]">
      <div className="px-(--s4)">
        <SearchField
          type="text"
          aria-label="Search projects"
          placeholder="Search projects, or type a path"
          autoComplete="off"
          spellCheck={false}
          aria-controls={browsing ? id : undefined}
          value={query}
          onChange={(event) => {
            reset()
            setQuery(event.target.value)
          }}
          onKeyDown={onKeyDown}
        />
      </div>
      {filters}
      {browsing ? (
        <SourceList id={id} aria-label="Folders" className="flex-1 px-(--s3)">
          <GroupHeader><span className="truncate" title={dir}>{dir}</span></GroupHeader>
          {error ? <p role="alert" className="px-(--s3) text-(--fg2)">{error}</p> : (
            <>
              {!data && <p className="px-(--s3) text-(--fg2)">Loading folders…</p>}
              {choices.map((choice, index) => (
                <SourceRow key={choice.name + index} id={`${id}-${index}`} type="button" selected={active === index} onClick={() => commit(choice)}>
                  {choice.name === ".." ? <CornerLeftUp aria-hidden className="size-4" /> : <Folder aria-hidden className="size-4" />}
                  <span className="min-w-0 flex-1 truncate">
                    {!choice.go && <strong>Use this folder </strong>}{choice.name}
                  </span>
                  {[choice.git, choice.pack && "agentpack", choice.recent && "project"].filter(Boolean).map((tag, i) => (
                    <span key={i} className="max-w-20 truncate rounded-(--r-ctl) bg-(--track) px-(--s2) text-(length:--fs-xs) text-(--fg2)">{tag}</span>
                  ))}
                </SourceRow>
              ))}
              {data && !choices.length && <p className="px-(--s3) text-(--fg2)">{segment ? `No folder starting with “${segment}”.` : "No folders here."}</p>}
              {data?.truncated && <p className="px-(--s3) text-(length:--fs-xs) text-(--fg2)">Showing the first 1,000 folders.</p>}
            </>
          )}
        </SourceList>
      ) : children}
      <div className="border-t-[0.5px] border-(--sep) px-(--s3) pb-(--s2)">
        <Button type="button" variant="ghost" size="sm" className="w-full justify-start" onClick={() => {
          setQuery("~/")
          reset()
          aside.current?.querySelector("input")?.focus()
        }}>
          <Folder aria-hidden className="size-4" /> Open folder…
        </Button>
      </div>
    </aside>
  )
}

function withSlash(path: string): string {
  return /[\\/]$/.test(path) ? path : path + (/^[A-Za-z]:\\/.test(path) || path.startsWith("\\\\") ? "\\" : "/")
}
