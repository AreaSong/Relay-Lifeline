import { Activity, ListTree, Square, TimerReset } from "lucide-react";
import { useTranslation } from "react-i18next";
import { errorMessage, type ApiClient } from "../api";
import { formatAge } from "../format";
import type { RequestInfo } from "../types";

interface Props {
  requests: RequestInfo[];
  api: ApiClient;
  refresh: () => Promise<void>;
  onOpen: (id: string) => void;
  onError: (message: string) => void;
}

export function RequestsTable({ requests, api, refresh, onOpen, onError }: Props) {
  const { t, i18n } = useTranslation(["common", "requests"]);
  async function act(action: () => Promise<unknown>) {
    try {
      await action();
      await refresh();
    } catch (reason) {
      onError(errorMessage(reason));
    }
  }
  if (!requests.length) return <div className="empty-state"><Activity size={24} /><span>{t("requests:emptyActive")}</span></div>;
  return <div className="table-wrap"><table><thead><tr>
    <th>{t("requests:columns.request")}</th><th>{t("requests:columns.status")}</th><th>{t("requests:columns.attempts")}</th><th>{t("requests:columns.duration")}</th><th>{t("requests:columns.nextRetry")}</th><th aria-label={t("requests:columns.actions")} />
  </tr></thead><tbody>{requests.map((request) => <tr key={request.id}>
    <td><strong>{request.method} {request.path}</strong><span className="subtle">{request.id}</span></td>
    <td><span className={`status ${request.state}`}>{t(`common:status.${request.state}`, { defaultValue: request.state })}</span>{request.lastError && <span className="subtle">{request.lastError}</span>}</td>
    <td>{request.attempt}</td><td>{formatAge(request.startedAt)}</td>
    <td>{request.nextRetryAt ? new Date(request.nextRetryAt).toLocaleTimeString(i18n.resolvedLanguage) : t("common:notAvailable")}</td>
    <td><div className="row-actions">
      <button className="icon-button" data-tooltip={t("common:actions.openTimeline")} aria-label={t("common:actions.openTimeline")} onClick={() => onOpen(request.id)}><ListTree size={17} /></button>
      <button className="icon-button" data-tooltip={t("common:actions.retry")} aria-label={t("common:actions.retry")} onClick={() => act(() => api.retry(request.id))}><TimerReset size={17} /></button>
      <button className="icon-button danger" data-tooltip={t("common:actions.cancel")} aria-label={t("common:actions.cancel")} onClick={() => act(() => api.cancel(request.id))}><Square size={16} /></button>
    </div></td>
  </tr>)}</tbody></table></div>;
}
