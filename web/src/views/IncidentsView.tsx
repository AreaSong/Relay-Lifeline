import { CircleCheck, ListTree, ShieldAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { errorMessage, type ApiClient } from "../api";
import type { HistoryRecord, Incident, IncidentDetail } from "../types";

export function IncidentsView({ api, incidents, selectedId, onOpen, hasMore, onLoadMore }: { api: ApiClient; incidents: Incident[]; selectedId?: string; onOpen: (record: HistoryRecord) => void; hasMore: boolean; onLoadMore: () => void }) {
  const { t } = useTranslation("incidents");
	const [expandedId, setExpandedId] = useState<string>();
	const [detail, setDetail] = useState<IncidentDetail>();
	const [loadError, setLoadError] = useState("");
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
  return <div className="content-section incidents-view">
    <div className="section-heading"><div><h2>{t("title")}</h2><p>{t("description")}</p></div><span>{t("count", { count: incidents.length })}</span></div>
    {!incidents.length ? <div className="empty-state"><CircleCheck size={24} /><span>{t("empty")}</span></div> : <div className="incident-list">
      {incidents.map((incident) => <article id={`incident-${incident.id}`} tabIndex={-1} className={`incident-row state-${incident.state}${selectedId === incident.id ? " selected" : ""}`} key={incident.id}>
        <div className="incident-row__heading"><span><ShieldAlert size={18} /></span><div><strong>{t(`state.${incident.state}`)}</strong><code>{incident.id}</code></div><time>{new Date(incident.startedAt).toLocaleString()}</time></div>
        <dl>
          <div><dt>{t("failedAttempts")}</dt><dd>{incident.failedAttempts}</dd></div>
			<div><dt>{t("affectedRequests")}</dt><dd><button className="link-button" onClick={() => void toggleDetail(incident.id)}>{incident.affectedRequests.length} · {t("viewRequests")}</button></dd></div>
          <div><dt>{t("lastFailure")}</dt><dd>{new Date(incident.lastFailureAt).toLocaleString()}</dd></div>
          <div><dt>{t("categories")}</dt><dd>{Object.entries(incident.categories).map(([name, count]) => `${name} ${count}`).join(" · ") || "-"}</dd></div>
          <div><dt>{t("statusCodes")}</dt><dd>{Object.entries(incident.statusCodes).map(([code, count]) => `HTTP ${code} × ${count}`).join(" · ") || "-"}</dd></div>
          {incident.resolvedAt && <div><dt>{t("resolvedAt")}</dt><dd>{new Date(incident.resolvedAt).toLocaleString()}</dd></div>}
		</dl>
		{expandedId === incident.id && <div className="incident-requests">
			{loadError && <div className="error-banner">{loadError}</div>}
			{detail?.incident.id === incident.id && (detail.requests.length ? detail.requests.map((record) => <button key={record.id} onClick={() => onOpen(record)}><ListTree size={15} /><span><strong>{record.method} {record.path}</strong><small>{record.id}</small></span><span className={`status ${record.state}`}>{record.state}</span></button>) : <div className="empty-state compact">{t("requestsUnavailable")}</div>)}
			{detail?.affectedRequestsTruncated && <small className="subtle">{t("requestsTruncated")}</small>}
		</div>}
      </article>)}
		</div>}
		{hasMore && <div className="list-load-more"><button className="button" onClick={onLoadMore}>{t("loadMore")}</button></div>}
  </div>;
}
