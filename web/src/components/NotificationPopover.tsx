import { Bell, ChevronRight, ShieldAlert, TriangleAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatAge } from "../format";
import type { Alert, Incident } from "../types";

export function NotificationPopover({ alerts, incidents, onOpenRequest, onOpenIncidents }: {
  alerts: Alert[];
  incidents: Incident[];
  onOpenRequest: (id: string) => void;
  onOpenIncidents: () => void;
}) {
  const { t } = useTranslation("common");
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const activeAlerts = alerts.filter((alert) => !alert.resolvedAt);
  const activeIncidents = incidents.filter((incident) => incident.state !== "resolved");
  const count = activeAlerts.length + activeIncidents.length;

  useEffect(() => {
    if (!open) return;
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") { setOpen(false); trigger.current?.focus(); }
    };
    const pointerdown = (event: MouseEvent) => {
      if (root.current && !root.current.contains(event.target as Node)) setOpen(false);
    };
    window.addEventListener("keydown", keydown);
    window.addEventListener("mousedown", pointerdown);
    return () => { window.removeEventListener("keydown", keydown); window.removeEventListener("mousedown", pointerdown); };
  }, [open]);

  return <div className="notification-center" ref={root}>
    <button ref={trigger} className="icon-button notification-trigger" aria-label={t("notifications.label")} aria-expanded={open} aria-controls="notification-popover" onClick={() => setOpen((value) => !value)}>
      <Bell size={17} />{count > 0 && <span>{count > 99 ? "99+" : count}</span>}
    </button>
    {open && <div className="notification-popover" id="notification-popover" role="dialog" aria-label={t("notifications.title")}>
      <header><div><strong>{t("notifications.title")}</strong><span>{t("notifications.active", { count })}</span></div><i className={count ? "attention" : ""} /></header>
      <div className="notification-list">
        {activeIncidents.map((incident) => <button key={incident.id} onClick={() => { setOpen(false); onOpenIncidents(); }}>
          <span className="notification-icon incident"><ShieldAlert size={15} /></span><div><strong>{t("notifications.incident")}</strong><small>{incident.id} · {formatAge(incident.lastFailureAt)}</small></div><ChevronRight size={14} />
        </button>)}
        {activeAlerts.map((alert) => <button key={alert.id} onClick={() => { setOpen(false); alert.requestId ? onOpenRequest(alert.requestId) : onOpenIncidents(); }}>
          <span className="notification-icon"><TriangleAlert size={15} /></span><div><strong>{alert.message}</strong><small>{formatAge(alert.createdAt)}</small></div><ChevronRight size={14} />
        </button>)}
        {!count && <div className="notification-empty"><Bell size={18} /><p>{t("notifications.empty")}</p></div>}
      </div>
    </div>}
  </div>;
}
