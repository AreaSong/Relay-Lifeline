import { ArrowUpRight, ListTree, ShieldAlert, ShieldCheck, TriangleAlert } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { formatAge } from "../format";
import type { Alert, Incident, RequestInfo } from "../types";

interface Props {
  alerts: Alert[];
  incidents: Incident[];
  requests: RequestInfo[];
  locale: string;
  onOpen: (id: string) => void;
}

export function OverviewPriorityPanel({ alerts, incidents, requests, locale, onOpen }: Props) {
  const { t } = useTranslation(["overview", "common", "incidents"]);
  const incident = useMemo(
    () => incidents.find((item) => item.state !== "resolved") || incidents[0],
    [incidents],
  );
  const request = requests[0];
  const activeAlerts = alerts.filter((alert) => !alert.resolvedAt).slice(0, 2);
  const categories = incident
    ? Object.entries(incident.categories).sort((left, right) => right[1] - left[1]).slice(0, 3)
    : [];
  const state = incident ? "incident" : request ? "request" : "stable";

  return <section className={`overview-priority priority-mode-${state}`} aria-labelledby="priority-title">
    <header className="priority-heading">
      <div><span className="panel-kicker">{t("overview:priority.kicker")}</span><h2 id="priority-title">{t("overview:priority.title")}</h2></div>
      <span className={`priority-state state-${state}`}><i />{t(`overview:priority.${state}`)}</span>
    </header>

    <div className="priority-body">
      {incident && <article className={`priority-incident state-${incident.state}`}>
        <span className="priority-icon"><ShieldAlert size={19} /></span>
        <div className="priority-copy">
          <span>{t(`incidents:state.${incident.state}`)}</span>
          <strong>{t("overview:priority.incidentHeadline")}</strong>
          <code>{incident.id}</code>
        </div>
        <dl className="priority-metrics">
          <div><dt>{t("overview:priority.affected")}</dt><dd>{incident.affectedRequests.length}</dd></div>
          <div><dt>{t("overview:priority.attempts")}</dt><dd>{incident.failedAttempts}</dd></div>
          <div><dt>{t("overview:priority.lastFailure")}</dt><dd>{formatAge(incident.lastFailureAt)}</dd></div>
        </dl>
        {categories.length > 0 && <div className="priority-categories" aria-label={t("overview:priority.categories")}>
          {categories.map(([category, count]) => <span key={category}>{t(`overview:errorCategories.${category}`, { defaultValue: category })}<b>{count}</b></span>)}
        </div>}
        <a className="priority-link" href="#/incidents">{t("overview:priority.openIncidents")}<ArrowUpRight size={15} /></a>
      </article>}

      {!incident && request && <article className="priority-request">
        <span className="priority-icon"><ListTree size={19} /></span>
        <div className="priority-copy"><span>{t("overview:priority.latestRequest")}</span><strong>{request.method} {request.path}</strong><code>{request.id}</code></div>
        <dl className="priority-metrics">
          <div><dt>{t("overview:priority.requestState")}</dt><dd><span className={`status ${request.state}`}>{t(`common:status.${request.state}`, { defaultValue: request.state })}</span></dd></div>
          <div><dt>{t("overview:priority.attempts")}</dt><dd>{request.attempt}</dd></div>
          <div><dt>{t("overview:priority.started")}</dt><dd>{formatAge(request.startedAt)}</dd></div>
        </dl>
        <button className="priority-link" onClick={() => onOpen(request.id)}>{t("common:actions.openTimeline")}<ArrowUpRight size={15} /></button>
      </article>}

      {!incident && !request && <div className="priority-stable">
        <span className="priority-icon"><ShieldCheck size={22} /></span>
        <strong>{t("overview:priority.stableHeadline")}</strong>
        <p>{t("overview:priority.stableDescription")}</p>
      </div>}
    </div>

    <footer className="priority-alerts">
      <div className="priority-alerts-heading"><span>{t("overview:priority.alerts")}</span><b>{activeAlerts.length}</b></div>
      {activeAlerts.length > 0 ? activeAlerts.map((alert) => <article key={alert.id}>
        <TriangleAlert size={14} /><div><strong>{alert.message}</strong><time dateTime={alert.createdAt}>{new Date(alert.createdAt).toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" })}</time></div>
      </article>) : <p>{t("overview:alerts.empty")}</p>}
    </footer>
  </section>;
}
