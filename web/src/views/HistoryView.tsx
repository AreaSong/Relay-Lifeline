import { Archive, Filter, ListTree, RotateCcw } from "lucide-react";
import { FormEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { OperationsCharts, operationsChartTheme } from "../components/OperationsCharts";
import { formatDuration } from "../format";
import type { HistoryRecord, ListFilters, MetricsErrors, MetricsSnapshot, MetricsWindow, MonitoringEvent } from "../types";

const windows: MetricsWindow[] = ["15m", "1h", "6h", "24h"];
const windowMilliseconds: Record<MetricsWindow, number> = { "15m": 15 * 60_000, "1h": 60 * 60_000, "6h": 6 * 60 * 60_000, "24h": 24 * 60 * 60_000 };

function localDateTime(value: string) {
  if (!value) return "";
  const date = new Date(value);
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function apiDateTime(value: string) {
  return value ? new Date(value).toISOString() : "";
}

export function HistoryView({ records, filters, onApplyFilters, onOpen, metrics, errors, events, window, onWindowChange, locale, dark, hasMore, onLoadMore }: {
  records: HistoryRecord[];
  filters: ListFilters;
  onApplyFilters: (filters: ListFilters) => void;
  onOpen: (record: HistoryRecord) => void;
  metrics: MetricsSnapshot | null;
  errors: MetricsErrors | null;
  events: MonitoringEvent[];
  window: MetricsWindow;
  onWindowChange: (window: MetricsWindow) => void;
  locale: string;
  dark: boolean;
	hasMore: boolean;
	onLoadMore: () => void;
}) {
  const { t } = useTranslation(["common", "requests", "overview"]);
  const [draft, setDraft] = useState(filters);
  useEffect(() => setDraft(filters), [filters]);
  function submit(event: FormEvent) {
    event.preventDefault();
    onApplyFilters({ ...draft, from: apiDateTime(draft.from), to: apiDateTime(draft.to) });
  }
  function selectWindow(value: MetricsWindow) {
    onWindowChange(value);
    onApplyFilters({ ...filters, from: new Date(Date.now() - windowMilliseconds[value]).toISOString(), to: "" });
  }
  const localizedErrors = (errors?.categories || []).map((category) => ({ category: t(`overview:errorCategories.${category.code}`, { defaultValue: category.code }), count: category.count }));
  const recovery = (metrics?.recovery.durationBuckets || []).map((bucket) => ({ bucket: t(`overview:recoveryBuckets.${bucket.bucket}`, { defaultValue: bucket.bucket }), count: bucket.count }));
  const chartLabels = {
    reliabilityTitle: t("overview:charts.reliability"), pressureTitle: t("overview:charts.pressure"), errorsTitle: t("overview:charts.errors"), recoveryTitle: t("overview:charts.recovery"),
    empty: t("overview:charts.empty"), unavailable: t("overview:charts.unavailable"), requests: t("overview:charts.requests"), successRate: t("overview:charts.successRate"), failedAttempts: t("overview:charts.failedAttempts"),
    active: t("overview:charts.active"), requesting: t("overview:charts.requesting"), waiting: t("overview:charts.waiting"), queued: t("overview:charts.queued"), duration: t("overview:charts.duration"),
    expand: t("overview:charts.expand"), collapse: t("overview:charts.collapse"),
  };

  return <div className="page-stack history-view">
    <section className="content-section">
      <div className="section-heading"><div><h2>{t("requests:history.title")}</h2><p>{t("requests:history.description")}</p></div><div className="segmented-control" role="group" aria-label={t("requests:history.window")}>{windows.map((value) => <button key={value} className={window === value ? "active" : ""} aria-pressed={window === value} onClick={() => selectWindow(value)}>{t(`requests:windowLabels.${value}`)}</button>)}</div></div>
      <OperationsCharts className="history-chart-system" reliability={metrics?.series || []} pressure={metrics?.series || []} errors={localizedErrors} recovery={recovery} labels={chartLabels} theme={operationsChartTheme(dark)} locale={locale} />
    </section>
    <form className="server-list-filters" onSubmit={submit}>
      <label className="field"><span>{t("requests:filters.search")}</span><input value={draft.q} onChange={(event) => setDraft((current) => ({ ...current, q: event.target.value }))} /></label>
      <label className="field"><span>{t("requests:filters.state")}</span><select value={draft.state} onChange={(event) => setDraft((current) => ({ ...current, state: event.target.value }))}><option value="">{t("requests:filters.allStates")}</option><option value="successful">{t("common:status.successful")}</option><option value="failed">{t("common:status.failed")}</option><option value="canceled">{t("common:status.canceled")}</option><option value="uncertain">{t("common:status.uncertain")}</option></select></label>
      <label className="field"><span>{t("requests:filters.from")}</span><input type="datetime-local" value={localDateTime(draft.from)} onChange={(event) => setDraft((current) => ({ ...current, from: event.target.value }))} /></label>
      <label className="field"><span>{t("requests:filters.to")}</span><input type="datetime-local" value={localDateTime(draft.to)} onChange={(event) => setDraft((current) => ({ ...current, to: event.target.value }))} /></label>
      <div className="server-list-filter-actions"><button className="button primary" type="submit"><Filter size={16} />{t("requests:filters.apply")}</button><button className="icon-button" type="button" aria-label={t("requests:filters.reset")} data-tooltip={t("requests:filters.reset")} onClick={() => onApplyFilters({ q: "", state: "", from: "", to: "" })}><RotateCcw size={16} /></button></div>
    </form>
    <section className="content-section"><div className="section-heading"><div><h2>{t("requests:history.events")}</h2><p>{t("requests:history.description")}</p></div></div>
      {events.length ? <div className="event-track">{events.slice(-20).reverse().map((event) => <article className={`event-marker ${event.code.includes("failure") || event.code.includes("failed") || event.code.includes("rejected") ? "failure" : event.code.includes("recover") || event.code.includes("succeeded") ? "recovery" : ""}`} key={event.id}><i /><div><strong>{t(`requests:events.${event.code}`, { defaultValue: event.code })}</strong><time>{new Date(event.time).toLocaleTimeString(locale)}</time></div></article>)}</div> : <div className="empty-state compact">{t("requests:history.eventsEmpty")}</div>}
    </section>
    <section className="content-section"><div className="section-heading"><div><h2>{t("requests:history.title")}</h2><p>{t("requests:history.description")}</p></div><span>{t("common:recordCount", { count: records.length })}</span></div>
      {!records.length ? <div className="empty-state"><Archive size={24} /><span>{t("requests:history.empty")}</span></div> : <div className="table-wrap history-table responsive-table"><table><thead><tr>
        <th>{t("requests:columns.request")}</th><th>{t("requests:columns.result")}</th><th>{t("requests:columns.attempts")}</th><th>{t("requests:columns.duration")}</th><th>{t("requests:columns.lastError")}</th><th aria-label={t("requests:columns.actions")} />
      </tr></thead><tbody>{records.map((record) => <tr key={record.id}>
        <td data-label={t("requests:columns.request")}><strong>{record.method} {record.path}</strong><span className="subtle">{record.id}</span>{record.taskId && <span className="subtle client-identity">{t("requests:identity.task")}: {record.taskId}</span>}{record.clientId && <span className="subtle client-identity">{t("requests:identity.client")}: {record.clientId}</span>}</td>
        <td data-label={t("requests:columns.result")}><span className={`status ${record.state}`}>{t(`common:status.${record.state}`, { defaultValue: record.state })}</span></td>
        <td data-label={t("requests:columns.attempts")}>{record.attempt}</td><td data-label={t("requests:columns.duration")}>{formatDuration(record.startedAt, record.completedAt)}</td>
        <td data-label={t("requests:columns.lastError")}><span className="subtle history-error">{record.lastError || t("common:notAvailable")}</span></td>
        <td data-label={t("requests:columns.actions")}><div className="row-actions"><button className="icon-button" aria-label={t("common:actions.openTimeline")} data-tooltip={t("common:actions.openTimeline")} onClick={() => onOpen(record)}><ListTree size={17} /></button></div></td>
		</tr>)}</tbody></table></div>}
		{hasMore && <div className="list-load-more"><button className="button" onClick={onLoadMore}>{t("requests:history.loadMore")}</button></div>}
    </section>
  </div>;
}
