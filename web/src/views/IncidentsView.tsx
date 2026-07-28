import { CircleCheck, ShieldAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { Incident } from "../types";

export function IncidentsView({ incidents }: { incidents: Incident[] }) {
  const { t } = useTranslation("incidents");
  return <div className="content-section incidents-view">
    <div className="section-heading"><div><h2>{t("title")}</h2><p>{t("description")}</p></div><span>{t("count", { count: incidents.length })}</span></div>
    {!incidents.length ? <div className="empty-state"><CircleCheck size={24} /><span>{t("empty")}</span></div> : <div className="incident-list">
      {incidents.map((incident) => <article className={`incident-row state-${incident.state}`} key={incident.id}>
        <div className="incident-row__heading"><span><ShieldAlert size={18} /></span><div><strong>{t(`state.${incident.state}`)}</strong><code>{incident.id}</code></div><time>{new Date(incident.startedAt).toLocaleString()}</time></div>
        <dl>
          <div><dt>{t("failedAttempts")}</dt><dd>{incident.failedAttempts}</dd></div>
          <div><dt>{t("affectedRequests")}</dt><dd>{incident.affectedRequests.length}</dd></div>
          <div><dt>{t("lastFailure")}</dt><dd>{new Date(incident.lastFailureAt).toLocaleString()}</dd></div>
          <div><dt>{t("categories")}</dt><dd>{Object.entries(incident.categories).map(([name, count]) => `${name} ${count}`).join(" · ") || "-"}</dd></div>
          <div><dt>{t("statusCodes")}</dt><dd>{Object.entries(incident.statusCodes).map(([code, count]) => `HTTP ${code} × ${count}`).join(" · ") || "-"}</dd></div>
          {incident.resolvedAt && <div><dt>{t("resolvedAt")}</dt><dd>{new Date(incident.resolvedAt).toLocaleString()}</dd></div>}
        </dl>
      </article>)}
    </div>}
  </div>;
}
