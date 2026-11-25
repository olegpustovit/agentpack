import { useEffect, useLayoutEffect, useRef, type ComponentProps, type ReactElement, type KeyboardEvent } from "react";
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
        "group/row flex h-(--row-h) w-full min-w-0 items-center gap-(--s3) rounded-(--r-row) px-(--s3) text-left text-(length:--fs) text-(--fg) outline-none hover:bg-(--hover) focus-visible:ring-2 focus-visible:ring-(--accent)",
        "data-selected:bg-(--page) data-selected:shadow-(--e-ctl) group-focus-within/list:data-selected:bg-(--accent-fill) group-focus-within/list:data-selected:text-(--accent-fg)",
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

// A selection thumb that slides between items (--dur-2, --ease) instead of jumping. The track draws it
// as ::before from --thumb-x/--thumb-w, measured from the item matching `selector`; it appears in
// place on first paint and moves only on later changes. Reduced motion turns the slide off globally.
export function useSlidingThumb<T extends HTMLElement>(selector: string) {
  const ref = useRef<T>(null);
  useLayoutEffect(() => {
    const track = ref.current;
    if (!track) return;
    const place = () => {
      const on = track.querySelector<HTMLElement>(selector);
      track.style.setProperty("--thumb-x", `${on?.offsetLeft ?? 0}px`);
      track.style.setProperty("--thumb-w", `${on?.offsetWidth ?? 0}px`);
      track.dataset.thumb = on ? "on" : "off";
    };
    place();
    let frame = requestAnimationFrame(() => {
      frame = requestAnimationFrame(() => { track.dataset.thumbReady = ""; });
    });
    const attributes = new MutationObserver(place);
    attributes.observe(track, { subtree: true, attributes: true, childList: true, attributeFilter: ["aria-pressed", "aria-current"] });
    // Absent in jsdom; browsers always have it.
    const size = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(place);
    size?.observe(track);
    return () => { cancelAnimationFrame(frame); attributes.disconnect(); size?.disconnect(); delete track.dataset.thumbReady; };
  }, [selector]);
  return ref;
}

// The ::before thumb; the caller sets its inset, radius and fill.
export const slidingThumb =
  "relative isolate before:absolute before:left-0 before:-z-1 before:w-(--thumb-w) before:translate-x-(--thumb-x) data-[thumb=off]:before:opacity-0 data-thumb-ready:before:transition-[translate,width,opacity] data-thumb-ready:before:duration-(--dur-2) data-thumb-ready:before:ease-(--ease)";

export function SegmentedControl({ className, ...props }: ComponentProps<typeof ToggleGroup>) {
  const ref = useSlidingThumb<HTMLDivElement>('[aria-pressed="true"]');
  return (
    <ToggleGroup
      ref={ref}
      data-slot="segmented-control"
      spacing={1}
      className={cn(
        "h-(--ctl-h) gap-0 rounded-(--r-ctl) bg-(--track) p-(--s1)",
        slidingThumb,
        "before:inset-y-(--s1) before:rounded-(--r-row) before:bg-(--control) before:shadow-(--shadow-ctl)",
        className,
      )}
      {...props}
    />
  );
}

export function SegmentedControlItem({ className, ...props }: ComponentProps<typeof ToggleGroupItem>) {
  return (
    <ToggleGroupItem
      className={cn(
        "h-(--ctl-h-sm) min-w-(--ctl-h-sm) rounded-(--r-row) px-(--s3) text-(length:--fs-sm) font-normal text-(--fg2) transition-colors duration-(--dur-2) ease-(--ease) hover:bg-transparent aria-pressed:bg-transparent aria-pressed:text-(--fg) aria-pressed:hover:bg-transparent",
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
        className={cn("border-transparent bg-(--track) ps-[calc(var(--fs)+var(--s3)*2)] pe-(--s3)", className)}
        {...props}
        ref={ref}
      />
    </div>
  );
}
