import { useState } from "react"
import { Section } from "@/components/kit/data"
import { fmt, metrics, type Metric, type UsageHour, type Window } from "./model"

const HOURS = Array.from({ length: 24 }, (_, hour) => hour)
const hourName = (hour: number) => new Date(2000, 0, 1, hour).toLocaleTimeString([], { hour: "numeric" })

type Row = { key: string; label: string; color: string; cells: number[]; reported: Set<number> }

// Hour by hour: days × hours for a week or month; agents × hours for a single day.
// Intensity is a square root, so one huge hour doesn't wash out the rest.
export function UsageHeatmap({ rows, win, metric, series, name }: {
  rows: UsageHour[]
  win: Window
  metric: Metric
  series: { key: string }[]
  name: (key: string) => string
}) {
  const [selectedCell, setSelectedCell] = useState<string>()
  const { value, kind, label } = metrics[metric]
  const day = win.range === "day"
  const dayIndex = (ms: number) => {
    for (let i = win.starts.length - 1; i >= 0; i--) if (ms >= win.starts[i]!) return i
    return -1
  }
  const grid: Row[] = day
    ? series.map((entry) => ({ key: entry.key, label: name(entry.key), color: "var(--fg)", cells: HOURS.map(() => 0), reported: new Set<number>() }))
    : win.starts.flatMap((start, index) => start > Date.now() ? [] : [{ key: String(index), label: `${new Date(start).toLocaleDateString([], { weekday: "short" })} ${new Date(start).getDate()}`, color: "var(--fg)", cells: HOURS.map(() => 0), reported: new Set<number>() }])
  for (const row of rows) {
    const ms = row.hour * 1000
    if (ms < win.starts[0]! || ms >= win.end) continue
    const hour = new Date(ms).getHours()
    const target = day ? grid.find((entry) => entry.key === row.harness) : grid.find((entry) => entry.key === String(dayIndex(ms)))
    if (target && (metric !== "cost" || row.cost_micros >= 0)) {
      target.cells[hour] = (target.cells[hour] ?? 0) + value(row)
      target.reported.add(hour)
    }
  }
  const max = Math.max(1e-9, ...grid.flatMap((entry) => entry.cells))
  const profile = HOURS.map((hour) => grid.reduce((sum, entry) => sum + entry.cells[hour]!, 0))
  const peakProfile = Math.max(1e-9, ...profile)
  const busiest = grid.flatMap((entry) => entry.cells.map((cell, hour) => ({ entry, hour, cell }))).sort((a, b) => b.cell - a.cell)[0]
  const active = grid.reduce((sum, entry) => sum + entry.cells.filter((cell) => cell > 0).length, 0)
  const peakHour = profile.indexOf(peakProfile)
  const cellHeight = day ? 18 : win.starts.length > 10 ? 9 : 16
  const columns = "grid-cols-[minmax(56px,max-content)_repeat(24,minmax(0,1fr))]"

  return (
    <Section title="Hour by hour">
      <p className="-mt-(--s2) mb-(--s4) text-(length:--fs-xs) text-(--fg2) tabular-nums" aria-live="polite">
        {selectedCell ?? (busiest && busiest.cell > 0 ? `Busiest hour ${busiest.entry.label}, ${hourName(busiest.hour)} (${fmt(busiest.cell, kind)}). Usually busiest around ${hourName(peakHour)}. ${active} active hours.` : "No activity")}
      </p>
      <div role="group" aria-label={`${label} by hour of day, ${day ? "per agent" : "per day"}. Busiest at ${busiest ? hourName(busiest.hour) : "none"}.`} className={`grid ${columns} gap-[2px] select-none`}>
        <span />
        {HOURS.map((hour) => (
          <span key={hour} className="overflow-visible text-[10px] leading-4 whitespace-nowrap text-(--fg3) tabular-nums">{hour % 6 === 0 ? hourName(hour) : ""}</span>
        ))}
        {grid.map((entry) => (
          <div key={entry.key} className="contents">
            <span className="truncate pe-(--s3) text-(length:--fs-xs) leading-none text-(--fg2) self-center">{entry.label}</span>
            {entry.cells.map((cell, hour) => {
              const cellLabel = `${entry.label} · ${hourName(hour)} · ${metric === "cost" && !entry.reported.has(hour) ? "Not reported" : fmt(cell, kind)}`
              return (
                <button
                  key={`${win.range}:${win.starts[0]}:${hour}`}
                  type="button"
                  aria-label={cellLabel}
                  title={cellLabel}
                  onFocus={() => setSelectedCell(cellLabel)}
                  onClick={() => setSelectedCell(cellLabel)}
                  className="cell-in rounded-[3px] border-0 p-0 outline-none focus-visible:ring-2 focus-visible:ring-(--accent)"
                  style={{
                    height: cellHeight,
                    background: cell > 0 ? `color-mix(in oklab, ${entry.color} ${Math.round(10 + Math.sqrt(cell / max) * 70)}%, var(--track))` : "var(--track)",
                    animationDelay: `${Math.min(hour * 6, 140)}ms`,
                  }}
                />
              )
            })}
          </div>
        ))}
        <span className="pt-(--s3) pe-(--s3) text-(length:--fs-xs) text-(--fg2)">Typical day</span>
        {profile.map((sum, hour) => (
          <span key={hour} className="flex h-9 items-end pt-(--s3)" title={`${hourName(hour)} · ${metric === "cost" && !grid.some((entry) => entry.reported.has(hour)) ? "Not reported" : fmt(sum, kind)}`}>
            <span className="bar-grow w-full rounded-t-[2px] bg-(--fg3)/55" style={{ height: `${Math.max(sum > 0 ? 6 : 0, (sum / peakProfile) * 100)}%` }} />
          </span>
        ))}
      </div>
    </Section>
  )
}

