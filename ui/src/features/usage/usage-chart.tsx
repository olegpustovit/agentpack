import { useEffect, useRef, useState, type KeyboardEvent } from "react"
import { bucketLabel, fmt, type Kind, type Series, type Window } from "./model"

const H = 190
const AXIS = 44 // right axis gutter
const BOTTOM = 18
const TOP = 8

// Rounds the axis maximum to 1, 2, 2.5 or 5 times a power of ten, with about three gridlines.
function axisOf(max: number) {
  const target = max / 3
  const power = 10 ** Math.floor(Math.log10(target))
  const step = [1, 2, 2.5, 5, 10].map((m) => m * power).find((value) => value >= target) ?? power * 10
  const lines = Math.max(1, Math.ceil(max / step))
  return { step, lines, top: step * lines }
}

// Each bucket's non-empty parts, bottom first, with the sum of the parts below each one.
function stacks(series: Series[], count: number) {
  return Array.from({ length: count }, (_, i) => {
    const parts: { key: string; index: number; value: number; below: number }[] = []
    let below = 0
    series.forEach((entry, index) => {
      const value = entry.values[i] ?? 0
      if (value <= 0) return
      parts.push({ key: entry.key, index, value, below })
      below += value
    })
    return parts
  })
}

// Screen Time style: thin rounded stacked bars, dashed average, axis on the right. Scrubbing reports the
// bucket under the pointer so its value moves into the header; a click or Enter pins it.
export function UsageChart({
  win,
  series,
  totals,
  average,
  kind,
  label,
  color,
  hover,
  pinned,
  onHover,
  onPin,
}: {
  win: Window
  series: Series[]
  totals: number[]
  average?: number
  kind: Kind
  label: string
  color: (key: string, index: number) => string
  hover: number | null
  pinned: number | null
  onHover: (index: number | null) => void
  onPin: (index: number | null) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(800)
  useEffect(() => {
    const element = ref.current
    if (!element) return
    const observer = new ResizeObserver(([entry]) => entry && setWidth(Math.max(240, entry.contentRect.width)))
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  const count = win.starts.length
  const { step, lines, top } = axisOf(Math.max(...totals, average ?? 0, 1e-9))
  const slot = (width - AXIS) / count
  const bar = Math.max(2, Math.min(slot - Math.max(2, Math.min(10, slot * 0.3)), win.range === "week" ? 34 : 14))
  const y = (value: number) => TOP + (H - BOTTOM - TOP) * (1 - value / top)
  const band = hover ?? pinned

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const current = pinned ?? -1
    const next = event.key === "ArrowRight" ? Math.min(count - 1, current + 1) : event.key === "ArrowLeft" ? Math.max(0, current < 0 ? count - 1 : current - 1) : undefined
    if (event.key === "Escape") onPin(null)
    else if (next !== undefined) onPin(next)
    else return
    event.preventDefault()
  }

  return (
    <div
      ref={ref}
      tabIndex={0}
      role="group"
      aria-label={`${label}. Arrow keys select a ${win.range === "day" ? "hour" : "day"}; Escape clears.`}
      onKeyDown={onKeyDown}
      onPointerLeave={() => onHover(null)}
      className="mt-(--s5) rounded-(--r-row) outline-none select-none focus-visible:ring-2 focus-visible:ring-(--accent)"
    >
      <svg width={width} height={H} viewBox={`0 0 ${width} ${H}`} role="img" aria-label={label} className="block overflow-visible">
        {Array.from({ length: lines + 1 }, (_, i) => (
          <g key={i}>
            <line x1={0} x2={width - AXIS + 6} y1={y(step * i)} y2={y(step * i)} stroke="var(--sep-soft)" />
            <text x={width - AXIS + 10} y={y(step * i) + 3} fontSize={10} fill="var(--fg3)" className="tabular-nums">{i ? fmt(step * i, kind) : "0"}</text>
          </g>
        ))}
        {band !== null && <rect data-slot="chart-band" x={band * slot} y={TOP - 4} width={slot} height={H - BOTTOM - TOP + 4} rx={4} fill={hover === null ? "var(--accent-soft)" : "var(--hover)"} />}
        {stacks(series, win.starts.length).map((parts, i) => {
          const x = i * slot + (slot - bar) / 2
          const scale = (H - BOTTOM - TOP) / top
          return (
            // Keyed by the window: bars rise once when the range or period changes, never on hover.
            <g key={`${win.range}:${win.starts[0]}:${i}`} className="bar-grow transition-opacity duration-(--dur-1)" style={{ animationDelay: `${Math.min(i * 10, 200)}ms` }} opacity={pinned !== null && pinned !== i ? 0.35 : 1}>
              {parts.map((part, j) => {
                const y0 = H - BOTTOM - (part.below + part.value) * scale
                const height = part.value * scale - (j ? 1.5 : 0) // a hairline of background between stacked fills
                // A sliver thinner than the gap has negative height; clamp so the path stays valid.
                const r = Math.max(0, Math.min(3, bar / 2, height))
                return j === parts.length - 1 ? (
                  <path key={part.key} fill={color(part.key, part.index)} d={`M${x},${y0 + height}V${y0 + r}q0,-${r} ${r},-${r}h${bar - 2 * r}q${r},0 ${r},${r}V${y0 + height}Z`} />
                ) : (
                  <rect key={part.key} fill={color(part.key, part.index)} x={x} y={y0} width={bar} height={Math.max(0, height)} />
                )
              })}
              <text x={i * slot + slot / 2} y={H - 3} textAnchor="middle" fontSize={10} fill="var(--fg3)">{bucketLabel(win, i)}</text>
            </g>
          )
        })}
        {average !== undefined && (
          <>
            <line data-slot="chart-average" x1={0} x2={width - AXIS + 6} y1={y(average)} y2={y(average)} stroke="var(--fg3)" strokeDasharray="3 3" />
            <text x={2} y={y(average) - 4} fontSize={10} fill="var(--fg3)">{`avg ${fmt(average, kind)}`}</text>
          </>
        )}
        {win.starts.map((_, i) => (
          <rect
            key={i}
            data-bucket={i}
            x={i * slot}
            y={0}
            width={slot}
            height={H}
            fill="transparent"
            onPointerEnter={() => onHover(i)}
            onClick={() => {
              onHover(null)
              onPin(pinned === i ? null : i)
            }}
          />
        ))}
      </svg>
    </div>
  )
}
