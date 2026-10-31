import { useState } from "react"
import { Puzzle, Sparkles } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Hash, Inspector, InspectorHead, KeyValue, Section } from "@/components/kit/data"
import { Empty } from "@/components/kit/feedback"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { plural, shortHash } from "@/lib/format"
import { installedIn, packageRecord, packageVersions } from "./inspect"
import { Failures, MissingItem } from "./inspector-state"
import type { PackageItem } from "./model"
import { useLibrary } from "./use-library"

export function PackageInspector({ module }: { module: string }) {
  const { inputs, items, loading, failures } = useLibrary()
  const [picked, setPicked] = useState<string>()
  const item = items.find(
    (entry): entry is PackageItem =>
      (entry.type === "skill" || entry.type === "plugin") && entry.module.toLowerCase() === module.toLowerCase(),
  )
  if (!item) {
    return (
      <MissingItem loading={loading} failures={failures} title="Package not found">
        {`No machine's library has ${module}. A machine that is offline may still hold it.`}
      </MissingItem>
    )
  }

  const commit = picked ?? item.commit
  const versions = packageVersions(inputs, item.module, item.commit)
  const record = packageRecord(inputs, item.module, commit)
  const installed = installedIn(inputs, item.module)
  const kind = item.type === "plugin" ? "Plugin" : "Skill"
  const Glyph = item.type === "plugin" ? Puzzle : Sparkles

  return (
    <Inspector>
      <Failures failures={failures} />
      <InspectorHead
        icon={<Glyph className="size-(--s6)" />}
        title={item.name}
        subtitle={`${kind} · ${plural(item.projects.length, "project")} · ${item.machines.join(", ")}`}
      />
      <Section title="Details">
        <dl className="flex flex-col gap-(--s3)">
          <KeyValue label="Module">
            <code>{item.module}</code>
          </KeyValue>
          <KeyValue label="Kind">{kind}</KeyValue>
          <KeyValue label="Commit">
            <span className="flex items-center gap-(--s3)">
              <Hash value={commit} />
              {versions.length > 1 && (
                <Menu>
                  <MenuTrigger render={<Button variant="ghost" size="sm" />}>{plural(versions.length, "version")}</MenuTrigger>
                  <MenuContent align="start">
                    {versions.map((version) => (
                      <MenuItem key={version} aria-current={version === commit ? "true" : undefined} onClick={() => setPicked(version)}>
                        <code>{shortHash(version)}</code>
                      </MenuItem>
                    ))}
                  </MenuContent>
                </Menu>
              )}
            </span>
          </KeyValue>
          <KeyValue label="Repository">{record?.url || "—"}</KeyValue>
          <KeyValue label="Content hash">{record?.content_hash ? <code className="break-all">{record.content_hash}</code> : "—"}</KeyValue>
        </dl>
      </Section>
      <Section title="Installed in" className="col-span-full">
        {installed.length === 0 ? (
          <Empty title="Not installed yet">No project on any machine pins this package.</Empty>
        ) : (
          <>
            <p className="mb-(--s2) text-(--fs-sm) text-(--fg2)">{plural(installed.length, "project")}</p>
            <DataGrid>
              <DataGridHead>
                <tr>
                  <DataGridHeaderCell className="w-40">Machine</DataGridHeaderCell>
                  <DataGridHeaderCell>Project</DataGridHeaderCell>
                  <DataGridHeaderCell className="w-28">Pin</DataGridHeaderCell>
                  <DataGridHeaderCell className="w-32">Status</DataGridHeaderCell>
                </tr>
              </DataGridHead>
              <tbody>
                {installed.map((row) => (
                  <DataGridRow key={row.key}>
                    <DataGridCell>{row.machine}</DataGridCell>
                    <DataGridCell title={row.directory}>{row.project}</DataGridCell>
                    <DataGridCell>
                      <Hash value={row.commit} />
                    </DataGridCell>
                    <DataGridCell>{row.commit === commit ? "Current" : "Other version"}</DataGridCell>
                  </DataGridRow>
                ))}
              </tbody>
            </DataGrid>
          </>
        )}
      </Section>
    </Inspector>
  )
}
