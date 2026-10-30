import type { ComponentProps, ReactNode } from "react";
import { toast } from "sonner";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { shortHash } from "@/lib/format";
import { cn } from "@/lib/utils";

// Inspector is capped at 1180 px. Two columns from the viewport breakpoint 1260 px.
export function Inspector({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      data-slot="inspector"
      className={cn("mx-auto grid w-full max-w-295 gap-(--s6) p-(--s6) min-[1260px]:grid-cols-2", className)}
      {...props}
    />
  );
}

export function InspectorHead({
  icon,
  title,
  subtitle,
  children,
  className,
}: {
  icon?: ReactNode;
  title: string;
  subtitle?: string;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <header className={cn("col-span-full flex min-w-0 items-start gap-(--s5)", className)}>
      {icon && <span className="mt-(--s1) text-(--fg2)">{icon}</span>}
      <div className="min-w-0 flex-1">
        <h1 className="truncate text-(--fs) font-semibold text-(--fg)">{title}</h1>
        {subtitle && <p className="truncate text-(--fs-sm) text-(--fg2)">{subtitle}</p>}
      </div>
      {children && <div className="flex shrink-0 items-center gap-(--s3)">{children}</div>}
    </header>
  );
}

export function Section({
  title,
  children,
  className,
}: {
  title: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("min-w-0", className)}>
      <h2 className="mb-(--s3) text-(--fs-sm) font-semibold text-(--fg2)">{title}</h2>
      {children}
    </section>
  );
}

// Wrap several in a <dl>; the label column is 120 px and stacks on phone.
export function KeyValue({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline gap-(--s4) max-[760px]:flex-col max-[760px]:gap-(--s1)">
      <dt className="w-30 shrink-0 text-right text-(--fg2) max-[760px]:w-auto max-[760px]:text-left">{label}</dt>
      <dd className="min-w-0 flex-1 break-words">{children}</dd>
    </div>
  );
}

// Sticky 22 px header, 26 px rows, fixed layout. The consumer leaves one column unsized: it takes the rest.
export function DataGrid({ className, ...props }: ComponentProps<"table">) {
  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <table data-slot="data-grid" className={cn("w-full table-fixed border-collapse text-(--fs)", className)} {...props} />
    </div>
  );
}

export function DataGridHead({ className, ...props }: ComponentProps<"thead">) {
  return <thead className={cn("sticky top-0 bg-(--window)", className)} {...props} />;
}

export function DataGridHeaderCell({ className, ...props }: ComponentProps<"th">) {
  return (
    <th
      className={cn("h-(--head-h) px-(--s3) text-left text-(--fs-xs) font-medium text-(--fg3)", className)}
      {...props}
    />
  );
}

export function DataGridRow({ className, ...props }: ComponentProps<"tr">) {
  return <tr className={cn("h-(--row-h) hover:bg-(--hover)", className)} {...props} />;
}

export function DataGridCell({ className, ...props }: ComponentProps<"td">) {
  return <td className={cn("truncate px-(--s3)", className)} {...props} />;
}

// Renders nothing when the value is unknown.
export function Meter({ value, max = 100, label }: { value?: number; max?: number; label: string }) {
  if (value === undefined) return null;
  const left = value / max;
  const tone = left <= 0.05 ? "danger" : left <= 0.2 ? "warn" : "ok";
  const fill = tone === "danger" ? "[&::-webkit-meter-optimum-value]:bg-(--danger) [&::-moz-meter-bar]:bg-(--danger)"
    : tone === "warn" ? "[&::-webkit-meter-optimum-value]:bg-(--warn) [&::-moz-meter-bar]:bg-(--warn)"
    : "[&::-webkit-meter-optimum-value]:bg-(--accent) [&::-moz-meter-bar]:bg-(--accent)";
  return (
    <meter
      aria-label={label}
      min={0}
      max={max}
      value={value}
      data-tone={tone}
      className={cn(
        "block h-1.5 w-full appearance-none overflow-hidden rounded-full bg-(--track) [&::-moz-meter-bar]:rounded-full [&::-webkit-meter-bar]:bg-(--track) [&::-webkit-meter-optimum-value]:rounded-full",
        fill,
      )}
    />
  );
}

// Shows the first 7 characters. Click copies the full value; the tooltip shows it.
export function Hash({ value, className }: { value: string; className?: string }) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={<button type="button" className={cn("rounded-(--r-row) text-left", className)} />}
        onClick={() => {
          navigator.clipboard.writeText(value).then(
            () => toast("Copied"),
            () => toast("Couldn't copy. Select the value and copy it manually."),
          );
        }}
      >
        <code className="font-(family-name:--mono) text-(--fs-sm)">{shortHash(value)}</code>
      </TooltipTrigger>
      <TooltipContent>
        <code className="font-(family-name:--mono)">{value}</code>
      </TooltipContent>
    </Tooltip>
  );
}
