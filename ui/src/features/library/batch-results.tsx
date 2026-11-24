import { cn } from "@/lib/utils"

export type BatchRowState = "pending" | "running" | "done" | "failed"

export type BatchRow = {
  key: string
  title: string
  detail: string
  trailing?: string
  state: BatchRowState
  statusText: string
}

// Shared per-target plan/result list for Install, Update and Add package.
// Successful rows remain visible; error messages wrap in full instead of truncating.
export function BatchResults({
  label,
  rows,
  summary,
}: {
  label: string
  rows: BatchRow[]
  summary?: string
}) {
  return (
    <div className="flex min-w-0 flex-col gap-(--s2)">
      {summary && (
        <p className="text-(length:--fs-sm) font-medium text-(--fg2)" aria-live="polite">
          {summary}
        </p>
      )}
      <ul aria-label={label} className="flex max-h-96 flex-col overflow-y-auto">
        {rows.map((row) => (
          <li
            key={row.key}
            className="flex min-h-11 min-w-0 flex-col justify-center gap-(--s1) border-b-[0.5px] border-(--sep-soft) py-(--s2) last:border-b-0"
          >
            <span className="flex min-w-0 items-baseline gap-(--s3)">
              <span className="min-w-0 flex-1 break-words font-medium">{row.title}</span>
              {row.trailing && (
                <span className="shrink-0 text-(length:--fs-sm) text-(--fg2) tabular-nums">{row.trailing}</span>
              )}
            </span>
            <span className="break-all text-(length:--fs-sm) text-(--fg2)" title={row.detail}>
              {row.detail}
            </span>
            {row.statusText && (
              <span
                className={cn(
                  "break-words whitespace-normal text-(length:--fs-sm)",
                  row.state === "failed" ? "text-(--danger)" : "text-(--fg2)",
                )}
                aria-live="polite"
              >
                {row.statusText}
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  )
}
