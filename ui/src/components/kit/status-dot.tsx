import type { ComponentProps } from "react";
import { RefreshCw } from "lucide-react";
import { cn } from "@/lib/utils";

export type MachineState = "running" | "stopped" | "restarting" | "offline" | "readonly" | "needsUpdate";

const words: Record<MachineState, string> = {
  running: "Running",
  stopped: "Stopped",
  restarting: "Restarting",
  offline: "Offline",
  readonly: "Read-only",
  needsUpdate: "Needs update",
};

const glyphs: Record<Exclude<MachineState, "restarting">, string> = {
  running: "bg-(--ok) border-(--ok) group-data-selected/row:bg-(--accent-fg) group-data-selected/row:border-(--accent-fg)",
  stopped: "border-(--fg2) group-data-selected/row:border-(--accent-fg)",
  offline: "border-dashed border-(--fg3) group-data-selected/row:border-(--accent-fg)",
  readonly: "bg-(--fg3) border-(--fg3) group-data-selected/row:bg-(--accent-fg) group-data-selected/row:border-(--accent-fg)",
  needsUpdate: "bg-(--warn) border-(--warn) group-data-selected/row:bg-(--accent-fg) group-data-selected/row:border-(--accent-fg)",
};

export function StatusDot({ state, className, ...props }: { state: MachineState } & ComponentProps<"span">) {
  return (
    <span
      data-slot="status-dot"
      data-state={state}
      className={cn(
        "inline-flex min-w-0 items-center gap-(--s3) text-(--fs-sm) text-(--fg2) group-data-selected/row:text-(--accent-fg)",
        className,
      )}
      {...props}
    >
      {state === "restarting" ? (
        <RefreshCw
          aria-hidden
          className="size-(--s4) shrink-0 animate-spin motion-reduce:animate-none group-data-selected/row:text-(--accent-fg)"
        />
      ) : (
        <span aria-hidden className={cn("inline-block size-(--s4) shrink-0 rounded-full border", glyphs[state])} />
      )}
      <span className="truncate">{words[state]}</span>
    </span>
  );
}
