import { ArrowUpRight, CirclePause, Lightbulb, ListTree, ShieldAlert, ShieldCheck, TriangleAlert } from "lucide-react";
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
  idSuffix?: string;
  paused?: boolean;
}

export function OverviewPriorityPanel({ alerts, incidents, requests, locale, onOpen, idSuffix = "main", paused = false }: Props) {
  const { t } = useTranslation(["overview", "common", "incidents"]);
  const incident = useMemo(
    () => incidents.find((item) => item.state !== "resolved"),
    [incidents],
  );
  const activeAlerts = alerts.filter((alert) => !alert.resolvedAt).slice(0, 2);
  const waitingRequest = requests.find((request) => request.state === "waiting" || request.state === "queued");
  const request = waitingRequest || requests[0];
  const categories = incident
    ? Object.entries(incident.categories).sort((left, right) => right[1] - left[1]).slice(0, 3)
    : [];
  const state = paused ? "paused" : incident ? "incident" : waitingRequest ? "waiting" : request ? "request" : activeAlerts.length ? "alert" : "stable";

  const titleId = `priority-title-${idSuffix}`;
  return <section className={`overview-priority priority-mode-${state}`} aria-labelledby={titleId}>
    <header className="priority-heading">
      <div><span className="panel-kicker">{t("overview:priority.kicker")}</span><h2 id={titleId}>{t("overview:priority.title")}</h2></div>
      <span className={`priority-state state-${state}`}><i />{t(`overview:priority.${state}`)}</span>
    </header>

    <div className="priority-body">
      {state === "paused" && <div className="priority-stable priority-paused">
        <span className="priority-icon"><CirclePause size={22} /></span>
        <strong>{t("overview:priority.pausedHeadline")}</strong>
        <p>{t("overview:priority.pausedDescription")}</p>
      </div>}

      {state === "incident" && incident && <article className={`priority-incident state-${incident.state}`}>
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

      {(state === "waiting" || state === "request") && request && <article className="priority-request">
        <span className="priority-icon"><ListTree size={19} /></span>
        <div className="priority-copy"><span>{t("overview:priority.latestRequest")}</span><strong>{request.method} {request.path}</strong><code>{request.id}</code></div>
        <dl className="priority-metrics">
          <div><dt>{t("overview:priority.requestState")}</dt><dd><span className={`status ${request.state}`}>{t(`common:status.${request.state}`, { defaultValue: request.state })}</span></dd></div>
          <div><dt>{t("overview:priority.attempts")}</dt><dd>{request.attempt}</dd></div>
          <div><dt>{t("overview:priority.started")}</dt><dd>{formatAge(request.startedAt)}</dd></div>
        </dl>
        <button className="priority-link" onClick={() => onOpen(request.id)}>{t("common:actions.openTimeline")}<ArrowUpRight size={15} /></button>
      </article>}

      {state === "alert" && activeAlerts[0] && <article className="priority-request priority-alert">
        <span className="priority-icon"><TriangleAlert size={19} /></span>
        <div className="priority-copy"><span>{t("overview:priority.alert")}</span><strong>{activeAlerts[0].message}</strong><code>{activeAlerts[0].id}</code></div>
        {activeAlerts[0].requestId && <button className="priority-link" onClick={() => onOpen(activeAlerts[0].requestId!)}>{t("common:actions.openTimeline")}<ArrowUpRight size={15} /></button>}
      </article>}

      {state === "stable" && <div className="priority-stable">
        <span className="priority-icon"><ShieldCheck size={22} /></span>
        <strong>{t("overview:priority.stableHeadline")}</strong>
        <p>{t("overview:priority.stableDescription")}</p>
      </div>}
    </div>

    <footer className="priority-alerts">
      <div className="priority-advice"><span><Lightbulb size={14} />{t("overview:priority.adviceTitle")}</span><p>{t(`overview:priority.advice.${state}`)}</p><small>{t("overview:priority.ruleBased")}</small></div>
      <div className="priority-alerts-heading"><span>{t("overview:priority.alerts")}</span><b>{activeAlerts.length}</b></div>
      {activeAlerts.length > 0 ? activeAlerts.map((alert) => <article key={alert.id}>
        <TriangleAlert size={14} /><div><strong>{alert.message}</strong><time dateTime={alert.createdAt}>{new Date(alert.createdAt).toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" })}</time></div>
      </article>) : <p>{t("overview:alerts.empty")}</p>}
    </footer>
  </section>;
}
