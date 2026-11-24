import { useRef, type ComponentProps, type ReactNode } from "react";
import { Ellipsis } from "lucide-react";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  ContextMenu,
  ContextMenuCheckboxItem,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { Kbd } from "@/components/kit/feedback";

// Sheet drops from the toolbar. Phone turns it into a bottom sheet.
export const Sheet = Dialog;

export function SheetContent({ className, children, ...props }: ComponentProps<typeof DialogContent>) {
  return (
    <DialogContent
      showCloseButton={false}
      finalFocus={() => document.querySelector<HTMLElement>(".xterm-helper-textarea") ?? true}
      className={cn(
        "duration-(--dur-2) ease-(--ease) data-open:zoom-in-100 data-closed:zoom-out-100 data-open:slide-in-from-top-2 data-closed:slide-out-to-top-1 max-[760px]:data-open:slide-in-from-bottom-full max-[760px]:data-closed:slide-out-to-bottom-full top-[calc(var(--bar-h)+var(--safe-t)+var(--s4))] w-120 max-w-[calc(100vw-var(--s6)*2)] sm:max-w-[calc(100vw-var(--s6)*2)] max-h-[calc(100dvh-var(--bar-h)-var(--safe-t)-var(--s9))] overflow-y-auto translate-y-0 grid-cols-[minmax(0,1fr)] gap-(--dlg-gap) rounded-(--r-sheet) bg-(--raised) p-(--dlg-pad) shadow-(--e-sheet) ring-0 max-[760px]:top-auto max-[760px]:bottom-0 max-[760px]:left-0 max-[760px]:w-full max-[760px]:max-w-none max-[760px]:max-h-[calc(100dvh-var(--safe-t)-var(--s4))] max-[760px]:translate-x-0 max-[760px]:translate-y-0 max-[760px]:rounded-b-none max-[760px]:pb-[calc(var(--dlg-pad)+var(--safe-b))]",
        className,
      )}
      {...props}
    >
      {children}
    </DialogContent>
  );
}

export function SheetTitle({ className, ...props }: ComponentProps<typeof DialogTitle>) {
  return <DialogTitle className={cn("text-(length:--fs-lg) leading-tight font-semibold tracking-[-.01em]", className)} {...props} />;
}

export function SheetDescription({ className, ...props }: ComponentProps<typeof DialogDescription>) {
  return <DialogDescription className={cn("text-(length:--fs-sm) text-(--fg2)", className)} {...props} />;
}

// Cancel first, primary action bottom-right. No close ✕.
// Pinned: the footer stays on the sheet's bottom edge while the body scrolls. Sheets with their own footer bar pass pinned={false}.
export function SheetActions({ cancelLabel = "Cancel", status, pinned = true, children }: { cancelLabel?: string; status?: ReactNode; pinned?: boolean; children: ReactNode }) {
  return (
    <DialogFooter data-slot="sheet-actions" className={cn("mt-(--s2) gap-(--s3) sm:justify-end [&_[data-slot=button]]:min-w-20 max-[760px]:[&_[data-slot=button]]:h-11", pinned && "sticky bottom-[calc(-1*var(--dlg-pad))] z-10 -mx-(--dlg-pad) -mb-(--dlg-pad) border-t-[0.5px] border-(--sep) bg-(--raised) px-(--dlg-pad) py-(--s6) max-[760px]:bottom-[calc(-1*(var(--dlg-pad)+var(--safe-b)))] max-[760px]:-mb-[calc(var(--dlg-pad)+var(--safe-b))] max-[760px]:pb-[calc(var(--s6)+var(--safe-b))]")}>
      {status && <span className="me-auto min-w-0 truncate text-(length:--fs-sm) text-(--fg2) max-[760px]:order-last max-[760px]:text-center" aria-live="polite">{status}</span>}
      <DialogClose render={<Button variant="default">{cancelLabel}</Button>} />
      {children}
    </DialogFooter>
  );
}

export const ConfirmAlert = AlertDialog;