// What the tokens were: cache reads dominate long chats, output is the work.
export function TokenMix({ rows }: { rows: UsageHour[] }) {
  const parts = [
    { key: "Output", value: rows.reduce((sum, row) => sum + row.output_tokens, 0), color: "var(--accent)" },
    { key: "Input", value: rows.reduce((sum, row) => sum + row.input_tokens, 0), color: "var(--series-3)" },
    { key: "Cache write", value: rows.reduce((sum, row) => sum + row.cache_write_tokens, 0), color: "var(--series-4)" },
    { key: "Cache read", value: rows.reduce((sum, row) => sum + row.cache_read_tokens, 0), color: "color-mix(in oklab, var(--fg3) 45%, var(--track))" },
  ]
  const total = parts.reduce((sum, part) => sum + part.value, 0) || 1
  return (
    <Section title="Token mix">
      <div className="flex h-2.5 w-full overflow-hidden rounded-full bg-(--track)" role="img" aria-label={parts.map((part) => `${part.key} ${Math.round((part.value / total) * 100)}%`).join(", ")}>
        {parts.map((part) => (
          <span key={part.key} className="h-full transition-[width] duration-(--dur-3) ease-(--ease) not-last:border-r-2 not-last:border-(--page)" style={{ width: `${(part.value / total) * 100}%`, background: part.color }} />
        ))}
      </div>
      <dl className="mt-(--s4) grid grid-cols-2 gap-x-(--s6) gap-y-(--s3) text-(length:--fs-sm)">
        {parts.map((part) => (
          <div key={part.key} className="flex min-w-0 items-center gap-(--s3)">
            <span aria-hidden className="size-2.5 shrink-0 rounded-[3px]" style={{ background: part.color }} />
            <dt className="min-w-0 flex-1 truncate text-(--fg2)">{part.key}</dt>
            <dd className="font-semibold tabular-nums">{fmt(part.value)}</dd>
            <dd className="w-10 text-end text-(--fg3) tabular-nums">{`${((part.value / total) * 100).toFixed(part.value / total < 0.1 ? 1 : 0)}%`}</dd>
          </div>
        ))}
      </dl>
    </Section>
  )
}
