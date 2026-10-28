import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./icons";

export type RowMenuItem = {
  label: string;
  onSelect?: () => void;
  href?: string;
  download?: boolean;
  danger?: boolean;
  disabled?: boolean;
  title?: string;
};

/**
 * List row menu rendered in a portal with fixed positioning.
 *
 * A menu nested in a scroll container or a card with `overflow: hidden` is
 * clipped, so the last rows of a long list lose their actions. This keeps the
 * menu outside every clipping ancestor, flips it upwards near the viewport
 * edge and follows scrolling instead of closing, which matches desktop
 * applications rather than a page that scrolls away.
 */
export function RowMenu({
  label,
  items,
  align = "end",
  summary,
}: {
  label: string;
  items: RowMenuItem[];
  align?: "start" | "end";
  summary?: React.ReactNode;
}) {
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [placement, setPlacement] = useState<{
    top: number;
    left: number;
    maxHeight: number;
    origin: "top" | "bottom";
  } | null>(null);

  function locate() {
    const anchor = trigger.current;
    const panel = menu.current;
    if (!anchor) return;
    const rect = anchor.getBoundingClientRect();
    const width = panel?.offsetWidth ?? 180;
    const height = panel?.offsetHeight ?? 0;
    const margin = 8;
    const below = window.innerHeight - rect.bottom - margin;
    const above = rect.top - margin;
    const openUp = height > below && above > below;
    let top = openUp ? rect.top - height - 4 : rect.bottom + 4;
    top = Math.max(margin, Math.min(top, window.innerHeight - margin - Math.min(height, window.innerHeight - margin * 2)));
    let left = align === "end" ? rect.right - width : rect.left;
    left = Math.max(margin, Math.min(left, window.innerWidth - width - margin));
    const maxHeight = Math.max(160, openUp ? above - 4 : below - 4);
    setPlacement({ top, left, maxHeight, origin: openUp ? "bottom" : "top" });
  }

  useLayoutEffect(() => {
    if (open) locate();
  }, [open]);

  useEffect(() => {
    if (!open) {
      setPlacement(null);
      return;
    }
    const first = menu.current?.querySelector<HTMLElement>('[role="menuitem"]:not([disabled])');
    first?.focus();
    const onPointer = (event: PointerEvent) => {
      const target = event.target as Node;
      if (menu.current?.contains(target) || trigger.current?.contains(target)) return;
      setOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setOpen(false);
        trigger.current?.focus();
        return;
      }
      if (event.key !== "ArrowDown" && event.key !== "ArrowUp" && event.key !== "Home" && event.key !== "End") return;
      const entries = Array.from(
        menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([disabled])') ?? [],
      );
      if (!entries.length) return;
      event.preventDefault();
      const index = entries.indexOf(document.activeElement as HTMLElement);
      let next = 0;
      if (event.key === "End") next = entries.length - 1;
      else if (event.key === "ArrowDown") next = index < 0 ? 0 : (index + 1) % entries.length;
      else if (event.key === "ArrowUp") next = index < 0 ? entries.length - 1 : (index - 1 + entries.length) % entries.length;
      entries[next].focus();
    };
    const reposition = () => locate();
    document.addEventListener("pointerdown", onPointer, true);
    document.addEventListener("keydown", onKey);
    // Capture scroll from any ancestor scroll container, not just the window.
    window.addEventListener("scroll", reposition, true);
    window.addEventListener("resize", reposition);
    return () => {
      document.removeEventListener("pointerdown", onPointer, true);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", reposition, true);
      window.removeEventListener("resize", reposition);
    };
  }, [open]);

  const choose = (item: RowMenuItem) => {
    if (item.disabled) return;
    setOpen(false);
    item.onSelect?.();
  };

  return (
    <>
      <button
        type="button"
        ref={trigger}
        className="row-menu-trigger"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        title={label}
        onClick={() => setOpen((value) => !value)}
      >
        {summary ?? <Icon name="more" size={18} />}
      </button>
      {open &&
        createPortal(
          <div
            className="row-menu"
            role="menu"
            aria-label={label}
            data-origin={placement?.origin ?? "top"}
            ref={menu}
            style={
              placement
                ? {
                    top: placement.top,
                    left: placement.left,
                    maxHeight: placement.maxHeight,
                  }
                : { top: -9999, left: -9999 }
            }
          >
            {items.map((item) =>
              item.href ? (
                <a
                  key={item.label}
                  role="menuitem"
                  className={`row-menu-item${item.danger ? " danger" : ""}`}
                  href={item.disabled ? undefined : item.href}
                  download={item.download || undefined}
                  aria-disabled={item.disabled || undefined}
                  title={item.title}
                  onClick={() => setOpen(false)}
                >
                  {item.label}
                </a>
              ) : (
                <button
                  key={item.label}
                  type="button"
                  role="menuitem"
                  className={`row-menu-item${item.danger ? " danger" : ""}`}
                  disabled={item.disabled}
                  title={item.title}
                  onClick={() => choose(item)}
                >
                  {item.label}
                </button>
              ),
            )}
          </div>,
          document.body,
        )}
    </>
  );
}
