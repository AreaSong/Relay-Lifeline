import { Archive, ListTree } from "lucide-react";
import { useTranslation } from "react-i18next";
import { formatDuration } from "../format";
import type { HistoryRecord } from "../types";

export function HistoryView({ records, onOpen }: { records: HistoryRecord[]; onOpen: (record: HistoryRecord) => void }) {
  const { t } = useTranslation(["common", "requests"]);
  return <section className="content-section"><div className="section-heading"><div><h2>{t("requests:history.title")}</h2><p>{t("requests:history.description")}</p></div><span>{t("common:recordCount", { count: records.length })}</span></div>
    {!records.length ? <div className="empty-state"><Archive size={24} /><span>{t("requests:history.empty")}</span></div> : <div className="table-wrap history-table"><table><thead><tr>
      <th>{t("requests:columns.request")}</th><th>{t("requests:columns.result")}</th><th>{t("requests:columns.attempts")}</th><th>{t("requests:columns.duration")}</th><th>{t("requests:columns.lastError")}</th><th aria-label={t("requests:columns.actions")} />
    </tr></thead><tbody>{records.map((record) => <tr key={record.id}>
      <td><strong>{record.method} {record.path}</strong><span className="subtle">{record.id}</span></td>
      <td><span className={`status ${record.state}`}>{t(`common:status.${record.state}`, { defaultValue: record.state })}</span></td>
      <td>{record.attempt}</td><td>{formatDuration(record.startedAt, record.completedAt)}</td>
      <td><span className="subtle history-error">{record.lastError || t("common:notAvailable")}</span></td>
      <td><div className="row-actions"><button className="icon-button" aria-label={t("common:actions.openTimeline")} data-tooltip={t("common:actions.openTimeline")} onClick={() => onOpen(record)}><ListTree size={17} /></button></div></td>
    </tr>)}</tbody></table></div>}
  </section>;
}
