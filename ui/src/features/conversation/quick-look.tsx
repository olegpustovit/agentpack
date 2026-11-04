import { useEffect } from "react"

export function QuickLook({ src, label, onClose }: { src: string; label: string; onClose: () => void }) {
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape" || event.key === " ") {
        event.preventDefault()
        onClose()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [onClose])

  return (
    <div
      role="dialog"
      aria-label={label}
      data-slot="quick-look"
      className="fixed inset-0 z-50 flex items-center justify-center bg-(--scrim) p-(--s6)"
      onClick={onClose}
    >
      <img
        src={src}
        alt=""
        className="max-h-[85vh] max-w-[min(90vw,960px)] rounded-(--r-pop) border-[0.5px] border-(--sep) object-contain"
        onClick={(event) => event.stopPropagation()}
      />
    </div>
  )
}
