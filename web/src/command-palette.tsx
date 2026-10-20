import { Icon, type IconName } from "./icons";
import { useEffect, useRef, useState } from "react";

export type CommandItem = {
  id: string;
  label: string;
  hint?: string;
  icon: IconName;
  run: () => void;
};

export function CommandPalette({
  open,
  items,
  onClose,
}: {
  open: boolean;
  items: CommandItem[];
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const previousFocus = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (open) {
      previousFocus.current =
        document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null;
      setQuery("");
      setActive(0);
      window.setTimeout(() => inputRef.current?.focus(), 0);
    } else {
      previousFocus.current?.focus();
    }
  }, [open]);

  useEffect(() => {
    setActive(0);
  }, [query]);

  useEffect(() => {
    if (!open) return;
    const handler = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [open, onClose]);

  useEffect(() => {
    listRef.current
      ?.querySelector('[aria-selected="true"]')
      ?.scrollIntoView({ block: "nearest" });
  }, [active]);

  if (!open) return null;

  const normalized = query.trim().toLowerCase();
  const score = (item: CommandItem) => {
    const label = item.label.toLowerCase();
    const hint = (item.hint ?? "").toLowerCase();
    if (label === normalized) return 0;
    if (label.startsWith(normalized)) return 1;
    if (label.includes(normalized)) return 2;
    if (hint.includes(normalized)) return 3;
    return 99;
  };
  const visible = items
    .map((item) => ({ item, rank: normalized ? score(item) : 0 }))
    .filter((entry) => entry.rank < 99)
    .sort((left, right) => left.rank - right.rank)
    .map((entry) => entry.item);

  function choose(item: CommandItem | undefined) {
    if (!item) return;
    onClose();
    item.run();
  }

  return (
    <div className="command-backdrop" onMouseDown={onClose}>
      <div
        className="command-panel"
        role="dialog"
        aria-modal="true"
        aria-label="命令面板"
        onMouseDown={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          if (event.key !== "Tab") return;
          const focusable = Array.from(
            event.currentTarget.querySelectorAll<HTMLElement>(
              "input, button:not(:disabled)",
            ),
          );
          if (!focusable.length) return;
          const first = focusable[0];
          const last = focusable[focusable.length - 1];
          if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last.focus();
          } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first.focus();
          }
        }}
      >
        <div className="command-input-row">
          <Icon name="search" size={16} />
          <input
            ref={inputRef}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "ArrowDown") {
                event.preventDefault();
                setActive((index) =>
                  visible.length ? (index + 1) % visible.length : 0,
                );
              } else if (event.key === "ArrowUp") {
                event.preventDefault();
                setActive((index) =>
                  visible.length
                    ? (index - 1 + visible.length) % visible.length
                    : 0,
                );
              } else if (event.key === "Enter") {
                event.preventDefault();
                choose(visible[active]);
              }
            }}
            placeholder="输入页面名称或操作"
            aria-label="搜索命令"
            autoComplete="off"
            spellCheck={false}
          />
          <kbd>Esc</kbd>
        </div>
        <div
          ref={listRef}
          className="command-list"
          role="listbox"
          aria-label="命令结果"
        >
          {visible.map((item, index) => (
            <button
              key={item.id}
              type="button"
              role="option"
              aria-selected={index === active}
              className="command-item"
              onMouseEnter={() => setActive(index)}
              onClick={() => choose(item)}
            >
              <Icon name={item.icon} size={16} />
              <span>{item.label}</span>
              {item.hint && <small>{item.hint}</small>}
            </button>
          ))}
          {!visible.length && (
            <p className="command-empty">没有匹配的结果</p>
          )}
        </div>
      </div>
    </div>
  );
}
