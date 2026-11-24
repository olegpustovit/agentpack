import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog"

export function QuickLook({ src, label, onClose }: { src: string; label: string; onClose: () => void }) {
  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent
        data-slot="quick-look"
        showCloseButton={false}
        className="block w-fit max-w-none bg-transparent p-0 ring-0 sm:max-w-none"
        onKeyDown={(event) => {
          if (event.key !== " ") return
          event.preventDefault()
          onClose()
        }}
      >
        <DialogTitle className="sr-only">{label}</DialogTitle>
        <img
          src={src}
          alt=""
          className="max-h-[85dvh] max-w-[min(90vw,960px)] rounded-(--r-pop) border-[0.5px] border-(--sep) object-contain"
        />
      </DialogContent>
    </Dialog>
  )
}
