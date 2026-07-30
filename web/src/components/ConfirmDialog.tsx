import { AlertTriangle, ShieldCheck } from "lucide-react";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

export interface ConfirmDialogState {
  title: string;
  description: string;
  confirmLabel?: string;
  tone?: "default" | "danger";
}

export function ConfirmDialog({ state, onConfirm, onCancel }: {
  state: ConfirmDialogState;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation("common");
  const dialog = useRef<HTMLDivElement>(null);
  const cancelRef = useRef(onCancel);
  cancelRef.current = onCancel;

  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.current?.querySelector<HTMLButtonElement>("button")?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") cancelRef.current();
      if (event.key !== "Tab" || !dialog.current) return;
      const buttons = Array.from(dialog.current.querySelectorAll<HTMLButtonElement>("button"));
      if (event.shiftKey && document.activeElement === buttons[0]) { event.preventDefault(); buttons[buttons.length - 1]?.focus(); }
      else if (!event.shiftKey && document.activeElement === buttons[buttons.length - 1]) { event.preventDefault(); buttons[0]?.focus(); }
    };
    window.addEventListener("keydown", keydown);
    return () => { window.removeEventListener("keydown", keydown); previous?.focus(); };
  }, []);

  return <div className="confirm-backdrop" role="presentation" onMouseDown={onCancel}>
    <div ref={dialog} className={`confirm-dialog tone-${state.tone || "default"}`} role="alertdialog" aria-modal="true" aria-labelledby="confirm-title" aria-describedby="confirm-description" onMouseDown={(event) => event.stopPropagation()}>
      <span className="confirm-icon">{state.tone === "danger" ? <AlertTriangle size={20} /> : <ShieldCheck size={20} />}</span>
      <div><h2 id="confirm-title">{state.title}</h2><p id="confirm-description">{state.description}</p></div>
      <footer><button className="button" onClick={onCancel}>{t("actions.cancelDialog")}</button><button className={`button ${state.tone === "danger" ? "danger" : "primary"}`} onClick={onConfirm}>{state.confirmLabel || t("actions.confirm")}</button></footer>
    </div>
  </div>;
}
