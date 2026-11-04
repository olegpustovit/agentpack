import type { ComposerHint } from "./composer-hints"

export function ComposerHintsMenu({
  title,
  hints,
  onPick,
}: {
  title: string
  hints: ComposerHint[]
  onPick: (hint: ComposerHint) => void
}) {
  if (hints.length === 0) return null
  return (
    <div
      data-slot="composer-hints"
      className="absolute bottom-full left-0 z-20 mb-(--s2) w-full max-w-md rounded-(--r-pop) border-[0.5px] border-(--sep) bg-(--chrome) py-(--s2) shadow-(--shadow-pop)"
      role="listbox"
      aria-label={title}
    >
      <p className="px-(--s3) pb-(--s1) text-(length:--fs-xs) text-(--fg3)">{title}</p>
      {hints.map((hint) => (
        <button
          key={hint.value}
          type="button"
          role="option"
          className="flex w-full flex-col items-start gap-(--s1) px-(--s3) py-(--s2) text-left hover:bg-(--hover)"
          onMouseDown={(event) => event.preventDefault()}
          onClick={() => onPick(hint)}
        >
          <code className="font-(family-name:--mono) text-(length:--fs-xs) text-(--fg)">{hint.value}</code>
          <span className="text-(length:--fs-xs) text-(--fg2)">{hint.description}</span>
        </button>
      ))}
    </div>
  )
}
