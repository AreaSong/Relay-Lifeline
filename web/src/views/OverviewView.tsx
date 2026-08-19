import { RadioTower, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { OperationsCharts, operationsChartTheme } from "../components/OperationsCharts";
import { OverviewMetrics } from "../components/OverviewMetrics";
import { OverviewPriorityPanel } from "../components/OverviewPriorityPanel";
import { SignalTopology } from "../components/SignalTopology";
import type { Alert, GovernanceStatus, HealthComponent, HealthSummary, Incident, MetricsErrors, MetricsSnapshot, MetricsWindow, PolicyStatus, Status } from "../types";

export function OverviewView({ status, healthSummary = null, metrics, errors, alerts, incidents, window, onOpen, locale, dark, incident, selectedRequestId, governanceStatus = null, policyStatus = null }: {
  status: Status;
  healthSummary?: HealthSummary | null;
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
  governanceStatus?: GovernanceStatus | null;
  policyStatus?: PolicyStatus | null;
}) {
  const { t } = useTranslation(["common", "overview"]);
  const [activeSelectedId, setActiveSelectedId] = useState<string | null>(null);
  const [selectedHealth, setSelectedHealth] = useState<HealthComponent | null>(null);
	const healthDrawerRef = useRef<HTMLElement>(null);
	const healthCloseRef = useRef<HTMLButtonElement>(null);
  const currentSelectedId = activeSelectedId || selectedRequestId;
  const slo = healthSummary?.components.find((component) => component.name === "slo");
  const uncertain = healthSummary?.components.find((component) => component.name === "uncertain-delivery");
  const uncertainOpen = healthNumber(uncertain?.details?.open) ?? status.requests.filter((request) => request.state === "uncertain").length;
  const uncertainOldestSeconds = healthNumber(uncertain?.details?.oldestSeconds);
  const uncertainTargetSeconds = healthNumber(uncertain?.details?.targetSeconds);

	useEffect(() => {
		if (!selectedHealth) return;
		const previous = document.activeElement as HTMLElement | null;
		healthCloseRef.current?.focus();
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.key === "Escape") {
				event.preventDefault(); event.stopPropagation(); setSelectedHealth(null); return;
			}
			if (event.key !== "Tab" || !healthDrawerRef.current) return;
			const focusable = Array.from(healthDrawerRef.current.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')).filter((element) => !element.hasAttribute("disabled"));
			if (!focusable.length) return;
			const first = focusable[0], last = focusable[focusable.length - 1];
			if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
			else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
		};
		document.addEventListener("keydown", onKeyDown, true);
		return () => { document.removeEventListener("keydown", onKeyDown, true); previous?.focus(); };
	}, [selectedHealth]);

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
	<div className="overview-health-band">
    {healthSummary && <section className={`health-summary-panel ${healthSummary.overall}`} aria-labelledby="health-summary-title">
      <div className="section-heading"><div><span className="section-kicker">{t("overview:health.kicker")}</span><h2 id="health-summary-title">{t("overview:health.title")}</h2><p>{t("overview:health.updated", { time: new Date(healthSummary.generatedAt).toLocaleTimeString(locale) })}</p></div><span className={`status ${healthSummary.overall}`}>{t(`common:health.${healthSummary.overall}`, { defaultValue: healthSummary.overall })}</span></div>
      <div className="health-summary-grid">{healthSummary.components.map((component) => <button type="button" className={`health-summary-item ${component.healthy ? "healthy" : "degraded"}`} key={component.name} onClick={() => setSelectedHealth(component)}><span><i />{t(`overview:health.components.${healthKey(component.name)}`, { defaultValue: component.name })}</span><strong>{t(`common:health.${component.state}`, { defaultValue: component.state })}</strong>{component.details && <small>{Object.entries(component.details).filter(([, value]) => typeof value === "number" || typeof value === "string").slice(0, 2).map(([key, value]) => `${t(`overview:health.details.${key}`, { defaultValue: key })}: ${formatHealthValue(key, value, locale)}`).join(" · ")}</small>}</button>)}</div>
      {!!healthSummary.actions?.length && <div className="health-summary-actions">{healthSummary.actions.map((action) => <span key={action}>{t(`overview:health.actions.${action}`, { defaultValue: action })}</span>)}</div>}
    </section>}
	{slo?.details && <section className={`slo-overview-strip ${slo.healthy ? "healthy" : "degraded"}`} aria-label={t("overview:slo.title")}><div><span>{t("overview:health.details.availability")}</span><strong>{formatRatio(slo.details.availability)}</strong></div><div><span>{t("overview:health.details.errorBudgetRemaining")}</span><strong>{formatRatio(slo.details.errorBudgetRemaining)}</strong></div><div><span>{t("overview:health.details.burnRate")}</span><strong>{formatNumber(slo.details.burnRate)}x</strong></div><div><span>{t("overview:health.components.slo")}</span><strong>{t(`common:health.${slo.state}`, { defaultValue: slo.state })}</strong></div></section>}
	{selectedHealth && <div className="health-detail-backdrop" role="presentation" onMouseDown={() => setSelectedHealth(null)}><aside ref={healthDrawerRef} tabIndex={-1} className="health-detail-drawer" role="dialog" aria-modal="true" aria-labelledby="health-detail-title" onMouseDown={(event) => event.stopPropagation()}><header><div><span>{t("overview:health.kicker")}</span><h2 id="health-detail-title">{t(`overview:health.components.${healthKey(selectedHealth.name)}`, { defaultValue: selectedHealth.name })}</h2></div><button ref={healthCloseRef} className="icon-button" type="button" aria-label={t("common:actions.close")} onClick={() => setSelectedHealth(null)}><X size={17} /></button></header><dl>{Object.entries(selectedHealth.details || {}).map(([key, value]) => <div key={key}><dt>{t(`overview:health.details.${key}`, { defaultValue: key })}</dt><dd>{formatHealthValue(key, value, locale)}</dd></div>)}</dl></aside></div>}
		{metrics && <section className="operational-rate-strip" aria-label={t("overview:health.ratesTitle")}><div><span>{t("overview:health.retryAmplification")}</span><strong>{metrics.totals.requests ? (metrics.totals.attempts / metrics.totals.requests).toFixed(2) : "0.00"}x</strong></div><div><span>{t("overview:health.failoverRate")}</span><strong>{metrics.totals.requests ? `${((metrics.totals.failovers ?? 0) / metrics.totals.requests * 100).toFixed(1)}%` : "0.0%"}</strong></div><div><span>{t("overview:health.uncertain")}</span><strong>{metrics.totals.uncertain ?? 0}</strong></div><div><span>{t("overview:health.persistenceFailures")}</span><strong>{metrics.totals.persistenceFailures ?? 0}</strong></div><div><span>{t("overview:health.captureFailures")}</span><strong>{metrics.totals.captureFailures ?? 0}</strong></div></section>}
		<section className="overview-control-panel" aria-label={t("overview:control.title")}>
		  <article className={`overview-control-card uncertain${uncertainOpen > 0 ? " warning" : " healthy"}`}><header><span>{t("overview:control.uncertain")}</span><strong>{uncertainOpen}</strong></header><dl><div><dt>{t("overview:control.uncertainOldest")}</dt><dd>{uncertainOldestSeconds != null ? formatAge(uncertainOldestSeconds, locale) : t("common:notAvailable")}</dd></div><div><dt>{t("overview:control.uncertainTarget")}</dt><dd>{uncertainTargetSeconds != null ? formatAge(uncertainTargetSeconds, locale) : t("common:notAvailable")}</dd></div></dl><small>{uncertainOpen > 0 ? t("overview:control.uncertainOpen") : t("overview:control.uncertainClear")}</small></article>
		  <article className={`overview-control-card governance${governanceStatus?.softThreshold ? " warning" : ""}`}><header><span>{t("overview:control.governance")}</span><strong>{governanceStatus ? governanceStatus.mode === "enforce" ? t("overview:control.enforce") : t("overview:control.observe") : t("overview:control.unavailable")}</strong></header><dl><div><dt>{t("overview:control.governancePrincipals")}</dt><dd>{governanceStatus?.principals ?? t("common:notAvailable")}</dd></div><div><dt>{t("overview:control.governanceReservations")}</dt><dd>{governanceStatus?.reservations ?? t("common:notAvailable")}</dd></div><div><dt>{t("overview:control.governanceThreshold")}</dt><dd>{governanceStatus ? governanceStatus.softThreshold ? t("overview:control.active") : t("overview:control.clear") : t("common:notAvailable")}</dd></div><div><dt>{t("overview:control.governanceForecast")}</dt><dd>{governanceStatus?.estimatedExhaustionMinutes && governanceStatus.estimatedExhaustionMinutes > 0 ? t("overview:control.forecastMinutes", { minutes: governanceStatus.estimatedExhaustionMinutes.toFixed(1) }) : t("overview:control.forecastUnavailable")}</dd></div></dl></article>
		  <article className={`overview-control-card adaptive${policyStatus?.adaptiveStopped ? " warning" : ""}`}><header><span>{t("overview:control.adaptive")}</span><strong>{policyStatus ? policyStatus.adaptiveStopped ? t("overview:control.stopped") : t("overview:control.running") : t("overview:control.unavailable")}</strong></header><dl><div><dt>{t("overview:control.adaptiveSwitches")}</dt><dd>{policyStatus?.adaptiveSwitches ?? t("common:notAvailable")}</dd></div><div><dt>{t("overview:control.adaptiveTarget")}</dt><dd>{policyStatus?.adaptiveLastTargetId || t("common:notAvailable")}</dd></div><div><dt>{t("overview:control.adaptiveScore")}</dt><dd>{policyStatus?.adaptiveLastScore != null ? policyStatus.adaptiveLastScore.toFixed(3) : t("common:notAvailable")}</dd></div></dl><small>{policyStatus?.adaptiveStopped ? t("overview:control.stoppedReason", { reason: policyStatus.adaptiveStopReason || t("common:notAvailable") }) : policyStatus ? t("overview:control.runningDescription") : t("overview:control.unavailable")}</small></article>
		</section>
		</div>
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

function healthKey(name: string) {
  return name.replaceAll(":", "_").replaceAll("-", "_");
}

function formatHealthValue(key: string, value: unknown, locale: string) {
  if (typeof value === "boolean") return value ? "✓" : "×";
  if (typeof value === "number" && key.toLowerCase().includes("bytes")) return new Intl.NumberFormat(locale, { notation: "compact", maximumFractionDigits: 1 }).format(value) + "B";
  if (typeof value === "number") return new Intl.NumberFormat(locale, { maximumFractionDigits: 3 }).format(value);
  return String(value ?? "-");
}

function formatRatio(value: unknown) {
	return typeof value === "number" ? `${(value * 100).toFixed(2)}%` : "-";
}

function formatNumber(value: unknown) {
	return typeof value === "number" ? value.toFixed(2) : "-";
}

function healthNumber(value: unknown) {
	return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function formatAge(seconds: number, locale: string) {
	if (seconds < 60) return `${Math.max(0, Math.round(seconds))}s`;
	if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
	return new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(seconds / 3600) + "h";
}
