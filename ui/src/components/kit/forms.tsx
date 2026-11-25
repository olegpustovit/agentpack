import { Fragment, type ComponentProps, type ReactNode } from "react";
import { ShieldAlert } from "lucide-react";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { menuRow, menuSurface } from "@/components/kit/overlays";
import { cn } from "@/lib/utils";

// Label column is 120 px (w-30) and right-aligned; stacks on phone widths.
export function FieldGrid({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="field-grid" className={cn("flex flex-col gap-(--s5) max-[760px]:gap-(--s6)", className)} {...props} />;
}

export function Field({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      data-slot="field"
      className={cn("flex items-center gap-(--s4) max-[760px]:flex-col max-[760px]:items-stretch max-[760px]:gap-(--s2)", className)}
      {...props}
    />
  );
}

export function FieldLabel({ className, ...props }: ComponentProps<"label">) {
  return (
    <label
      data-slot="field-label"
      className={cn("w-30 shrink-0 text-right text-(--fg2) max-[760px]:w-auto max-[760px]:text-left max-[760px]:text-(length:--fs-sm) max-[760px]:font-medium", className)}
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
  items: { value: string; label: string; disabled?: boolean; danger?: boolean; detail?: ReactNode; group?: string }[];
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
            {item.group && item.group !== items[index - 1]?.group && (
              <>
                {index > 0 && <SelectSeparator />}
                <div className="px-3 py-1 text-(length:--fs-xs) text-(--fg2)">{item.group}</div>
              </>
            )}
            {/* Dangerous choices form their own group, as AppKit separates destructive items. */}
            {item.danger && index > 0 && !items[index - 1]?.danger && <SelectSeparator />}
            <SelectItem
              value={item.value}
              label={item.label}
              disabled={item.disabled}
              data-danger={item.danger}
              className={cn(menuRow, "min-w-0 pl-6 [&_span]:truncate")}
            >
              <span className="min-w-0 flex-1">{item.label}</span>
              {item.detail && <span className="ml-auto flex shrink-0 items-center gap-(--s2) text-(length:--fs-xs) text-(--fg2)">{item.detail}</span>}
            </SelectItem>
          </Fragment>
        ))}
      </SelectContent>
    </Select>
  );
}

// macOS grouped form: an inset box of rows, an optional title above and a footer note below.
export function FormGroup({ title, action, footer, className, children }: { title?: ReactNode; action?: ReactNode; footer?: ReactNode; className?: string; children: ReactNode }) {
  return (
    <section data-slot="form-group" className={cn("flex min-w-0 flex-col gap-(--s3)", className)}>
      {(title || action) && (
        <div className="flex min-h-(--ctl-h) items-end gap-(--s3) px-(--s2)">
          <h3 className="min-w-0 flex-1 text-(length:--fs-sm) font-semibold text-(--fg2)">{title}</h3>
          {action}
        </div>
      )}
      <div className="overflow-hidden rounded-(--r-pop) border-[0.5px] border-(--sep) bg-(--window)">{children}</div>
      {footer && <div className="px-(--s2) text-(length:--fs-sm) text-(--fg2)">{footer}</div>}
    </section>
  );
}

// Label and description lead, the control trails; `detail` spans the row below (checks, errors, inputs).
export function FormRow({ label, description, htmlFor, detail, className, children }: { label: ReactNode; description?: ReactNode; htmlFor?: string; detail?: ReactNode; className?: string; children?: ReactNode }) {
  return (
    <div data-slot="form-row" className={cn("flex min-h-(--row-h-2) min-w-0 flex-col justify-center gap-(--s3) border-t-[0.5px] border-(--sep-soft) px-(--s5) py-(--s3) first:border-t-0", className)}>
      <div className="flex min-w-0 items-center gap-(--s5) max-[760px]:flex-wrap">
        <div className="min-w-0 flex-1">
          {htmlFor ? <label htmlFor={htmlFor}>{label}</label> : <div>{label}</div>}
          {description && <div className="text-(length:--fs-sm) text-(--fg2)">{description}</div>}
        </div>
        {children !== undefined && <div className="flex min-w-0 shrink-0 flex-wrap items-center justify-end gap-(--s3) text-(--fg2) max-[760px]:shrink">{children}</div>}
      </div>
      {detail && <div className="flex min-w-0 flex-col items-start gap-(--s3)">{detail}</div>}
    </div>
  );
}
