import { Button } from "@/components/ui/button"

// One attached image, in the composer and the New session sheet.
export function AttachmentRow({ name, previewUrl, disabled, onRemove }: { name: string; previewUrl: string; disabled?: boolean; onRemove: () => void }) {
  return (
    <div className="flex items-center gap-(--s2) rounded-(--r-ctl) border-[0.5px] border-(--sep) bg-(--window) p-(--s2)">
      <img src={previewUrl} alt="" className="h-12 w-16 rounded-(--r-row) object-cover" />
      <span className="min-w-0 flex-1 truncate text-(length:--fs-xs) text-(--fg2)">Image attached · {name}</span>
      <Button type="button" size="sm" variant="ghost" aria-label={`Remove ${name}`} disabled={disabled} onClick={onRemove}>
        Remove
      </Button>
    </div>
  )
}
