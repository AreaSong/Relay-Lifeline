import { Activity, RotateCcw, ShieldCheck, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { MetricsSnapshot, MetricsWindow, Status } from "../types";

export function OverviewMetrics({ status, metrics, window }: {
  status: Status;
  metrics: MetricsSnapshot | null;
  window: MetricsWindow;
}) {
  const { t } = useTranslation(["common", "overview"]);
  const hasRequests = !!metrics && metrics.totals.requests > 0;
  const successRate = hasRequests ? `${metrics.totals.successRate.toFixed(metrics.totals.successRate < 100 ? 1 : 0)}%` : "—";
  const items = [
    { key: "active", icon: Activity, value: status.active, tone: "active", scope: t("overview:stats.live") },
    { key: "recovering", icon: RotateCcw, value: status.waiting + status.queued, tone: "warning", scope: t("overview:stats.live") },
    { key: "successRate", icon: ShieldCheck, value: successRate, tone: "success", scope: t(`overview:charts.windows.${window}`) },
    { key: "failedAttempts", icon: TriangleAlert, value: metrics?.totals.failedAttempts ?? status.failedAttempts, tone: "danger", scope: t(`overview:charts.windows.${window}`) },
  ] as const;

  return <section className="overview-metrics" aria-label={t("overview:cockpit.title")}>
    {items.map(({ key, icon: Icon, value, tone, scope }) => <article className={`metric-card tone-${tone}`} key={key}>
      <header><span>{t(`overview:stats.${key}`)}</span><i><Icon size={18} /></i></header>
      <strong>{value}</strong>
      <footer><span className={key === "active" || key === "recovering" ? "live" : ""}>{scope}</span><small>{t("overview:cockpit.observation")}</small></footer>
    </article>)}
  </section>;
}
