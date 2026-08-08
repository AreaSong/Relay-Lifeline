import { ArrowUpRight, Ban, CheckCircle2, CirclePause, Clock3, Inbox, Lightbulb, ListTree, RotateCcw, Send, ShieldAlert, ShieldCheck, TriangleAlert, X, XCircle } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { formatAge, formatDuration, formatTime } from "../format";
import type { Alert, Incident, RequestInfo } from "../types";

interface Props {
  alerts: Alert[];
  incidents: Incident[];
  requests: RequestInfo[];
  locale: string;
  onOpen: (id: string) => void;
  idSuffix?: string;
  paused?: boolean;
  selectedRequestId?: string;
  onClearSelected?: () => void;
}

function TimelineEventIcon({ type }: { type: string }) {
  if (type === "completed") return <CheckCircle2 size={15} />;
  if (type === "attempt_failed" || type === "delivery_failed") return <XCircle size={15} />;
  if (type === "waiting") return <Clock3 size={15} />;
  if (type === "retry_resumed" || type === "retry_requested") return <RotateCcw size={15} />;
  if (type === "risk_warning") return <ShieldAlert size={15} />;
  if (type === "canceled" || type === "cancel_requested") return <Ban size={15} />;
  if (type === "attempt_started") return <Send size={15} />;
  return <Inbox size={15} />;
}

