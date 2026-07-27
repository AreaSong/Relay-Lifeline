import { Download, Pause, Play, RefreshCw, ScrollText } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ApiClient } from "../api";
import { formatTime } from "../format";
import type { RuntimeLogEntry } from "../types";

interface Props {
  api: ApiClient;
  onError: (message: string) => void;
}

export function LogsView({ api, onError }: Props) {
  const { t } = useTranslation(["logs", "common"]);
  const [entries, setEntries] = useState<RuntimeLogEntry[]>([]);
  const [paused, setPaused] = useState(false);
  const [level, setLevel] = useState("");
  const [event, setEvent] = useState("");
  const [requestId, setRequestId] = useState("");
  const load = useCallback(async () => {
    try { setEntries(await api.runtimeLogs({ level, event, requestId })); }
    catch (reason) { onError(reason instanceof Error ? reason.message : t("common:httpError", { status: "?" })); }
  }, [api, event, level, onError, requestId, t]);

  useEffect(() => {
    void load();
    if (paused) return;
    const timer = window.setInterval(load, 2000);
    return () => window.clearInterval(timer);
  }, [load, paused]);

  return <section className="content-section">
    <div className="section-heading logs-heading"><div><h2>{t("logs:title")}</h2><p>{t("logs:summary", { count: entries.length })}</p></div><div className="header-actions">
      <button className="icon-button" data-tooltip={t("common:actions.refresh")} aria-label={t("common:actions.refresh")} onClick={load}><RefreshCw size={17} /></button>
      <button className="button" onClick={() => setPaused((value) => !value)}>{paused ? <Play size={17} /> : <Pause size={17} />}{paused ? t("logs:resume") : t("logs:pause")}</button>
      <button className="button" onClick={() => api.downloadRuntimeLogs().catch((reason) => onError(String(reason)))}><Download size={17} />{t("logs:download")}</button>
    </div></div>
    <div className="log-filters">
      <label className="field"><span>{t("logs:level")}</span><select value={level} onChange={(e) => setLevel(e.target.value)}><option value="">{t("logs:all")}</option><option value="info">{t("logs:levels.info")}</option><option value="warn">{t("logs:levels.warn")}</option><option value="error">{t("logs:levels.error")}</option></select></label>
      <label className="field"><span>{t("logs:event")}</span><input value={event} onChange={(e) => setEvent(e.target.value)} placeholder={t("logs:eventPlaceholder")} /></label>
      <label className="field"><span>{t("logs:requestId")}</span><input value={requestId} onChange={(e) => setRequestId(e.target.value)} /></label>
    </div>
    {entries.length === 0 ? <div className="empty-state"><ScrollText />{t("logs:empty")}</div> : <div className="runtime-log" aria-live="polite">
      {entries.map((entry) => <article className={`log-entry ${entry.level}`} key={entry.id}>
        <time>{formatTime(entry.time)}</time><span className="log-level">{t(`logs:levels.${entry.level}`, { defaultValue: entry.level })}</span><div><strong>{entry.event}</strong><p>{entry.message}</p>{entry.requestId && <code>{entry.requestId}{entry.attempt ? ` · #${entry.attempt}` : ""}{entry.statusCode ? ` · HTTP ${entry.statusCode}` : ""}</code>}</div>
        {entry.fields && <pre>{JSON.stringify(entry.fields, null, 2)}</pre>}
      </article>)}
    </div>}
  </section>;
}
