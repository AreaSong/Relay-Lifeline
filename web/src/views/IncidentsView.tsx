import { CircleCheck, Filter, ListTree, RotateCcw, ShieldAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import { FormEvent, useEffect, useState } from "react";
import { errorMessage, type ApiClient } from "../api";
import type { HistoryRecord, Incident, IncidentDetail, ListFilters } from "../types";

function localDateTime(value: string) {
	if (!value) return "";
	const date = new Date(value);
	return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
}

export function IncidentsView({ api, incidents, filters, onApplyFilters, selectedId, onOpen, hasMore, onLoadMore }: { api: ApiClient; incidents: Incident[]; filters: ListFilters; onApplyFilters: (filters: ListFilters) => void; selectedId?: string; onOpen: (record: HistoryRecord) => void; hasMore: boolean; onLoadMore: () => void }) {
  const { t, i18n } = useTranslation(["incidents", "overview", "common"]);
	const [draft, setDraft] = useState(filters);
	const [expandedId, setExpandedId] = useState<string>();
	const [detail, setDetail] = useState<IncidentDetail>();
	const [loadError, setLoadError] = useState("");
	useEffect(() => setDraft(filters), [filters]);
	function submit(event: FormEvent) {
		event.preventDefault();
		onApplyFilters({ ...draft, from: draft.from ? new Date(draft.from).toISOString() : "", to: draft.to ? new Date(draft.to).toISOString() : "" });
	}
	async function toggleDetail(id: string) {
		if (expandedId === id) { setExpandedId(undefined); return; }
		setExpandedId(id); setLoadError("");
		try { setDetail(await api.incident(id)); }
		catch (reason) { setDetail(undefined); setLoadError(errorMessage(reason)); }
	}
  useEffect(() => {
    if (!selectedId) return;
    const selected = document.getElementById(`incident-${selectedId}`);
    selected?.scrollIntoView({ block: "center" }); selected?.focus({ preventScroll: true });
  }, [selectedId]);
  return <div className="page-stack incidents-view">
    <form className="server-list-filters" onSubmit={submit}>
      <label className="field"><span>{t("filters.search")}</span><input value={draft.q} onChange={(event) => setDraft((current) => ({ ...current, q: event.target.value }))} /></label>
      <label className="field"><span>{t("filters.state")}</span><select value={draft.state} onChange={(event) => setDraft((current) => ({ ...current, state: event.target.value }))}><option value="">{t("filters.allStates")}</option><option value="open">{t("state.open")}</option><option value="recovering">{t("state.recovering")}</option><option value="resolved">{t("state.resolved")}</option></select></label>
      <label className="field"><span>{t("filters.from")}</span><input type="datetime-local" value={localDateTime(draft.from)} onChange={(event) => setDraft((current) => ({ ...current, from: event.target.value }))} /></label>
      <label className="field"><span>{t("filters.to")}</span><input type="datetime-local" value={localDateTime(draft.to)} onChange={(event) => setDraft((current) => ({ ...current, to: event.target.value }))} /></label>
      <div className="server-list-filter-actions"><button className="button primary" type="submit"><Filter size={16} />{t("filters.apply")}</button><button className="icon-button" type="button" aria-label={t("filters.reset")} data-tooltip={t("filters.reset")} onClick={() => onApplyFilters({ q: "", state: "", from: "", to: "" })}><RotateCcw size={16} /></button></div>
    </form>
    <section className="content-section">
    <div className="section-heading"><div><h2>{t("title")}</h2><p>{t("description")}</p></div><span>{t("count", { count: incidents.length })}</span></div>
    {!incidents.length ? <div className="empty-state"><CircleCheck size={24} /><span>{t("empty")}</span></div> : <div className="incident-list">
      {incidents.map((incident) => <article id={`incident-${incident.id}`} tabIndex={-1} className={`incident-row state-${incident.state}${selectedId === incident.id ? " selected" : ""}`} key={incident.id}>
        <div className="incident-row__heading"><span><ShieldAlert size={18} /></span><div><strong>{t(`state.${incident.state}`)}</strong><code>{incident.id}</code></div><time>{new Date(incident.startedAt).toLocaleString(i18n.resolvedLanguage)}</time></div>
        <dl>
          <div><dt>{t("failedAttempts")}</dt><dd>{incident.failedAttempts}</dd></div>
			<div><dt>{t("affectedRequests")}</dt><dd><button className="link-button" onClick={() => void toggleDetail(incident.id)}>{incident.affectedRequests.length} · {t("viewRequests")}</button></dd></div>
          <div><dt>{t("lastFailure")}</dt><dd>{new Date(incident.lastFailureAt).toLocaleString(i18n.resolvedLanguage)}</dd></div>
		  <div><dt>{t("categories")}</dt><dd>{Object.entries(incident.categories).map(([name, count]) => `${t(`overview:errorCategories.${name}`, { defaultValue: name })} ${count}`).join(" · ") || "-"}</dd></div>
          <div><dt>{t("statusCodes")}</dt><dd>{Object.entries(incident.statusCodes).map(([code, count]) => `HTTP ${code} × ${count}`).join(" · ") || "-"}</dd></div>
          {incident.resolvedAt && <div><dt>{t("resolvedAt")}</dt><dd>{new Date(incident.resolvedAt).toLocaleString(i18n.resolvedLanguage)}</dd></div>}
		</dl>
		{expandedId === incident.id && <div className="incident-requests">
			{loadError && <div className="error-banner">{loadError}</div>}
			{detail?.incident.id === incident.id && <section className="incident-timeline" aria-label={t("timeline.title")}><h3>{t("timeline.title")}</h3>{detail.timeline.map((event, index) => <article key={`${event.time}-${event.type}-${index}`} className={`incident-timeline-event ${event.type.startsWith("incident_") ? "lifecycle" : "request"}`}><i /><div><strong>{event.message || event.type}</strong><time>{new Date(event.time).toLocaleString(i18n.resolvedLanguage)}</time>{event.requestId && <button className="link-button" onClick={() => { const record = detail.requests.find((request) => request.id === event.requestId); if (record) onOpen(record); }}>{event.requestId}</button>}<small>{[event.attempt ? `#${event.attempt}` : "", event.statusCode ? `HTTP ${event.statusCode}` : "", event.category || "", event.attemptPhase || ""].filter(Boolean).join(" · ")}</small></div></article>)}</section>}
			{detail?.incident.id === incident.id && (detail.requests.length ? detail.requests.map((record) => <button key={record.id} onClick={() => onOpen(record)}><ListTree size={15} /><span><strong>{record.method} {record.path}</strong><small>{record.id}</small></span><span className={`status ${record.state}`}>{t(`common:status.${record.state}`, { defaultValue: record.state })}</span></button>) : <div className="empty-state compact">{t("requestsUnavailable")}</div>)}
			{detail?.affectedRequestsTruncated && <small className="subtle">{t("requestsTruncated")}</small>}
		</div>}
      </article>)}
		</div>}
		{hasMore && <div className="list-load-more"><button className="button" onClick={onLoadMore}>{t("loadMore")}</button></div>}
    </section>
  </div>;
}
