import { Activity, ListTree, Repeat2, Settings2, ShieldAlert, Square, TimerReset } from "lucide-react";
import { useTranslation } from "react-i18next";
import { formatAge, formatOptionalTime, formatSeconds } from "../format";
import type { RequestInfo, RetryPolicyInfo } from "../types";

interface Props {
  requests: RequestInfo[];
  selected: Set<string>;
  onToggle: (id: string) => void;
  onToggleAll: () => void;
  onOpen: (id: string) => void;
  onRetry: (request: RequestInfo) => void;
  onPolicy: (request: RequestInfo) => void;
  onRepeat: (request: RequestInfo) => void;
  onCancel: (request: RequestInfo) => void;
  onResolveUncertain?: (request: RequestInfo) => void;
  canOperate: boolean;
}

function isUncertain(request: RequestInfo) {
  return request.state === "uncertain";
}

function canRetry(request: RequestInfo) {
  return !isUncertain(request) && (request.actions?.canRetryNow ?? request.state === "waiting");
}

function canSetPolicy(request: RequestInfo) {
  return !isUncertain(request) && (request.actions?.canSetRetryPolicy ?? ["queued", "requesting", "waiting"].includes(request.state));
}

function canRepeat(request: RequestInfo) {
  return !isUncertain(request) && (request.actions?.canRepeat ?? request.state !== "uncertain");
}

function hasLegacyPolicy(request: RequestInfo) {
  if ((request.retryIntervalMilliseconds || 0) > 0) return true;
  if (!request.retryDeadline) return false;
  const deadline = new Date(request.retryDeadline);
  return !Number.isNaN(deadline.getTime()) && deadline.getUTCFullYear() > 1;
}

function policyFor(request: RequestInfo): RetryPolicyInfo | undefined {
  if (request.retryPolicy) return request.retryPolicy;
  // Older servers only expose the legacy deadline/interval pair. Keep that
  // snapshot useful while the browser is talking to a rolling deployment.
  if (!hasLegacyPolicy(request)) return undefined;
  const deadline = request.retryDeadline;
  const durationMilliseconds = deadline ? Math.max(0, new Date(deadline).getTime() - Date.now()) : 0;
  return {
    state: deadline && new Date(deadline).getTime() > Date.now() ? "active" : "pending",
    durationMilliseconds,
    schedule: { mode: "fixed", intervalMilliseconds: request.retryIntervalMilliseconds || undefined },
    additionalAttemptsUsed: 0,
    honorRetryAfter: true,
    appliedAt: request.startedAt,
    deadline,
  };
}

function intervalLabel(policy: RetryPolicyInfo, t: (key: string, options?: Record<string, unknown>) => string) {
  const schedule = policy.schedule;
  const seconds = (value = 0) => formatSeconds(Math.max(0, Math.round(value / 1000)));
  switch (schedule.mode) {
    case "fixed": return t("requests:retryPolicy.summary.fixed", { interval: seconds(schedule.intervalMilliseconds) });
    case "random": return t("requests:retryPolicy.summary.random", { minimum: seconds(schedule.minimumIntervalMilliseconds), maximum: seconds(schedule.maximumIntervalMilliseconds) });
    case "exponential": return t("requests:retryPolicy.summary.exponential", { base: seconds(schedule.baseIntervalMilliseconds), maximum: seconds(schedule.maximumIntervalMilliseconds) });
    default: return t(`requests:retryPolicy.summary.${schedule.mode}`);
  }
}

