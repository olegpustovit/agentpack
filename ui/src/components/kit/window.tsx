import type { ComponentProps } from "react";
import { Button } from "@/components/ui/button";
import { TooltipProvider } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

export function AppWindow({ className, ...props }: ComponentProps<"div">) {
  return (
    <TooltipProvider delay={700}>
      <div
        data-slot="app-window"
        className={cn(
          "grid h-dvh grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden bg-(--window) text-(--fs) text-(--fg)",
          className,
        )}
        {...props}
      />
    </TooltipProvider>
  );
}

export function Toolbar({ className, ...props }: ComponentProps<"header">) {
  return (
    <header
      data-slot="toolbar"
      className={cn(
        "flex h-(--bar-h) min-w-0 items-center gap-(--s3) border-b-[0.5px] border-(--sep) bg-(--chrome) px-(--s4) max-[760px]:h-11",
        className,
      )}
      {...props}
    />
  );
}

export function ToolbarButton({ className, ...props }: ComponentProps<typeof Button>) {
  return <Button variant="ghost" size="icon" className={cn("shrink-0", className)} {...props} />;
}

export function StatusBar({ className, ...props }: ComponentProps<"footer">) {
  return (
    <footer
      data-slot="status-bar"
      className={cn(
        "flex h-(--status-h) min-w-0 items-center gap-(--s4) border-t-[0.5px] border-(--sep) bg-(--chrome) px-(--s4) text-(--fs-sm) text-(--fg2) max-[760px]:hidden",
        className,
      )}
      {...props}
    />
  );
}

export function StatusItem({ className, ...props }: ComponentProps<"span">) {
  return <span data-slot="status-item" className={cn("min-w-0 truncate", className)} {...props} />;
}
