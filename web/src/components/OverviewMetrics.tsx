import { Activity, Clock, RotateCcw, ShieldCheck, TriangleAlert } from "lucide-react";
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
  const recoveryTime = metrics && metrics.totals.averageRecoveryMilliseconds > 0 
    ? `${(metrics.totals.averageRecoveryMilliseconds / 1000).toFixed(1)}s` 
    : "—";

  const items = [
    { key: "active", icon: Activity, value: status.active, tone: "active", scope: t("overview:stats.live") },
    { key: "recovering", icon: RotateCcw, value: status.waiting + status.queued, tone: "warning", scope: t("overview:stats.live") },
    { key: "successRate", icon: ShieldCheck, value: successRate, tone: "success", scope: t(`overview:charts.windows.${window}`) },
    { key: "recoveryTime", icon: Clock, value: recoveryTime, tone: "active", scope: t(`overview:charts.windows.${window}`) },
    { key: "failedAttempts", icon: TriangleAlert, value: metrics?.totals.failedAttempts ?? status.failedAttempts, tone: "danger", scope: t(`overview:charts.windows.${window}`) },
  ] as const;

  return <section className="overview-metrics" aria-label={t("overview:cockpit.title")}>
    {items.map(({ key, icon: Icon, value, tone, scope }) => <article className={`metric-card tone-${tone}`} key={key}>
      <header><span>{t(`overview:stats.${key}`, { defaultValue: key === "recoveryTime" ? "平均恢复耗时" : key })}</span><i><Icon size={16} /></i></header>
      <div className="metric-reading"><strong>{value}</strong><span className={key === "active" || key === "recovering" ? "live" : ""}>{scope}</span></div>
    </article>)}
  </section>;
}