export function RequestsTable({ requests, selected, onToggle, onToggleAll, onOpen, onRetry, onPolicy, onRepeat, onCancel, onResolveUncertain, canOperate }: Props) {
  const { t, i18n } = useTranslation(["common", "requests"]);
  if (!requests.length) return <div className="empty-state request-empty"><Activity size={20} /><span>{t("requests:emptyActive")}</span></div>;
  const allSelected = requests.every((request) => selected.has(request.id));
  const someSelected = !allSelected && requests.some((request) => selected.has(request.id));
  return <div className="table-wrap responsive-table"><table><thead><tr>
    {canOperate && <th className="selection-column"><input type="checkbox" checked={allSelected} ref={(element) => { if (element) element.indeterminate = someSelected; }} onChange={onToggleAll} aria-label={t("requests:batch.selectAll")} /></th>}
    <th>{t("requests:columns.request")}</th><th>{t("requests:columns.status")}</th><th>{t("requests:columns.attempts")}</th><th>{t("requests:columns.duration")}</th><th>{t("requests:columns.nextRetry")}</th><th>{t("requests:columns.policy")}</th><th aria-label={t("requests:columns.actions")} />
  </tr></thead><tbody>{requests.map((request) => {
    const policy = policyFor(request);
    const uncertain = isUncertain(request);
    return <tr key={request.id} className={`${selected.has(request.id) ? "selected " : ""}${uncertain ? "uncertain-row" : ""}`}>
      {canOperate && <td className="selection-column" data-label={t("requests:batch.select")}><input type="checkbox" checked={selected.has(request.id)} onChange={() => onToggle(request.id)} aria-label={t("requests:batch.selectRequest", { id: request.id })} /></td>}
      <td className="request-cell" data-label={t("requests:columns.request")}><strong>{request.method} {request.path}</strong><span className="subtle">{request.id}</span>{request.taskId && <span className="subtle client-identity">{t("requests:identity.task")}: {request.taskId}</span>}{request.clientId && <span className="subtle client-identity">{t("requests:identity.client")}: {request.clientId}</span>}</td>
      <td data-label={t("requests:columns.status")}><span className={`status ${request.state}`}>{t(`common:status.${request.state}`, { defaultValue: request.state })}</span>{request.lastError && <span className="subtle">{request.lastError}</span>}</td>
      <td data-label={t("requests:columns.attempts")}>{request.attempt}</td><td data-label={t("requests:columns.duration")}>{formatAge(request.startedAt)}</td>
      <td data-label={t("requests:columns.nextRetry")}>{formatOptionalTime(request.nextRetryAt, i18n.resolvedLanguage, t("common:notAvailable"))}</td>
      <td data-label={t("requests:columns.policy")}><button className="policy-summary" disabled={!canOperate || !canSetPolicy(request)} onClick={() => onPolicy(request)}>{policy ? <><strong>{intervalLabel(policy, t)}</strong><span>{policy.state === "pending" ? t("requests:retryPolicy.pending") : policy.deadline ? t("requests:retryPolicy.until", { time: formatOptionalTime(policy.deadline, i18n.resolvedLanguage) }) : ""}{policy.maxAdditionalAttempts ? ` · ${t("requests:retryPolicy.remaining", { count: policy.remainingAdditionalAttempts || 0 })}` : ""}</span></> : <span>{t("requests:retryPolicy.globalDefault")}</span>}</button></td>
      <td data-label={t("requests:columns.actions")}><div className="row-actions">
        <button className="icon-button" data-tooltip={t("common:actions.openTimeline")} aria-label={t("common:actions.openTimeline")} onClick={() => onOpen(request.id)}><ListTree size={17} /></button>
        {uncertain && onResolveUncertain && <button className="icon-button uncertain-action" data-tooltip={t("requests:uncertain.resolveAction")} aria-label={t("requests:uncertain.resolveAction")} onClick={() => onResolveUncertain(request)}><ShieldAlert size={17} /></button>}
        {canOperate && <button className="icon-button" disabled={!canRetry(request)} data-tooltip={canRetry(request) ? t("common:actions.retry") : t("requests:retryPolicy.retryUnavailable")} aria-label={t("common:actions.retry")} onClick={() => onRetry(request)}><TimerReset size={17} /></button>}
        {canOperate && <button className="icon-button" disabled={!canSetPolicy(request)} data-tooltip={t("requests:retryPolicy.action")} aria-label={t("requests:retryPolicy.action")} onClick={() => onPolicy(request)}><Settings2 size={17} /></button>}
        {canOperate && <button className="icon-button" disabled={!canRepeat(request)} data-tooltip={t("requests:repeat.action")} aria-label={t("requests:repeat.action")} onClick={() => onRepeat(request)}><Repeat2 size={17} /></button>}
        {canOperate && <button className="icon-button danger" disabled={uncertain || request.actions?.canCancel === false} data-tooltip={t("common:actions.cancel")} aria-label={t("common:actions.cancel")} onClick={() => onCancel(request)}><Square size={16} /></button>}
      </div></td>
    </tr>;
  })}</tbody></table></div>;
}
