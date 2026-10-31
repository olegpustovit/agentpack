import type { ComponentProps } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
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
  items: { value: string; label: string }[];
  label: string;
  disabled?: boolean;
}) {
  return (
    <Select items={items} value={value} onValueChange={(next) => next && onValueChange(next)} disabled={disabled}>
      <SelectTrigger aria-label={label} className="h-(--ctl-h) max-w-full min-w-0 rounded-(--r-ctl) text-(length:--fs) *:data-[slot=select-value]:min-w-0 *:data-[slot=select-value]:truncate">
        <SelectValue />
      </SelectTrigger>
      <SelectContent className="max-w-[min(90vw,40rem)] rounded-(--r-pop) text-(length:--fs)">
        {items.map((item) => (
          <SelectItem key={item.value} value={item.value} className="h-(--head-h) min-w-0 rounded-(--r-row) text-(length:--fs) [&_span]:truncate">
            {item.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