export function OverviewPriorityPanel({ alerts, incidents, requests, locale, onOpen, idSuffix = "main", paused = false, selectedRequestId, onClearSelected }: Props) {
  const { t } = useTranslation(["overview", "common", "incidents", "requests"]);
  const incident = useMemo(
    () => incidents.find((item) => item.state !== "resolved"),
    [incidents],
  );
  const selectedRequest = useMemo(
    () => selectedRequestId ? requests.find((req) => req.id === selectedRequestId) : null,
    [requests, selectedRequestId],
  );
  const activeAlerts = alerts.filter((alert) => !alert.resolvedAt).slice(0, 2);
  const waitingRequest = requests.find((request) => request.state === "waiting" || request.state === "queued");
  const request = selectedRequest || waitingRequest || requests[0];
  const categories = incident
    ? Object.entries(incident.categories).sort((left, right) => right[1] - left[1]).slice(0, 3)
    : [];
  const state = selectedRequest ? "request" : paused ? "paused" : incident ? "incident" : waitingRequest ? "waiting" : request ? "request" : activeAlerts.length ? "alert" : "stable";

  const generatedEvents = useMemo(() => {
    if (!selectedRequest) return [];
    const eventsList = [
      {
        type: "received",
        message: t("overview:priority.receivedRequest", { defaultValue: "收到请求" }),
        time: selectedRequest.startedAt,
      },
      {
        type: "attempt_started",
        message: t("overview:priority.sendingToUpstream", { defaultValue: "开始向 CPA 发送请求" }),
        time: selectedRequest.updatedAt || selectedRequest.startedAt,
        attempt: selectedRequest.attempt,
      },
    ];
    if (selectedRequest.lastError) {
      eventsList.push({
        type: "attempt_failed",
        message: selectedRequest.lastError,
        time: selectedRequest.updatedAt || selectedRequest.startedAt,
        attempt: selectedRequest.attempt,
      });
    }
    return eventsList;
  }, [selectedRequest, t]);

  const titleId = `priority-title-${idSuffix}`;
  return <section className={`overview-priority glass-panel-floating priority-mode-${state}`} aria-labelledby={titleId}>
    <header className="priority-heading">
      <div>
        <span className="panel-kicker">{selectedRequest ? t("overview:priority.latestRequest") : t("overview:priority.kicker")}</span>
        <h2 id={titleId}>{selectedRequest ? `${selectedRequest.method} ${selectedRequest.path}` : t("overview:priority.title")}</h2>
      </div>
      <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
        {selectedRequest ? <span className={`status ${selectedRequest.state}`}><i />{t(`common:status.${selectedRequest.state}`, { defaultValue: selectedRequest.state })}</span> : <span className={`priority-state state-${state}`}><i />{t(`overview:priority.${state}`)}</span>}
        {selectedRequest && <button className="icon-button" aria-label="Close" onClick={onClearSelected} style={{ width: 28, height: 28, padding: 0 }}><X size={15} /></button>}
      </div>
    </header>

    <div className="priority-body" style={{ animation: "fade-slide-up 300ms ease-out" }}>
      {selectedRequest && <div className="priority-timeline-embedded">
        <div className="timeline-summary" style={{ display: "flex", justifyContent: "space-between", padding: "10px 14px", background: "var(--surface-raised)", borderRadius: "var(--radius-md)", margin: "0 0 14px 0", fontSize: 12 }}>
          <span>{t("requests:timeline.attempts")} <strong>{selectedRequest.attempt}</strong></span>
          <span>{t("requests:timeline.duration")} <strong>{formatDuration(selectedRequest.startedAt, selectedRequest.updatedAt)}</strong></span>
        </div>

        <div className="timeline-list" style={{ display: "flex", flexDirection: "column", gap: "14px" }}>
          {generatedEvents.map((event, index) => <div className={`timeline-event ${event.type}`} key={`${event.time}-${index}`} style={{ display: "flex", gap: "12px", alignItems: "flex-start" }}>
            <span className="event-icon" style={{ width: 28, height: 28, borderRadius: "50%", background: "var(--neon-emerald-soft)", color: "var(--neon-emerald-bright)", display: "grid", placeItems: "center", flexShrink: 0 }}>
              <TimelineEventIcon type={event.type} />
            </span>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div className="event-heading" style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 2 }}>
                <strong style={{ fontSize: 13, color: "var(--text)" }}>{event.message}</strong>
                <time style={{ font: "11px var(--font-mono)", color: "var(--text-faint)" }}>{formatTime(event.time)}</time>
              </div>
              {event.attempt && <div className="event-meta" style={{ font: "11px var(--font-mono)", color: "var(--text-soft)" }}>{t("common:time.attempt", { count: event.attempt })}</div>}
            </div>
          </div>)}
        </div>

        {selectedRequest.lastError && <div className="safe-error-detail" style={{ marginTop: 14, padding: 12, background: "rgba(239, 68, 68, 0.1)", border: "1px solid rgba(239, 68, 68, 0.3)", borderRadius: "var(--radius-md)" }}>
          <strong style={{ color: "#ef4444", fontSize: 12 }}>{selectedRequest.lastErrorCode || "ERROR"}</strong>
          <p style={{ fontSize: 12, color: "var(--text-soft)", margin: "4px 0 0 0" }}>{selectedRequest.lastError}</p>
        </div>}
      </div>}

      {!selectedRequest && state === "paused" && <div className="priority-stable priority-paused">
        <span className="priority-icon"><CirclePause size={22} /></span>
        <strong>{t("overview:priority.pausedHeadline")}</strong>
        <p>{t("overview:priority.pausedDescription")}</p>
      </div>}

      {!selectedRequest && state === "incident" && incident && <article className={`priority-incident state-${incident.state}`}>
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

      {!selectedRequest && (state === "waiting" || state === "request") && request && <article className="priority-request">
        <span className="priority-icon"><ListTree size={19} /></span>
        <div className="priority-copy"><span>{t("overview:priority.latestRequest")}</span><strong>{request.method} {request.path}</strong><code>{request.id}</code></div>
        <dl className="priority-metrics">
          <div><dt>{t("overview:priority.requestState")}</dt><dd><span className={`status ${request.state}`}>{t(`common:status.${request.state}`, { defaultValue: request.state })}</span></dd></div>
          <div><dt>{t("overview:priority.attempts")}</dt><dd>{request.attempt}</dd></div>
          <div><dt>{t("overview:priority.started")}</dt><dd>{formatAge(request.startedAt)}</dd></div>
        </dl>
        <button className="priority-link" onClick={() => onOpen(request.id)}>{t("common:actions.openTimeline")}<ArrowUpRight size={15} /></button>
      </article>}

      {!selectedRequest && state === "alert" && activeAlerts[0] && <article className="priority-request priority-alert">
        <span className="priority-icon"><TriangleAlert size={19} /></span>
        <div className="priority-copy"><span>{t("overview:priority.alert")}</span><strong>{activeAlerts[0].message}</strong><code>{activeAlerts[0].id}</code></div>
        {activeAlerts[0].requestId && <button className="priority-link" onClick={() => onOpen(activeAlerts[0].requestId!)}>{t("common:actions.openTimeline")}<ArrowUpRight size={15} /></button>}
      </article>}

      {!selectedRequest && state === "stable" && <div className="priority-stable">
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
