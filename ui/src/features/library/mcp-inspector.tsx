import { Plug } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Inspector, InspectorHead, KeyValue, Section } from "@/components/kit/data"
import { plural } from "@/lib/format"
import { mcpUses } from "./inspect"
import { Failures, MissingItem } from "./inspector-state"
import type { MCPItem } from "./model"
import { useLibrary } from "./use-library"

// Lock statuses from pack.lock. Unknown values show as recorded.
const statusWords: Record<string, string> = { pinned: "Pinned", unpinned: "Unpinned", unpinnable: "Unpinnable" }

// Launch commands, environment and headers never leave the machine, so the inspector has none to show.
export function McpInspector({ name }: { name: string }) {
  const { inputs, items, loading, failures } = useLibrary()
  const server = items.find((entry): entry is MCPItem => entry.type === "mcp" && entry.name === name)
  if (!server) {
    return (
      <MissingItem loading={loading} failures={failures} title="MCP server not found">
        {`No project on any machine uses ${name}.`}
      </MissingItem>
    )
  }

  const uses = mcpUses(inputs, server)
  const statuses = [...new Set(uses.map((use) => statusWords[use.status] ?? use.status))]

  return (
    <Inspector>
      <Failures failures={failures} />
      <InspectorHead
        icon={<Plug aria-hidden />}
        title={server.name}
        subtitle={`${server.launcher} · ${plural(server.projects.length, "project")} · ${server.machines.join(", ")}`}
      />
      <Section title="Server" className="col-span-full">
        <dl className="flex flex-col gap-(--s3)">
          <KeyValue label="Launcher">{server.launcher || "—"}</KeyValue>
          <KeyValue label="Status">{statuses.join(", ") || "—"}</KeyValue>
          <KeyValue label="Version">{server.version || "Not pinned"}</KeyValue>
        </dl>
      </Section>
      <Section title="Used in" className="col-span-full">
        <p className="mb-(--s2) text-(length:--fs-sm) text-(--fg2)">{plural(uses.length, "project")}</p>
        <DataGrid>
          <DataGridHead>
            <tr>
              <DataGridHeaderCell className="w-40">Machine</DataGridHeaderCell>
              <DataGridHeaderCell>Project</DataGridHeaderCell>
              <DataGridHeaderCell className="w-28">Status</DataGridHeaderCell>
              <DataGridHeaderCell className="w-56">Integrity</DataGridHeaderCell>
            </tr>
          </DataGridHead>
          <tbody>
            {uses.map((use) => (
              <DataGridRow key={`${use.machine}\0${use.directory}`}>
                <DataGridCell>{use.machine}</DataGridCell>
                <DataGridCell title={use.directory}>{use.project}</DataGridCell>
                <DataGridCell>{statusWords[use.status] ?? use.status}</DataGridCell>
                <DataGridCell title={use.integrity}>
                  <code>{use.integrity || "—"}</code>
                </DataGridCell>
              </DataGridRow>
            ))}
          </tbody>
        </DataGrid>
      </Section>
    </Inspector>
  )
}
