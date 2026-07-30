import { X } from "lucide-react";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";

export function InspectorShell({ title, subtitle, status, children, footer, onClose, wide = false, modal = true, className = "" }: {
  title: ReactNode;
  subtitle?: ReactNode;
  status?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  onClose?: () => void;
  wide?: boolean;
  modal?: boolean;
  className?: string;
}) {
  const { t } = useTranslation("common");
  const inspector = useRef<HTMLElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    if (!modal || !onClose) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    inspector.current?.querySelector<HTMLElement>("button")?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") closeRef.current?.();
      if (event.key !== "Tab" || !inspector.current) return;
      const focusable = Array.from(inspector.current.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')).filter((element) => !element.hasAttribute("disabled"));
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", keydown);
    return () => { window.removeEventListener("keydown", keydown); previous?.focus(); };
  }, [modal]);

  return <>
    {modal && onClose && <button className="inspector-backdrop" aria-label={t("actions.close")} onClick={onClose} />}
    <aside
      ref={inspector}
      className={`inspector-shell${wide ? " inspector-wide" : ""}${modal ? " inspector-modal" : " inspector-persistent"} ${className}`}
      role={modal ? "dialog" : "complementary"}
      aria-modal={modal || undefined}
      aria-label={typeof title === "string" ? title : undefined}
    >
      <header className="inspector-header"><div>{status}{subtitle && <span>{subtitle}</span>}<h2>{title}</h2></div>
        {onClose && <button className="icon-button" aria-label={t("actions.close")} onClick={onClose}><X size={18} /></button>}
      </header>
      <div className="inspector-body">{children}</div>
      {footer && <footer className="inspector-actions">{footer}</footer>}
    </aside>
  </>;
}
