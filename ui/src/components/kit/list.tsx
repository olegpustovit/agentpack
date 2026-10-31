import { useEffect, useRef, type ComponentProps, type ReactElement, type KeyboardEvent } from "react";
import { useRender } from "@base-ui/react/use-render";
import { Search } from "lucide-react";
import { Input } from "@/components/ui/input";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { cn } from "@/lib/utils";

// Roving tabindex: exactly one row is a tab stop. Rows carry no tabIndex prop,
// so React never fights the DOM-managed value between renders.
export function SourceList({ className, onKeyDown, ...props }: ComponentProps<"nav">) {
  const ref = useRef<HTMLElement>(null);

  useEffect(() => {
    const rows = rowsOf(ref.current);
    if (rows.length === 0) return;
    const active = rows.find((row) => row.dataset.selected !== undefined) ?? rows[0];
    rows.forEach((row) => {
      row.tabIndex = row === active ? 0 : -1;
    });
  });

  function handleKeyDown(event: KeyboardEvent<HTMLElement>) {
    onKeyDown?.(event);
    if (event.defaultPrevented) return;
    const rows = rowsOf(event.currentTarget);
    if (rows.length === 0) return;
    const current = rows.indexOf(document.activeElement as HTMLElement);
    let next: number;
    switch (event.key) {
      case "ArrowDown":
        next = Math.min(current + 1, rows.length - 1);
        break;
      case "ArrowUp":
        next = Math.max(current - 1, 0);
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = rows.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    rows.forEach((row, index) => {
      row.tabIndex = index === next ? 0 : -1;
    });
    rows[next]?.focus();
  }

  return (
    <nav
      ref={ref}
      data-slot="source-list"
      className={cn("group/list flex min-h-0 flex-col overflow-y-auto px-(--s2) pb-(--s2)", className)}
      onKeyDown={handleKeyDown}
      {...props}
    />
  );
}

function rowsOf(root: HTMLElement | null): HTMLElement[] {
  return root ? Array.from(root.querySelectorAll<HTMLElement>("[data-source-row]")) : [];
}

// Selected rows are gray while the list is unfocused and accent when focus is inside it.
export function SourceRow({
  className,
  render,
  selected = false,
  ...props
}: ComponentProps<"button"> & { render?: ReactElement; selected?: boolean }) {
  return useRender({
    defaultTagName: "button",
    render,
    props: {
      "data-slot": "source-row",
      "data-source-row": "",
      "data-selected": selected ? "true" : undefined,
      "aria-current": selected ? "true" : undefined,
      className: cn(
        "group/row flex h-(--row-h) w-full min-w-0 items-center gap-(--s3) rounded-(--r-row) px-(--s3) text-left text-(length:--fs) text-(--fg) outline-none hover:bg-(--hover) focus-visible:ring-2 focus-visible:ring-(--accent) max-[760px]:h-8",
        "data-selected:bg-(--sel-inactive) group-focus-within/list:data-selected:bg-(--accent) group-focus-within/list:data-selected:text-(--accent-fg)",
        className,
      ),
      ...props,
    },
  });
}

export function GroupHeader({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      data-slot="group-header"
      className={cn(
        "flex h-(--head-h) min-w-0 items-center gap-(--s3) px-(--s2) text-(length:--fs-xs) font-semibold text-(--fg2) max-[760px]:h-6",
        className,
      )}
      {...props}
    />
  );
}

export function SegmentedControl({ className, ...props }: ComponentProps<typeof ToggleGroup>) {
  return (
    <ToggleGroup
      data-slot="segmented-control"
      spacing={1}
      className={cn("h-(--ctl-h) gap-0 rounded-(--r-ctl) bg-(--track) p-(--s1)", className)}
      {...props}
    />
  );
}

export function SegmentedControlItem({ className, ...props }: ComponentProps<typeof ToggleGroupItem>) {
  return (
    <ToggleGroupItem
      className={cn(
        "h-(--ctl-h-sm) min-w-(--ctl-h-sm) rounded-(--r-row) px-(--s3) text-(length:--fs-sm) font-normal text-(--fg2) hover:bg-transparent aria-pressed:bg-(--control) aria-pressed:text-(--fg) aria-pressed:shadow-(--shadow-ctl) aria-pressed:hover:bg-(--control)",
        className,
      )}
      {...props}
    />
  );
}

export function SearchField({ className, ...props }: ComponentProps<"input">) {
  const ref = useRef<HTMLInputElement>(null);

  useEffect(() => {
    function focusOnShortcut(event: globalThis.KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "f") {
        event.preventDefault();
        ref.current?.focus();
        ref.current?.select();
      }
    }
    window.addEventListener("keydown", focusOnShortcut);
    return () => window.removeEventListener("keydown", focusOnShortcut);
  }, []);

  return (
    <div className="relative min-w-0 flex-1">
      <Search
        aria-hidden
        className="pointer-events-none absolute top-1/2 left-(--s3) size-(--fs) -translate-y-1/2 text-(--fg3)"
      />
      <Input
        type="search"
        className={cn("border-transparent bg-(--track) ps-(--s7) pe-(--s3)", className)}
        {...props}
        ref={ref}
      />
    </div>
  );
}