// Centered, 300 px. macOS order (Cancel left, filled red destructive right); stacked on phone with destructive first, Cancel focused.
export function ConfirmAlertContent({
  title,
  description,
  destructiveLabel,
  onConfirm,
  cancelLabel = "Cancel",
}: {
  title: string;
  description: string;
  destructiveLabel: string;
  onConfirm: () => void;
  cancelLabel?: string;
}) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  return (
    <AlertDialogContent className="w-75 max-w-[calc(100vw-var(--s6)*2)] gap-(--s3) rounded-(--r-sheet) bg-(--raised) p-(--dlg-pad) text-center shadow-(--e-sheet) ring-0" initialFocus={cancelRef}>
      <AlertDialogTitle className="line-clamp-2 text-(length:--fs-lg) leading-tight font-semibold tracking-[-.01em]">{title}</AlertDialogTitle>
      <AlertDialogDescription className="text-(length:--fs-sm) text-pretty text-(--fg2)">{description}</AlertDialogDescription>
      <AlertDialogFooter className="mt-(--s5) grid grid-cols-2 gap-(--s3) sm:grid max-[760px]:grid-cols-1 max-[760px]:[&_button]:h-11">
        {/* macOS order: Cancel left, the action right. Stacked on a phone, the action sits above Cancel. */}
        <AlertDialogCancel ref={cancelRef} className="w-full">
          {cancelLabel}
        </AlertDialogCancel>
        <Button variant="destructive" onClick={onConfirm} className="w-full bg-(--danger) text-(--on-accent) shadow-none hover:bg-(--danger)/90 max-[760px]:order-first">
          {destructiveLabel}
        </Button>
      </AlertDialogFooter>
    </AlertDialogContent>
  );
}

export const Menu = DropdownMenu;
export const MenuTrigger = DropdownMenuTrigger;

// macOS 26 NSMenu: 5 px window padding, 12 px corners, translucent material. Shared by menus, context menus and PopupSelect.
export const menuSurface =
  "min-w-44 rounded-(--r-menu) bg-(--menu) p-[5px] text-(length:--fs) text-(--fg) shadow-(--shadow-menu) ring-0 backdrop-blur-xl backdrop-saturate-180";

// 24 px row with a 6 px selection pill; the title sits 16 px from the menu edge. A danger row is red and highlights red.
export const menuRow =
  "h-(--ctl-h) gap-[7px] rounded-[6px] pr-[10px] pl-[11px] py-0 text-(length:--fs) [&_svg:not([class*='size-'])]:size-[15px] focus:bg-(--accent) focus:text-(--accent-fg) focus:**:text-(--accent-fg) data-disabled:opacity-35 data-[danger=true]:text-(--danger-word) data-[danger=true]:focus:bg-(--menu-danger) data-[danger=true]:focus:text-(--accent-fg)";

export function MenuContent({ className, ...props }: ComponentProps<typeof DropdownMenuContent>) {
  return <DropdownMenuContent className={cn(menuSurface, className)} {...props} />;
}

// Icon, label, shortcut; the shortcut turns white with the pill, as in AppKit.
export function MenuItem({
  icon,
  shortcut,
  danger,
  children,
  className,
  ...props
}: ComponentProps<typeof DropdownMenuItem> & { icon?: ReactNode; shortcut?: string; danger?: boolean }) {
  return (
    <DropdownMenuItem data-danger={danger} className={cn(menuRow, className)} {...props}>
      {icon}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {shortcut && <Kbd className="ml-(--s6) font-(family-name:--font)">{shortcut}</Kbd>}
    </DropdownMenuItem>
  );
}

// Base UI leaves a context menu's focus on its list; focus the first enabled item once it opens.
export function RowContextMenu(props: ComponentProps<typeof ContextMenu>) {
  return (
    <ContextMenu
      onOpenChangeComplete={(open) => {
        if (open) document.querySelector<HTMLElement>('[data-slot="context-menu-content"] [role="menuitem"]:not([aria-disabled="true"])')?.focus();
      }}
      {...props}
    />
  );
}

export const RowContextMenuTrigger = ContextMenuTrigger;

export function RowContextMenuContent({ className, ...props }: ComponentProps<typeof ContextMenuContent>) {
  return (
    <ContextMenuContent className={cn(menuSurface, className)} {...props} />
  );
}

export function RowContextMenuItem({
  icon,
  shortcut,
  children,
  className,
  ...props
}: ComponentProps<typeof ContextMenuItem> & { icon?: ReactNode; shortcut?: string }) {
  return (
    <ContextMenuItem className={cn(menuRow, className)} {...props}>
      {icon}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {shortcut && <Kbd className="ml-(--s6) font-(family-name:--font)">{shortcut}</Kbd>}
    </ContextMenuItem>
  );
}

// A toggle row; the check sits at the trailing edge and the menu stays open when it flips.
export function RowContextMenuCheckboxItem({ className, ...props }: ComponentProps<typeof ContextMenuCheckboxItem>) {
  return <ContextMenuCheckboxItem className={cn(menuRow, "pr-8", className)} {...props} />;
}

// Row ⋯ affordance: visible on hover or selection (parent row sets group/row).
export function RowMoreButton({ className, ...props }: ComponentProps<typeof Button>) {
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label="More actions"
      className={cn("size-(--head-h) opacity-0 group-hover/row:opacity-100 group-data-selected/row:opacity-100 focus-visible:opacity-100", className)}
      {...props}
    >
      <Ellipsis className="size-(--fs)" />
    </Button>
  );
}
