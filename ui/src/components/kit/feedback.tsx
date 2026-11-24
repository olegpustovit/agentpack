import { type ComponentProps, type ReactNode } from "react";
import { CircleAlert, Info, TriangleAlert } from "lucide-react";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

// Tones are told apart by glyph and weight, never by color alone.
const tones = {
  info: { icon: Info, className: "border-(--sep)" },
  warn: { icon: TriangleAlert, className: "border-(--warn)" },
  danger: { icon: CircleAlert, className: "border-(--danger)" },
} as const;

export function Banner({
  tone = "info",
  action,
  children,
  className,
}: {
  tone?: keyof typeof tones;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  const { icon: Icon, className: toneClass } = tones[tone];
  return (
    <div
      role={tone === "info" ? "status" : "alert"}
      data-slot="banner"
      data-tone={tone}
      className={cn(
        "flex min-w-0 items-center gap-(--s3) border-b-[0.5px] px-(--s4) py-(--s3) text-(length:--fs-sm) text-(--fg)",
        toneClass,
        className,
      )}
    >
      <Icon aria-hidden className="size-(--fs) shrink-0" />
      <span className="min-w-0 flex-1">{children}</span>
      {action}
    </div>
  );
}

export function Empty({
  icon,
  title,
  children,
  className,
}: {
  icon?: ReactNode;
  title: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col items-center gap-(--s3) p-(--s7) text-center", className)}>
      {icon && <span className="text-(--fg3)">{icon}</span>}
      <p className="text-(length:--fs) font-medium text-(--fg)">{title}</p>
      <p className="text-(length:--fs-sm) text-(--fg2)">{children}</p>
    </div>
  );
}

export function RowSkeleton({ lines = 1, className }: { lines?: 1 | 2; className?: string }) {
  return (
    <div className={cn("flex flex-col justify-center gap-(--s2) px-(--s3)", lines === 1 ? "h-(--row-h)" : "h-(--row-h-2)", className)}>
      <Skeleton className="h-(--fs) w-3/4" />
      {lines === 2 && <Skeleton className="h-(--fs-sm) w-1/2" />}
    </div>
  );
}

export function Spinner({ className, ...props }: ComponentProps<"span">) {
  return (
    <span role="status" aria-label="Loading" className={cn("inline-flex", className)} {...props}>
      <span aria-hidden className="size-(--fs) rounded-full border-[1.5px] border-current border-r-transparent opacity-70 animate-spin motion-reduce:animate-none" />
    </span>
  );
}

// The polite status line: startup and machine discovery announce through it.
export function Announcement({ className, children }: { className?: string; children: ReactNode }) {
  return <p role="status" className={cn("min-w-0 truncate text-(length:--fs-sm) text-(--fg2)", className)}>{children}</p>;
}

export function Kbd({ className, ...props }: ComponentProps<"kbd">) {
  return (
    <kbd
      className={cn("font-(family-name:--mono) text-(length:--fs-xs) text-(--fg3)", className)}
      {...props}
    />
  );
}
