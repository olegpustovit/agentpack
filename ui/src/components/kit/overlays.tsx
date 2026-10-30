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
      className={cn(
        "top-(--bar-h) w-96 max-w-full translate-y-0 gap-(--s5) rounded-(--r-sheet) p-(--s6) max-[760px]:top-auto max-[760px]:bottom-0 max-[760px]:w-full max-[760px]:translate-x-0 max-[760px]:translate-y-0 max-[760px]:rounded-b-none",
        className,
      )}
      {...props}
    >
      {children}
    </DialogContent>
  );
}

export function SheetTitle({ className, ...props }: ComponentProps<typeof DialogTitle>) {
  return <DialogTitle className={cn("text-(--fs) font-semibold", className)} {...props} />;
}

export function SheetDescription({ className, ...props }: ComponentProps<typeof DialogDescription>) {
  return <DialogDescription className={cn("text-(--fs-sm) text-(--fg2)", className)} {...props} />;
}

// Cancel first, primary action bottom-right. No close ✕.
export function SheetActions({ cancelLabel = "Cancel", children }: { cancelLabel?: string; children: ReactNode }) {
  return (
    <DialogFooter className="gap-(--s3) sm:justify-end">
      <DialogClose render={<Button variant="default">{cancelLabel}</Button>} />
      {children}
    </DialogFooter>
  );
}

export const ConfirmAlert = AlertDialog;

// Centered, 280 px. Destructive action is stacked above Cancel, and Cancel gets focus.
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
    <AlertDialogContent className="w-70 gap-(--s4) rounded-(--r-sheet) p-(--s6)" initialFocus={cancelRef}>
      <AlertDialogTitle className="text-(--fs) font-semibold">{title}</AlertDialogTitle>
      <AlertDialogDescription className="text-(--fs-sm) text-(--fg2)">{description}</AlertDialogDescription>
      <AlertDialogFooter className="flex-col gap-(--s3)">
        <Button variant="destructive" onClick={onConfirm} className="w-full">
          {destructiveLabel}
        </Button>
        <AlertDialogCancel ref={cancelRef} className="w-full">
          {cancelLabel}
        </AlertDialogCancel>
      </AlertDialogFooter>
    </AlertDialogContent>
  );
}

export const Menu = DropdownMenu;
export const MenuTrigger = DropdownMenuTrigger;

export function MenuContent({ className, ...props }: ComponentProps<typeof DropdownMenuContent>) {
  return (
    <DropdownMenuContent
      className={cn("min-w-44 rounded-(--r-pop) p-(--s1) text-(--fs) shadow-(--shadow-pop)", className)}
      {...props}
    />
  );
}

// 22 px item (--head-h, the nearest token for a menu row): icon, label, shortcut.
export function MenuItem({
  icon,
  shortcut,
  children,
  className,
  ...props
}: ComponentProps<typeof DropdownMenuItem> & { icon?: ReactNode; shortcut?: string }) {
  return (
    <DropdownMenuItem
      className={cn("h-(--head-h) gap-(--s3) rounded-(--r-row) px-(--s3) text-(--fs) focus:bg-(--accent) focus:text-(--accent-fg)", className)}
      {...props}
    >
      {icon}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {shortcut && <Kbd>{shortcut}</Kbd>}
    </DropdownMenuItem>
  );
}

export const RowContextMenu = ContextMenu;
export const RowContextMenuTrigger = ContextMenuTrigger;

export function RowContextMenuContent({ className, ...props }: ComponentProps<typeof ContextMenuContent>) {
  return (
    <ContextMenuContent
      className={cn("min-w-44 rounded-(--r-pop) p-(--s1) text-(--fs) shadow-(--shadow-pop)", className)}
      {...props}
    />
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
    <ContextMenuItem
      className={cn("h-(--head-h) gap-(--s3) rounded-(--r-row) px-(--s3) text-(--fs) focus:bg-(--accent) focus:text-(--accent-fg)", className)}
      {...props}
    >
      {icon}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {shortcut && <Kbd>{shortcut}</Kbd>}
    </ContextMenuItem>
  );
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
