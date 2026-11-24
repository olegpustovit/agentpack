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
          "grid h-dvh grid-cols-[minmax(0,1fr)] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden bg-(--canvas) text-(length:--fs) text-(--fg)",
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
        "flex min-w-0 items-center gap-(--s3) bg-(--chrome) max-[760px]:border-b-[0.5px] max-[760px]:border-(--sep)",
        className,
      )}
      {...props}
    />
  );
}

export function ToolbarButton({ className, ...props }: ComponentProps<typeof Button>) {
  return <Button variant="ghost" size="icon" className={cn("shrink-0 rounded-full [&_svg]:size-4", className)} {...props} />;
}
