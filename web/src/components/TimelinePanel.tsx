import { useEffect, useRef } from "react";
import { Ban, CheckCircle2, Clock3, Inbox, RotateCcw, Send, ShieldAlert, X, XCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { formatBytes, formatDuration, formatTime } from "../format";
import type { ErrorDetail, HistoryRecord, TimelineEvent } from "../types";

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
  const drawer = useRef<HTMLElement>(null);
  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    drawer.current?.querySelector<HTMLElement>("button")?.focus();
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
      if (event.key !== "Tab" || !drawer.current) return;
      const focusable = Array.from(drawer.current.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')).filter((element) => !element.hasAttribute("disabled"));
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", handleKey);
    return () => { window.removeEventListener("keydown", handleKey); previous?.focus(); };
  }, [onClose]);
  return <div className="drawer-backdrop" onMouseDown={onClose}>
    <aside ref={drawer} className="timeline-drawer" role="dialog" aria-modal="true" aria-label={t("requests:timeline.label")} onMouseDown={(event) => event.stopPropagation()}>
      <div className="drawer-header"><div><span className={`status ${record.state}`}>{t(`common:status.${record.state}`, { defaultValue: record.state })}</span><h2>{record.method} {record.path}</h2><p>{record.id}</p></div>
        <button className="icon-button" aria-label={t("common:actions.close")} data-tooltip={t("common:actions.close")} onClick={onClose}><X size={18} /></button>
      </div>
      <div className="timeline-summary"><span>{t("requests:timeline.attempts")} <strong>{record.attempt}</strong></span><span>{t("requests:timeline.duration")} <strong>{formatDuration(record.startedAt, record.completedAt)}</strong></span></div>
      {record.eventsTruncated && <div className="warning-banner page-banner" role="status">{t("requests:timeline.truncated", { count: record.droppedEvents || 0 })}</div>}
      <div className="timeline-list">{record.events.map((event, index) => <div className={`timeline-event ${event.type}`} key={`${event.time}-${index}`}>
        <span className="event-icon"><EventIcon type={event.type} /></span>
        <div><div className="event-heading"><strong>{event.message}</strong><time>{formatTime(event.time)}</time></div><div className="event-meta"><EventMeta event={event} /></div>{event.errorDetail && <SafeErrorDetail detail={event.errorDetail} />}</div>
      </div>)}</div>
    </aside>
  </div>;
}
