import { RadioTower } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { OperationsCharts, operationsChartTheme } from "../components/OperationsCharts";
import { OverviewMetrics } from "../components/OverviewMetrics";
import { OverviewPriorityPanel } from "../components/OverviewPriorityPanel";
import { SignalTopology } from "../components/SignalTopology";
import type { Alert, Incident, MetricsErrors, MetricsSnapshot, MetricsWindow, Status } from "../types";

export function OverviewView({ status, metrics, errors, alerts, incidents, window, onOpen, locale, dark, incident, selectedRequestId }: {
  status: Status;
  metrics: MetricsSnapshot | null;
  errors: MetricsErrors | null;
  alerts: Alert[];
  incidents: Incident[];
  window: MetricsWindow;
  onOpen: (id: string) => void;
  locale: string;
  dark: boolean;
  incident: boolean;
  selectedRequestId?: string;
}) {
  const { t } = useTranslation(["common", "overview"]);
  const [activeSelectedId, setActiveSelectedId] = useState<string | null>(null);
  const currentSelectedId = activeSelectedId || selectedRequestId;

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
    stateLabels: {
      requesting: t("common:status.requesting"), waiting: t("common:status.waiting"), queued: t("common:status.queued"), running: t("common:status.running"),
    },
  }), [locale, t]);
  const chartTheme = useMemo(() => operationsChartTheme(dark), [dark]);
  return <div className="overview-view">
    <OverviewMetrics status={status} metrics={metrics} window={window} />
    <section className={`signal-hero${incident ? " incident" : ""}`}>
      <div className="signal-hero-copy"><span className="signal-kicker"><RadioTower size={13} />{t("overview:signal.kicker")}</span><h2>{heroTitle}</h2><p>{heroDescription}</p></div>
      <SignalTopology
        upstreamState={state}
        active={status.active}
        waiting={status.waiting}
        requests={status.requests}
        nextRetryAt={nextRetryAt}
        locale={locale}
        labels={topologyLabels}
        onSelect={(id) => {
          if (!id) {
            setActiveSelectedId(null);
            onOpen("");
          } else {
            setActiveSelectedId(id);
            onOpen(id);
          }
        }}
        selectedRequestId={currentSelectedId}
      />
      <div className="signal-context" aria-hidden="true"><span>{t("overview:incident.dominant")}</span><strong>{dominantError ? t(`overview:errorCategories.${dominantError}`, { defaultValue: dominantError }) : t("overview:incident.stable")}</strong></div>
    </section>

    <OverviewPriorityPanel
      alerts={alerts}
      incidents={incidents}
      requests={status.requests}
      locale={locale}
      onOpen={onOpen}
      idSuffix="main"
      paused={status.paused}
      selectedRequestId={currentSelectedId}
      onClearSelected={() => setActiveSelectedId(null)}
    />

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
