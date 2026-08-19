import { Bell, ChevronRight, ShieldAlert, TriangleAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatAge } from "../format";
import type { Alert, Incident } from "../types";

export function NotificationPopover({ alerts, incidents, onOpenRequest, onOpenIncident, onOpenLogs }: {
  alerts: Alert[];
  incidents: Incident[];
  onOpenRequest: (id: string) => void;
  onOpenIncident: (id: string) => void;
  onOpenLogs: (event: string) => void;
}) {
  const { t } = useTranslation("common");
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const activeAlerts = alerts.filter((alert) => !alert.resolvedAt);
  const activeIncidents = incidents.filter((incident) => incident.state !== "resolved");
  const count = activeAlerts.length + activeIncidents.length;

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const focusFrame = window.requestAnimationFrame(() => (panel.current?.querySelector<HTMLElement>("button, [tabindex]:not([tabindex='-1'])") || panel.current)?.focus());
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") { event.preventDefault(); setOpen(false); return; }
      if (event.key !== "Tab" || !panel.current) return;
      const focusable = Array.from(panel.current.querySelectorAll<HTMLElement>("button, [href], input, select, textarea, [tabindex]:not([tabindex='-1'])")).filter((element) => !element.hasAttribute("disabled"));
      if (!focusable.length) { event.preventDefault(); panel.current.focus(); return; }
      const first = focusable[0]; const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    const pointerdown = (event: MouseEvent) => {
      if (root.current && !root.current.contains(event.target as Node)) setOpen(false);
    };
    window.addEventListener("keydown", keydown);
    window.addEventListener("mousedown", pointerdown);
    return () => { window.cancelAnimationFrame(focusFrame); window.removeEventListener("keydown", keydown); window.removeEventListener("mousedown", pointerdown); if (previous && previous !== panel.current) trigger.current?.focus(); };
  }, [open]);

  return <div className="notification-center" ref={root}>
    <button ref={trigger} className="icon-button notification-trigger" aria-label={t("notifications.label")} aria-expanded={open} aria-controls="notification-popover" onClick={() => setOpen((value) => !value)}>
      <Bell size={17} />{count > 0 && <span>{count > 99 ? "99+" : count}</span>}
    </button>
    {open && <div ref={panel} className="notification-popover" id="notification-popover" role="dialog" aria-modal="true" aria-label={t("notifications.title")} tabIndex={-1}>
      <header><div><strong>{t("notifications.title")}</strong><span>{t("notifications.active", { count })}</span></div><i className={count ? "attention" : ""} /></header>
      <div className="notification-list">
        {activeIncidents.map((incident) => <button key={incident.id} onClick={() => { setOpen(false); onOpenIncident(incident.id); }}>
          <span className="notification-icon incident"><ShieldAlert size={15} /></span><div><strong>{t("notifications.incident")}</strong><small>{incident.id} · {formatAge(incident.lastFailureAt)}</small></div><ChevronRight size={14} />
        </button>)}
        {activeAlerts.map((alert) => <button key={alert.id} onClick={() => {
          setOpen(false);
          if (alert.requestId) { onOpenRequest(alert.requestId); return; }
          const related = activeIncidents.find((incident) => incident.categories[alert.type] !== undefined);
          if (related) onOpenIncident(related.id); else onOpenLogs(alert.type);
        }}>
          <span className="notification-icon"><TriangleAlert size={15} /></span><div><strong>{alert.message}</strong><small>{formatAge(alert.createdAt)}</small></div><ChevronRight size={14} />
        </button>)}
        {!count && <div className="notification-empty"><Bell size={18} /><p>{t("notifications.empty")}</p></div>}
      </div>
    </div>}
  </div>;
}
