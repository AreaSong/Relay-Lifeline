import { ShieldCheck, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import { formatAge } from "../format";
import type { Alert } from "../types";

export function AlertsList({ alerts }: { alerts: Alert[] }) {
  const { t } = useTranslation("overview");
  if (!alerts.length) return <div className="empty-state compact"><ShieldCheck size={22} /><span>{t("alerts.empty")}</span></div>;
  return <div className="alert-list">{alerts.slice(0, 6).map((alert) => <div className={`alert-row ${alert.resolvedAt ? "resolved" : ""}`} key={alert.id}>
    <span className="alert-icon"><TriangleAlert size={17} /></span>
    <div><strong>{alert.message}</strong><span>{formatAge(alert.createdAt)}{alert.resolvedAt ? ` · ${t("alerts.resolved")}` : ""}</span></div>
  </div>)}</div>;
}
