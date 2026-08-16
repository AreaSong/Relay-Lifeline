import { Ban, CheckCircle2, Clock3, Inbox, RotateCcw, Send, ShieldAlert, XCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { formatBytes, formatDuration, formatTime } from "../format";
import type { ErrorDetail, HistoryRecord, TimelineEvent } from "../types";
import { InspectorShell } from "./InspectorShell";

function EventIcon({ type }: { type: string }) {
  if (type === "completed") return <CheckCircle2 />;
  if (type === "attempt_failed" || type === "delivery_failed") return <XCircle />;
  if (type === "waiting") return <Clock3 />;
  if (type === "retry_resumed" || type === "retry_requested") return <RotateCcw />;
  if (type === "risk_warning") return <ShieldAlert />;
  if (type === "canceled" || type === "cancel_requested") return <Ban />;
  if (type === "attempt_started") return <Send />;
  return <Inbox />;
}

function EventMeta({ event }: { event: TimelineEvent }) {
  const { t } = useTranslation("common");
  const details = [event.attempt ? t("time.attempt", { count: event.attempt }) : "", event.statusCode ? t("httpError", { status: event.statusCode }) : ""]
    .filter(Boolean).join(" · ");
  return <>{details && <span>{details}</span>}{event.waitMilliseconds ? <span>{t("time.waitSeconds", { count: Math.round(event.waitMilliseconds / 1000) })}</span> : null}</>;
}

function SafeErrorDetail({ detail }: { detail: ErrorDetail }) {
  const { t } = useTranslation("requests");
  const metadata = [
    detail.type && [t("errorDetail.type"), detail.type],
    detail.code && [t("errorDetail.code"), detail.code],
    detail.upstreamRequestId && [t("errorDetail.requestId"), detail.upstreamRequestId],
    detail.retryAfter && [t("errorDetail.retryAfter"), detail.retryAfter],
    detail.responseBytes !== undefined && [t("errorDetail.responseBytes"), formatBytes(detail.responseBytes)],
  ].filter(Boolean) as string[][];
  return <div className="safe-error-detail">
    <strong>{t("errorDetail.title")}</strong>
    <p>{detail.parsed ? detail.message || t("errorDetail.structured") : t("errorDetail.unparsed")}</p>
    {metadata.length > 0 && <dl>{metadata.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>}
  </div>;
}

export function TimelinePanel({ record, onClose }: { record: HistoryRecord; onClose: () => void }) {
  const { t } = useTranslation(["common", "requests"]);
  return <InspectorShell
    className="timeline-inspector"
    title={`${record.method} ${record.path}`}
    subtitle={record.id}
    status={<span className={`status ${record.state}`}>{t(`common:status.${record.state}`, { defaultValue: record.state })}</span>}
    onClose={onClose}
  >
      <div className="timeline-summary"><span>{t("requests:timeline.attempts")} <strong>{record.attempt}</strong></span><span>{t("requests:timeline.duration")} <strong>{formatDuration(record.startedAt, record.completedAt)}</strong></span>{record.taskId && <span>{t("requests:identity.task")} <strong>{record.taskId}</strong></span>}{record.clientId && <span>{t("requests:identity.client")} <strong>{record.clientId}</strong></span>}</div>
      {record.eventsTruncated && <div className="warning-banner page-banner" role="status">{t("requests:timeline.truncated", { count: record.droppedEvents || 0 })}</div>}
      <div className="timeline-list">{record.events.map((event, index) => <div className={`timeline-event ${event.type}`} key={`${event.time}-${index}`}>
        <span className="event-icon"><EventIcon type={event.type} /></span>
        <div><div className="event-heading"><strong>{event.message}</strong><time>{formatTime(event.time)}</time></div><div className="event-meta"><EventMeta event={event} /></div>{event.errorDetail && <SafeErrorDetail detail={event.errorDetail} />}</div>
      </div>)}</div>
  </InspectorShell>;
}
