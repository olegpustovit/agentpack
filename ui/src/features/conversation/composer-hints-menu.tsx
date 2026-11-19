import type { ComposerHint } from "./composer-hints"

// The matched part of a command, underlined.
function Match({ value, query }: { value: string; query: string }) {
  const at = query ? value.toLowerCase().indexOf(query.toLowerCase(), 1) : -1
  if (at < 0) return <>{value}</>
  return (
    <>
      {value.slice(0, at)}
      <span className="underline underline-offset-2">{value.slice(at, at + query.length)}</span>
      {value.slice(at + query.length)}
    </>
  )
}

export function ComposerHintsMenu({
  title,
  hints,
  onPick,
  id,
  activeIndex,
  query = "",
  agentName,
}: {
  id: string
  activeIndex: number
  title: string
  hints: ComposerHint[]
  onPick: (hint: ComposerHint) => void
  query?: string
  agentName?: string
}) {
  if (hints.length === 0) return null
  return (
    <div
      data-slot="composer-hints"
      className="menu-enter absolute bottom-full left-0 z-20 mb-(--s2) max-h-80 w-full max-w-md overflow-y-auto rounded-(--r-pop) border-[0.5px] border-(--sep) bg-(--chrome) py-(--s2) shadow-(--shadow-pop)"
      id={id}
      role="listbox"
      aria-label={title}
    >
      {hints.map((hint, index) => {
        const heading = hint.source && hint.source !== hints[index - 1]?.source
        return (
          <div key={hint.value}>
            {(index === 0 || heading) && (
              <p className="px-(--s3) pt-(--s1) pb-(--s1) text-(length:--fs-xs) text-(--fg3)">
                {hint.source === "gateway" ? "Gateway" : hint.source === "agent" ? agentName ?? title : title}
              </p>
            )}
            <button
              id={`${id}-${index}`}
              type="button"
              role="option"
              tabIndex={-1}
              aria-selected={index === activeIndex}
              className={`flex w-full flex-col items-start gap-(--s1) px-(--s3) py-(--s2) text-left hover:bg-(--hover) ${index === activeIndex ? "bg-(--hover)" : ""}`}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => onPick(hint)}
            >
              <span className="flex min-w-0 items-baseline gap-(--s3)">
                <code className="font-(family-name:--mono) text-(length:--fs-xs) text-(--fg)">
                  {hint.value.startsWith("/") ? <Match value={hint.value} query={query} /> : hint.value}
                </code>
                {hint.hint && <span className="truncate font-(family-name:--mono) text-(length:--fs-xs) text-(--fg3)">{`<${hint.hint}>`}</span>}
              </span>
              {hint.description && <span className="text-(length:--fs-xs) text-(--fg2)">{hint.description}</span>}
            </button>
          </div>
        )
      })}
    </div>
  )
}
