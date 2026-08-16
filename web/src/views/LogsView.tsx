import { Download, Pause, Play, RefreshCw, ScrollText } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ApiClient } from "../api";
import { formatTime } from "../format";
import type { RuntimeLogEntry } from "../types";

interface Props {
  api: ApiClient;
  onError: (message: string) => void;
  initialRequestId?: string;
  initialEvent?: string;
}

export function LogsView({ api, onError, initialRequestId = "", initialEvent = "" }: Props) {
  const { t } = useTranslation(["logs", "common", "requests"]);
  const [entries, setEntries] = useState<RuntimeLogEntry[]>([]);
  const [paused, setPaused] = useState(false);
  const [level, setLevel] = useState("");
  const [event, setEvent] = useState(initialEvent);
  const [requestId, setRequestId] = useState(initialRequestId);
  const [truncated, setTruncated] = useState(false);
  const [newCount, setNewCount] = useState(0);
  const displayed = useRef<RuntimeLogEntry[]>([]);
  const pending = useRef<RuntimeLogEntry[]>([]);
  const pausedRef = useRef(false);
  const log = useRef<HTMLDivElement>(null);
  const load = useCallback(async () => {
    try {
      const page = await api.runtimeLogs({ limit: 500, tail: true, level, event, requestId });
      pending.current = page.entries;
      if (pausedRef.current) {
        const visibleIds = new Set(displayed.current.map((entry) => entry.id));
        setNewCount(page.entries.filter((entry) => !visibleIds.has(entry.id)).length);
      } else {
        displayed.current = page.entries;
        setEntries(page.entries);
        setNewCount(0);
      }
      setTruncated(page.hasGap || page.hasMore);
    }
    catch (reason) { onError(reason instanceof Error ? reason.message : t("common:httpError", { status: "?" })); }
  }, [api, event, level, onError, requestId, t]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(load, 2000);
    return () => window.clearInterval(timer);
  }, [load]);
  useEffect(() => { pausedRef.current = paused; }, [paused]);
  useEffect(() => { setEvent(initialEvent); setRequestId(initialRequestId); }, [initialEvent, initialRequestId]);
  useEffect(() => { if (!paused && log.current) log.current.scrollTop = log.current.scrollHeight; }, [entries, paused]);

  function togglePaused() {
    if (paused) {
      displayed.current = pending.current;
      setEntries(pending.current);
      setNewCount(0);
    }
    pausedRef.current = !paused;
    setPaused((value) => !value);
  }

  return <section className="content-section">
    <div className="section-heading logs-heading"><div><h2>{t("logs:title")}</h2><p>{t("logs:summary", { count: entries.length })}</p></div><div className="header-actions">
      <button className="icon-button" data-tooltip={t("common:actions.refresh")} aria-label={t("common:actions.refresh")} onClick={load}><RefreshCw size={17} /></button>
      <button className="button" onClick={togglePaused}>{paused ? <Play size={17} /> : <Pause size={17} />}{paused ? t("logs:resumeWithCount", { count: newCount }) : t("logs:pause")}</button>
      <button className="button" onClick={() => api.downloadRuntimeLogs().catch((reason) => onError(String(reason)))}><Download size={17} />{t("logs:download")}</button>
    </div></div>
    <div className="log-filters">
      <label className="field"><span>{t("logs:level")}</span><select value={level} onChange={(e) => setLevel(e.target.value)}><option value="">{t("logs:all")}</option><option value="info">{t("logs:levels.info")}</option><option value="warn">{t("logs:levels.warn")}</option><option value="error">{t("logs:levels.error")}</option></select></label>
      <label className="field"><span>{t("logs:event")}</span><input value={event} onChange={(e) => setEvent(e.target.value)} placeholder={t("logs:eventPlaceholder")} /></label>
      <label className="field"><span>{t("logs:requestId")}</span><input value={requestId} onChange={(e) => setRequestId(e.target.value)} /></label>
    </div>
    {truncated && <div className="warning-banner page-banner" role="status">{t("logs:truncated", { count: entries.length })}</div>}
    {paused && newCount > 0 && <div className="log-new-count" role="status">{t("logs:newCount", { count: newCount })}</div>}
    {entries.length === 0 ? <div className="empty-state"><ScrollText />{t("logs:empty")}</div> : <div className="runtime-log" ref={log} aria-live={paused ? "off" : "polite"}>
      {entries.map((entry) => <article className={`log-entry ${entry.level}`} key={entry.id}>
        <time>{formatTime(entry.time)}</time><span className="log-level">{t(`logs:levels.${entry.level}`, { defaultValue: entry.level })}</span><div><strong>{entry.event}</strong><p>{entry.message}</p>{entry.requestId && <code>{entry.requestId}{entry.attempt ? ` · #${entry.attempt}` : ""}{entry.statusCode ? ` · HTTP ${entry.statusCode}` : ""}</code>}{(entry.taskId || entry.clientId) && <code>{entry.taskId && `${t("requests:identity.task")}: ${entry.taskId}`}{entry.taskId && entry.clientId ? " · " : ""}{entry.clientId && `${t("requests:identity.client")}: ${entry.clientId}`}</code>}</div>
        {entry.fields && <pre>{JSON.stringify(entry.fields, null, 2)}</pre>}
      </article>)}
    </div>}
  </section>;
}
