import { Icon } from "./icons";
import { ReactNode, useEffect, useId, useRef } from "react";

export function KnowledgeDialog({
  title,
  pending = false,
  onClose,
  children,
}: {
  title: string;
  pending?: boolean;
  onClose: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleID = useId();
  useEffect(() => {
    const dialog = ref.current!;
    dialog.showModal();
    (
      dialog.querySelector<HTMLElement>(
        "input:not([type=checkbox]), textarea, [data-dialog-cancel]",
      ) ?? dialog
    ).focus();
    return () => dialog.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className="kb-dialog"
      aria-labelledby={titleID}
      onCancel={(event) => {
        event.preventDefault();
        if (!pending) onClose();
      }}
      onClick={(event) => {
        if (event.target !== event.currentTarget || pending) return;
        const box = event.currentTarget.getBoundingClientRect();
        if (
          event.clientX < box.left ||
          event.clientX > box.right ||
          event.clientY < box.top ||
          event.clientY > box.bottom
        )
          onClose();
      }}
    >
      <header>
        <h2 id={titleID}>{title}</h2>
        <button
          type="button"
          className="icon-button"
          aria-label="关闭弹窗"
          disabled={pending}
          onClick={onClose}
        >
          <Icon name="close" />
        </button>
      </header>
      <div className="kb-dialog-body">{children}</div>
    </dialog>
  );
}
