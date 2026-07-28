import { Activity, RadioTower, RotateCcw, ShieldCheck, TriangleAlert } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { OperationsCharts, operationsChartTheme } from "../components/OperationsCharts";
import { OverviewPriorityPanel } from "../components/OverviewPriorityPanel";
import { SignalTopology } from "../components/SignalTopology";
import type { Alert, Incident, MetricsErrors, MetricsSnapshot, MetricsWindow, Status } from "../types";

const windows: MetricsWindow[] = ["15m", "1h", "6h", "24h"];

export function OverviewView({ status, metrics, errors, alerts, incidents, window, onWindowChange, onOpen, locale, dark, incident }: {
  status: Status;
  metrics: MetricsSnapshot | null;
  errors: MetricsErrors | null;
  alerts: Alert[];
  incidents: Incident[];
  window: MetricsWindow;
  onWindowChange: (window: MetricsWindow) => void;
  onOpen: (id: string) => void;
  locale: string;
  dark: boolean;
  incident: boolean;
}) {
  const { t } = useTranslation(["common", "overview"]);
  const nextRetryAt = useMemo(() => status.requests.map((request) => request.nextRetryAt).filter(Boolean).sort()[0], [status.requests]);
  const state = status.upstream.state;
  const heroTitle = t(`overview:signal.${state}Title`);
  const heroDescription = t(`overview:signal.${state}Description`);
  const dominantError = errors?.categories.find((category) => category.count > 0)?.code;
  const localizedErrors = useMemo(() => (errors?.categories || []).map((category) => ({
    category: t(`overview:errorCategories.${category.code}`, { defaultValue: category.code }), count: category.count,
  })), [errors, locale, t]);
  const recovery = useMemo(() => (metrics?.recovery.durationBuckets || []).map((bucket) => ({
    bucket: t(`overview:recoveryBuckets.${bucket.bucket}`, { defaultValue: bucket.bucket }), count: bucket.count,
  })), [locale, metrics?.recovery.durationBuckets, t]);
  const labels = useMemo(() => ({
    reliabilityTitle: t("overview:charts.reliability"), pressureTitle: t("overview:charts.pressure"),
    errorsTitle: t("overview:charts.errors"), recoveryTitle: t("overview:charts.recovery"),
    empty: t("overview:charts.empty"), unavailable: t("overview:charts.unavailable"),
    requests: t("overview:charts.requests"), successRate: t("overview:charts.successRate"),
    failedAttempts: t("overview:charts.failedAttempts"), active: t("overview:charts.active"), requesting: t("overview:charts.requesting"),
    waiting: t("overview:charts.waiting"), queued: t("overview:charts.queued"), duration: t("overview:charts.duration"),
    expand: t("overview:charts.expand"), collapse: t("overview:charts.collapse"),
  }), [locale, t]);
  const topologyLabels = useMemo(() => ({
    ariaLabel: t("overview:signal.topologyLabel"), codex: t("overview:signal.codex"), relay: t("overview:signal.relay"), cpa: t("overview:signal.cpa"),
    healthy: t("overview:signal.healthy"), degraded: t("overview:signal.degraded"), unknown: t("overview:signal.unknown"),
    active: t("overview:signal.active"), waiting: t("overview:signal.waiting"), nextRetry: t("overview:signal.nextRetry"),
    retryNow: t("overview:signal.retryNow"), staticFallback: t("overview:signal.staticFallback"),
  }), [locale, t]);
  const chartTheme = useMemo(() => operationsChartTheme(dark), [dark]);
  const successRate = metrics ? `${metrics.totals.successRate.toFixed(metrics.totals.successRate < 100 ? 1 : 0)}%` : t("common:notAvailable");

  return <div className="overview-view">
    <section className={`signal-hero${incident ? " incident" : ""}`}>
      <div className="signal-hero-copy"><span className="signal-kicker"><RadioTower size={13} />{t("overview:signal.kicker")}</span><h2>{heroTitle}</h2><p>{heroDescription}</p></div>
      <SignalTopology
        upstreamState={state}
        active={status.active}
        waiting={status.waiting}
        nextRetryAt={nextRetryAt}
        locale={locale}
        labels={topologyLabels}
      />
      <div className="signal-context" aria-hidden="true"><span>{t("overview:incident.dominant")}</span><strong>{dominantError ? t(`overview:errorCategories.${dominantError}`, { defaultValue: dominantError }) : t("overview:incident.stable")}</strong></div>
    </section>

    <section className="telemetry-board" aria-labelledby="telemetry-title">
      <header className="telemetry-heading"><div><span className="panel-kicker">{t("overview:cockpit.kicker")}</span><h2 id="telemetry-title">{t("overview:cockpit.title")}</h2></div>
        <div className="telemetry-window"><span className={`window-integrity ${metrics?.complete ? "complete" : "partial"}`}><i />{t(metrics?.complete ? "overview:cockpit.complete" : "overview:cockpit.partial")}</span><div className="segmented-control" role="group" aria-label={t("overview:charts.window")}>
          {windows.map((value) => <button key={value} className={window === value ? "active" : ""} aria-pressed={window === value} onClick={() => onWindowChange(value)}>{t(`overview:charts.windows.${value}`)}</button>)}
        </div></div>
      </header>
      <div className="telemetry-strip">
        <div className="telemetry-cell"><Activity size={18} /><div><span>{t("overview:stats.active")}</span><strong>{status.active}</strong><small>{t("overview:stats.live")}</small></div></div>
        <div className="telemetry-cell warning"><RotateCcw size={18} /><div><span>{t("overview:stats.recovering")}</span><strong>{status.waiting + status.queued}</strong><small>{t("overview:stats.live")}</small></div></div>
        <div className="telemetry-cell success"><ShieldCheck size={18} /><div><span>{t("overview:stats.successRate")}</span><strong>{successRate}</strong><small>{t(`overview:charts.windows.${window}`)}</small></div></div>
        <div className="telemetry-cell warning"><TriangleAlert size={18} /><div><span>{t("overview:stats.failedAttempts")}</span><strong>{metrics?.totals.failedAttempts ?? status.failedAttempts}</strong><small>{t(`overview:charts.windows.${window}`)}</small></div></div>
      </div>
    </section>

    <OverviewPriorityPanel alerts={alerts} incidents={incidents} requests={status.requests} locale={locale} onOpen={onOpen} />

    <OperationsCharts
      reliability={metrics?.series || []}
      pressure={metrics?.series || []}
      errors={localizedErrors}
      recovery={recovery}
      labels={labels}
      theme={chartTheme}
      locale={locale}
      className="overview-chart-system"
      preferredSecondary={incident ? "errors" : "pressure"}
    />
  </div>;
}
