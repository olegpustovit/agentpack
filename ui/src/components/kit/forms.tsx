import { Fragment, type ComponentProps } from "react";
import { ShieldAlert } from "lucide-react";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { menuRow, menuSurface } from "@/components/kit/overlays";
import { cn } from "@/lib/utils";

// Label column is 120 px (w-30) and right-aligned; stacks on phone widths.
export function FieldGrid({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="field-grid" className={cn("flex flex-col gap-(--s4)", className)} {...props} />;
}

export function Field({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      data-slot="field"
      className={cn("flex items-center gap-(--s4) max-[760px]:flex-col max-[760px]:items-stretch", className)}
      {...props}
    />
  );
}

export function FieldLabel({ className, ...props }: ComponentProps<"label">) {
  return (
    <label
      data-slot="field-label"
      className={cn("w-30 shrink-0 text-right text-(--fg2) max-[760px]:w-auto max-[760px]:text-left", className)}
      {...props}
    />
  );
}

// Server rejections and validation errors show here, never in alerts.
export function FieldError({ className, ...props }: ComponentProps<"p">) {
  return <p role="alert" className={cn("text-(length:--fs-sm) text-(--danger)", className)} {...props} />;
}

export function PopupSelect({
  value,
  onValueChange,
  items,
  label,
  disabled,
}: {
  value: string;
  onValueChange: (value: string) => void;
  items: { value: string; label: string; disabled?: boolean; danger?: boolean }[];
  label: string;
  disabled?: boolean;
}) {
  // A dangerous choice stays visible after the menu closes: the trigger turns red with a shield.
  const danger = items.find((item) => item.value === value)?.danger === true;
  return (
    <Select items={items} value={value} onValueChange={(next) => next && onValueChange(next)} disabled={disabled}>
      <SelectTrigger
        aria-label={label}
        data-danger={danger}
        className="h-(--ctl-h) max-w-full min-w-0 rounded-(--r-ctl) text-(length:--fs) *:data-[slot=select-value]:min-w-0 *:data-[slot=select-value]:truncate data-[danger=true]:bg-(--del) data-[danger=true]:text-(--danger-word) data-[danger=true]:shadow-[inset_0_0_0_.5px_var(--del-strong)] data-[danger=true]:[&_svg]:text-(--danger-word)"
      >
        {danger && <ShieldAlert aria-hidden className="size-3.5" />}
        <SelectValue />
      </SelectTrigger>
      <SelectContent className={cn(menuSurface, "w-max min-w-(--anchor-width) max-w-[min(90vw,40rem)]")}>
        {items.map((item, index) => (
          <Fragment key={item.value}>
            {/* Dangerous choices form their own group, as AppKit separates destructive items. */}
            {item.danger && index > 0 && !items[index - 1]?.danger && <SelectSeparator />}
            <SelectItem
              value={item.value}
              disabled={item.disabled}
              data-danger={item.danger}
              className={cn(menuRow, "min-w-0 pl-6 [&_span]:truncate")}
            >
              {item.label}
            </SelectItem>
          </Fragment>
        ))}
      </SelectContent>
    </Select>
  );
}
