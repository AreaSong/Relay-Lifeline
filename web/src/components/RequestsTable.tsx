import { Activity, ListTree, Repeat2, Square, TimerReset } from "lucide-react";
import { useTranslation } from "react-i18next";
import { errorMessage, type ApiClient } from "../api";
import { formatAge, formatOptionalTime } from "../format";
import type { RequestInfo } from "../types";

interface Props {
  requests: RequestInfo[];
  api: ApiClient;
  refresh: () => Promise<void>;
  onOpen: (id: string) => void;
  onError: (message: string) => void;
  canOperate: boolean;
  onPolicy: (request: RequestInfo) => void;
}

export function RequestsTable({ requests, api, refresh, onOpen, onError, canOperate, onPolicy }: Props) {
  const { t, i18n } = useTranslation(["common", "requests"]);
  async function act(action: () => Promise<unknown>) {
    try {
      await action();
      await refresh();
    } catch (reason) {
      onError(errorMessage(reason));
    }
  }
  if (!requests.length) return <div className="empty-state request-empty"><Activity size={20} /><span>{t("requests:emptyActive")}</span></div>;
  return <div className="table-wrap responsive-table"><table><thead><tr>
    <th>{t("requests:columns.request")}</th><th>{t("requests:columns.status")}</th><th>{t("requests:columns.attempts")}</th><th>{t("requests:columns.duration")}</th><th>{t("requests:columns.nextRetry")}</th><th aria-label={t("requests:columns.actions")} />
  </tr></thead><tbody>{requests.map((request) => <tr key={request.id}>
    <td data-label={t("requests:columns.request")}><strong>{request.method} {request.path}</strong><span className="subtle">{request.id}</span></td>
    <td data-label={t("requests:columns.status")}><span className={`status ${request.state}`}>{t(`common:status.${request.state}`, { defaultValue: request.state })}</span>{request.lastError && <span className="subtle">{request.lastError}</span>}</td>
    <td data-label={t("requests:columns.attempts")}>{request.attempt}</td><td data-label={t("requests:columns.duration")}>{formatAge(request.startedAt)}</td>
    <td data-label={t("requests:columns.nextRetry")}>{formatOptionalTime(request.nextRetryAt, i18n.resolvedLanguage, t("common:notAvailable"))}</td>
    <td data-label={t("requests:columns.actions")}><div className="row-actions">
      <button className="icon-button" data-tooltip={t("common:actions.openTimeline")} aria-label={t("common:actions.openTimeline")} onClick={() => onOpen(request.id)}><ListTree size={17} /></button>
      {canOperate && <button className="icon-button" data-tooltip={t("common:actions.retry")} aria-label={t("common:actions.retry")} onClick={() => act(() => api.retry(request.id))}><TimerReset size={17} /></button>}
      {canOperate && <button className="icon-button" data-tooltip={t("requests:repeat.action")} aria-label={t("requests:repeat.action")} onClick={() => onPolicy(request)}><Repeat2 size={17} /></button>}
      {canOperate && <button className="icon-button danger" data-tooltip={t("common:actions.cancel")} aria-label={t("common:actions.cancel")} onClick={() => act(() => api.cancel(request.id))}><Square size={16} /></button>}
    </div></td>
  </tr>)}</tbody></table></div>;
}
