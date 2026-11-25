import type { ComponentProps, ReactNode } from "react"
import { ListFilter, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

// Linear-style view filters: a toggle button that tints while filters are on, facets of count chips
// inside its popover, and removable tokens that keep active filters visible after it closes.

export function FilterButton({ active, className, ...props }: ComponentProps<typeof Button> & { active: boolean }) {
  return (
    <Button variant="ghost" size="icon" aria-haspopup="dialog" {...props}
      className={cn(active && "bg-(--accent-tint) text-(--accent) hover:bg-(--accent-tint)", className)}>
      <ListFilter aria-hidden />
    </Button>
  )
}

export function FilterFacet({ label, active, onClear, children }: { label: string; active: boolean; onClear: () => void; children: ReactNode }) {
  return (
    <div role="group" aria-label={label} className="flex min-w-0 flex-col gap-(--s3)">
      <div className="flex h-(--ctl-h-sm) items-center">
        <span aria-hidden className="flex-1 text-(length:--fs-xs) font-semibold text-(--fg2)">{label}</span>
        {active && <button type="button" aria-label={`Any ${label.toLowerCase()}`} onClick={onClear} className="rounded-(--r-xs) px-(--s1) text-(length:--fs-xs) text-(--accent) hover:underline">Any</button>}
      </div>
      <div className="flex flex-wrap gap-(--s2)">{children}</div>
    </div>
  )
}

export function FilterChip({ pressed, count, onClick, children }: { pressed: boolean; count: number; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" aria-pressed={pressed} onClick={onClick}
      className={cn(
        "group/chip inline-flex h-(--ctl-h-sm) items-center gap-(--s2) rounded-(--r-pill) px-(--s4) text-(length:--fs-sm) transition-colors duration-(--dur-1)",
        "bg-(--sunken) hover:bg-(--hover) aria-pressed:bg-(--accent-tint) aria-pressed:text-(--accent) aria-pressed:shadow-[inset_0_0_0_.5px_var(--accent)]",
        count === 0 && !pressed ? "text-(--fg3)" : "text-(--fg)",
      )}>
      {children}
      <span className="text-(--fg3) tabular-nums group-aria-pressed/chip:text-(--accent)">{count}</span>
    </button>
  )
}
// One active filter: clicking it reopens the view options, × drops just that dimension.
export function FilterToken({ label, value, onOpen, onRemove }: { label: string; value: string; onOpen: () => void; onRemove: () => void }) {
  return (
    <span className="inline-flex h-(--ctl-h-sm) max-w-full min-w-0 items-center rounded-(--r-pill) bg-(--accent-tint) text-(length:--fs-sm) text-(--accent)">
      <button type="button" title={`${label}: ${value}`} aria-label={`${label}: ${value}. Edit filters`} onClick={onOpen}
        className="min-w-0 truncate rounded-s-(--r-pill) ps-(--s4) pe-(--s1) font-medium">
        {value}
      </button>
      <button type="button" aria-label={`Remove ${label.toLowerCase()} filter`} onClick={onRemove}
        className="grid size-(--ctl-h-sm) shrink-0 place-items-center rounded-e-(--r-pill) hover:bg-(--accent-tint)">
        <X aria-hidden className="size-3" />
      </button>
    </span>
  )
}
