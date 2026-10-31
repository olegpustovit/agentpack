import type { ComponentProps, CSSProperties, ReactNode } from "react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { shortHash } from "@/lib/format";
import { hud } from "@/lib/hud";
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
      {icon && <span className="grid size-(--bar-h) shrink-0 place-items-center rounded-(--r-pop) bg-(--track) text-(--fg2)">{icon}</span>}
      <div className="min-w-0 flex-1">
        <h1 className="truncate text-(length:--fs-lg) font-semibold text-(--fg)">{title}</h1>
        {subtitle && <p className="truncate text-(length:--fs-sm) text-(--fg2)">{subtitle}</p>}
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
    <section className={cn("min-w-0 border-t-[0.5px] border-(--sep) pt-(--s4)", className)}>
      <h2 className="mb-(--s3) text-(length:--fs-sm) font-semibold text-(--fg2)">{title}</h2>
      {children}
    </section>
  );
}

// Wrap several in a <dl>; the label column is 120 px and stacks on phone.
export function KeyValue({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline gap-(--s4) max-[760px]:flex-col max-[760px]:gap-(--s1)">
      <dt className="w-30 shrink-0 text-(--fg2) max-[760px]:w-auto">{label}</dt>
      <dd className="min-w-0 flex-1 break-words">{children}</dd>
    </div>
  );
}

// Sticky 22 px header, 26 px rows, fixed layout. The consumer leaves one column unsized: it takes the rest.
// The minimum width makes a narrow window scroll, because fixed layout would otherwise squeeze that column to nothing.
export function DataGrid({ className, ...props }: ComponentProps<"table">) {
  return (
    <div className="-mx-(--s3) min-h-0 flex-1 overflow-auto">
      <table data-slot="data-grid" className={cn("w-full min-w-140 table-fixed border-collapse text-(length:--fs)", className)} {...props} />
    </div>
  );
}

export function DataGridHead({ className, ...props }: ComponentProps<"thead">) {
  return <thead className={cn("sticky top-0 z-10 bg-(--window)", className)} {...props} />;
}

export function DataGridHeaderCell({ className, ...props }: ComponentProps<"th">) {
  return (
    <th
      className={cn("h-(--head-h) border-b-[0.5px] border-(--sep) px-(--s3) text-left text-(length:--fs-xs) font-medium text-(--fg2)", className)}
      {...props}
    />
  );
}

export function DataGridRow({ className, ...props }: ComponentProps<"tr">) {
  return <tr className={cn("h-(--row-h) border-b-[0.5px] border-(--sep-soft) hover:bg-(--hover)", className)} {...props} />;
}

export function DataGridCell({ className, ...props }: ComponentProps<"td">) {
  return <td className={cn("truncate px-(--s3)", className)} {...props} />;
}

// Renders nothing when the value is unknown.
export function Meter({ value, max = 100, label }: { value?: number; max?: number; label: string }) {
  if (value === undefined) return null;
  const left = value / max;
  const tone = left <= 0.05 ? "danger" : left <= 0.2 ? "warn" : "ok";
  // The browser's own meter fill is a gradient image, so only the `background` shorthand replaces it.
  const fill = tone === "danger" ? "var(--danger)" : tone === "warn" ? "var(--warn)" : "var(--accent)";
  return (
    <meter
      aria-label={label}
      min={0}
      max={max}
      value={value}
      data-tone={tone}
      className={cn(
        "block h-1.5 w-full appearance-none overflow-hidden rounded-full border-0 bg-(--track) [&::-moz-meter-bar]:rounded-full [&::-moz-meter-bar]:[background:var(--meter)] [&::-webkit-meter-bar]:h-1.5 [&::-webkit-meter-bar]:rounded-full [&::-webkit-meter-bar]:border-0 [&::-webkit-meter-bar]:[background:var(--track)] [&::-webkit-meter-optimum-value]:rounded-full [&::-webkit-meter-optimum-value]:[background:var(--meter)]",
      )}
      style={{ "--meter": fill } as CSSProperties}
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
          // navigator.clipboard is undefined on plain-HTTP gateway origins.
          (navigator.clipboard?.writeText(value) ?? Promise.reject()).then(
            () => hud.success("Copied"),
            () => hud.error("Couldn't copy. Select the value and copy it manually."),
          );
        }}
      >
        <code className="font-(family-name:--mono) text-(length:--fs-sm)">{shortHash(value)}</code>
      </TooltipTrigger>
      <TooltipContent>
        <code className="font-(family-name:--mono)">{value}</code>
      </TooltipContent>
    </Tooltip>
  );
}
