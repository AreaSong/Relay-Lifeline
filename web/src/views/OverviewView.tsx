import { Activity, RotateCcw, ShieldCheck, TriangleAlert } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import type { ApiClient } from "../api";
import { AlertsList } from "../components/AlertsList";
import { OperationsCharts, operationsChartTheme } from "../components/OperationsCharts";
import { RequestsTable } from "../components/RequestsTable";
import { SignalTopology } from "../components/SignalTopology";
import type { Alert, MetricsErrors, MetricsSnapshot, Status } from "../types";

export function OverviewView({ status, metrics, errors, alerts, api, refresh, onOpen, onError, locale, dark, incident }: {
  status: Status;
  metrics: MetricsSnapshot | null;
  errors: MetricsErrors | null;
  alerts: Alert[];
  api: ApiClient;
  refresh: () => Promise<void>;
  onOpen: (id: string) => void;
  onError: (message: string) => void;
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
    failedAttempts: t("overview:charts.failedAttempts"), active: t("overview:charts.active"),
    waiting: t("overview:charts.waiting"), queued: t("overview:charts.queued"), duration: t("overview:charts.duration"),
  }), [locale, t]);
  const topologyLabels = useMemo(() => ({
    ariaLabel: t("overview:signal.topologyLabel"), codex: t("overview:signal.codex"), relay: t("overview:signal.relay"), cpa: t("overview:signal.cpa"),
    healthy: t("overview:signal.healthy"), degraded: t("overview:signal.degraded"), unknown: t("overview:signal.unknown"),
    active: t("overview:signal.active"), waiting: t("overview:signal.waiting"), nextRetry: t("overview:signal.nextRetry"),
    retryNow: t("overview:signal.retryNow"), staticFallback: t("overview:signal.staticFallback"),
  }), [locale, t]);
  const chartTheme = useMemo(() => operationsChartTheme(dark), [dark]);

  return <div className="page-stack overview-view">
    <section className={`signal-hero${incident ? " incident" : ""}`}>
      <div className="signal-hero-copy"><span className="signal-kicker"><i />{t("overview:signal.kicker")}</span><h2>{heroTitle}</h2><p>{heroDescription}</p></div>
      <SignalTopology
        upstreamState={state}
        active={status.active}
        waiting={status.waiting}
        nextRetryAt={nextRetryAt}
        locale={locale}
        labels={topologyLabels}
      />
      {incident && <div className="incident-strip" role="status">
        <div className="incident-state"><span>{t("overview:incident.title")}</span><strong>{t("common:status.upstreamDegraded")}</strong></div>
        <div><span>{t("overview:incident.started")}</span><strong>{status.upstream.lastChecked ? new Date(status.upstream.lastChecked).toLocaleTimeString(locale) : t("common:notAvailable")}</strong></div>
        <div><span>{t("overview:incident.affected")}</span><strong>{status.waiting + status.queued}</strong></div>
        <div><span>{t("overview:incident.dominant")}</span><strong>{dominantError ? t(`overview:errorCategories.${dominantError}`, { defaultValue: dominantError }) : t("overview:incident.noRetry")}</strong></div>
        <div><span>{t("overview:incident.nextRetry")}</span><strong>{nextRetryAt ? new Date(nextRetryAt).toLocaleTimeString(locale) : t("overview:incident.noRetry")}</strong></div>
      </div>}
    </section>

    <div className="telemetry-strip">
      <div className="telemetry-cell"><Activity size={19} /><div><span>{t("overview:stats.active")}</span><strong>{status.active}</strong></div></div>
      <div className="telemetry-cell"><RotateCcw size={19} /><div><span>{t("overview:stats.recovering")}</span><strong>{status.waiting + status.queued}</strong></div></div>
      <div className="telemetry-cell"><ShieldCheck size={19} /><div><span>{t("overview:stats.successful")}</span><strong>{status.successful}</strong></div></div>
      <div className="telemetry-cell"><TriangleAlert size={19} /><div><span>{t("overview:stats.failedAttempts")}</span><strong>{status.failedAttempts}</strong></div></div>
    </div>

    <OperationsCharts
      reliability={metrics?.series || []}
      pressure={metrics?.series || []}
      errors={localizedErrors}
      recovery={recovery}
      labels={labels}
      theme={chartTheme}
      locale={locale}
    />

    <div className="overview-lower">
      <section className="content-section"><div className="section-heading"><div><h2>{t("overview:recent.title")}</h2><p>{t("overview:recent.description")}</p></div><span className={`mode ${status.paused ? "paused" : ""}`}>{status.paused ? t("common:status.paused") : t("common:status.running")}</span></div>
        <RequestsTable requests={status.requests.slice(0, 6)} api={api} refresh={refresh} onOpen={onOpen} onError={onError} />
      </section>
      <div className="overview-side"><section className="content-section"><div className="section-heading"><div><h2>{t("overview:alerts.title")}</h2><p>{t("overview:alerts.description")}</p></div></div><AlertsList alerts={alerts} /></section></div>
    </div>
  </div>;
}
