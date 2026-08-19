import { BookmarkPlus, Download, Pause, Play, RefreshCw, ScrollText, X } from "lucide-react";
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
  pageVisible: boolean;
}

type LogRange = "" | "15m" | "1h" | "6h" | "24h";
interface SavedLogFilter { level: string; event: string; requestId: string; filter: string; range: LogRange }
const ranges: LogRange[] = ["15m", "1h", "6h", "24h", ""];
const rangeMilliseconds: Record<Exclude<LogRange, "">, number> = { "15m": 15 * 60_000, "1h": 60 * 60_000, "6h": 6 * 60 * 60_000, "24h": 24 * 60 * 60_000 };
const savedFiltersKey = "relay-lifeline-log-filters";

function urlFilter(): SavedLogFilter {
  const params = new URLSearchParams(window.location.hash.split("?", 2)[1] || "");
  const range = params.get("range");
  return {
    level: params.get("level") || "", event: params.get("event") || "", requestId: params.get("requestId") || "", filter: params.get("filter") || "",
    range: range === "15m" || range === "1h" || range === "6h" || range === "24h" ? range : "",
  };
}

function storedFilters(): SavedLogFilter[] {
  try {
    const value = JSON.parse(localStorage.getItem(savedFiltersKey) || "[]");
    return Array.isArray(value) ? value.slice(0, 6) : [];
  } catch { return []; }
}

export function LogsView({ api, onError, initialRequestId = "", initialEvent = "", pageVisible }: Props) {
  const { t } = useTranslation(["logs", "common", "requests"]);
  const [entries, setEntries] = useState<RuntimeLogEntry[]>([]);
  const [paused, setPaused] = useState(false);
  const initial = useRef(urlFilter()).current;
  const [level, setLevel] = useState(initial.level);
  const [event, setEvent] = useState(initialEvent || initial.event);
  const [requestId, setRequestId] = useState(initialRequestId || initial.requestId);
  const [filter, setFilter] = useState(initial.filter);
  const [range, setRange] = useState<LogRange>(initial.range);
  const [savedFilters, setSavedFilters] = useState<SavedLogFilter[]>(storedFilters);
  const [truncated, setTruncated] = useState(false);
  const [newCount, setNewCount] = useState(0);
  const displayed = useRef<RuntimeLogEntry[]>([]);
  const pending = useRef<RuntimeLogEntry[]>([]);
  const pausedRef = useRef(false);
  const log = useRef<HTMLDivElement>(null);
  const load = useCallback(async () => {
    try {
      const since = range ? new Date(Date.now() - rangeMilliseconds[range]).toISOString() : undefined;
      const page = await api.runtimeLogs({ limit: 500, tail: true, level, event, requestId, q: filter, since });
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
  }, [api, event, filter, level, onError, range, requestId, t]);

  useEffect(() => {
	if (!pageVisible) return;
    void load();
    const timer = window.setInterval(load, 2000);
    return () => window.clearInterval(timer);
  }, [load, pageVisible]);
  useEffect(() => { pausedRef.current = paused; }, [paused]);
  useEffect(() => { if (initialEvent) setEvent(initialEvent); if (initialRequestId) setRequestId(initialRequestId); }, [initialEvent, initialRequestId]);
  useEffect(() => {
    if (!window.location.hash.replace(/^#\/?/, "").startsWith("logs")) return;
    const params = new URLSearchParams();
    if (level) params.set("level", level); if (event) params.set("event", event); if (requestId) params.set("requestId", requestId); if (filter) params.set("filter", filter); if (range) params.set("range", range);
    const query = params.toString();
    window.history.replaceState(null, "", `#/logs${query ? `?${query}` : ""}`);
  }, [event, filter, level, range, requestId]);
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

  function currentFilter(): SavedLogFilter { return { level, event, requestId, filter, range }; }
  function saveFilter() {
    const current = currentFilter();
    if (!Object.values(current).some(Boolean)) return;
    const key = JSON.stringify(current);
    const next = [current, ...savedFilters.filter((item) => JSON.stringify(item) !== key)].slice(0, 6);
    setSavedFilters(next); localStorage.setItem(savedFiltersKey, JSON.stringify(next));
  }
  function applyFilter(value: SavedLogFilter) { setLevel(value.level); setEvent(value.event); setRequestId(value.requestId); setFilter(value.filter); setRange(value.range); }
  function removeFilter(index: number) { const next = savedFilters.filter((_, itemIndex) => itemIndex !== index); setSavedFilters(next); localStorage.setItem(savedFiltersKey, JSON.stringify(next)); }
  function filterLabel(value: SavedLogFilter) { return value.filter || value.event || value.requestId || value.level || value.range || t("logs:all"); }

  return <section className="content-section">
    <div className="section-heading logs-heading"><div><h2>{t("logs:title")}</h2><p>{t("logs:summary", { count: entries.length })}</p></div><div className="header-actions">
      <button className="icon-button" data-tooltip={t("common:actions.refresh")} aria-label={t("common:actions.refresh")} onClick={load}><RefreshCw size={17} /></button>
      <button className="button" onClick={togglePaused}>{paused ? <Play size={17} /> : <Pause size={17} />}{paused ? t("logs:resumeWithCount", { count: newCount }) : t("logs:pause")}</button>
      <button className="button" onClick={() => api.downloadRuntimeLogs().catch((reason) => onError(String(reason)))}><Download size={17} />{t("logs:download")}</button>
    </div></div>
    <div className="log-filters">
      <label className="field"><span>{t("logs:filter")}</span><input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={t("logs:filterPlaceholder")} /></label>
      <label className="field"><span>{t("logs:level")}</span><select value={level} onChange={(e) => setLevel(e.target.value)}><option value="">{t("logs:all")}</option><option value="info">{t("logs:levels.info")}</option><option value="warn">{t("logs:levels.warn")}</option><option value="error">{t("logs:levels.error")}</option></select></label>
      <label className="field"><span>{t("logs:event")}</span><input value={event} onChange={(e) => setEvent(e.target.value)} placeholder={t("logs:eventPlaceholder")} /></label>
      <label className="field"><span>{t("logs:requestId")}</span><input value={requestId} onChange={(e) => setRequestId(e.target.value)} /></label>
      <button className="icon-button log-save-filter" aria-label={t("logs:saveFilter")} data-tooltip={t("logs:saveFilter")} onClick={saveFilter}><BookmarkPlus size={17} /></button>
    </div>
    <div className="log-query-tools"><div className="segmented-control" role="group" aria-label={t("logs:range")}>{ranges.map((value) => <button key={value || "all"} className={range === value ? "active" : ""} aria-pressed={range === value} onClick={() => setRange(value)}>{value || t("logs:allTime")}</button>)}</div>{savedFilters.length > 0 && <div className="saved-log-filters" aria-label={t("logs:savedFilters")}>{savedFilters.map((value, index) => <span key={`${JSON.stringify(value)}-${index}`}><button onClick={() => applyFilter(value)}>{filterLabel(value)}</button><button className="icon-button" aria-label={t("logs:removeFilter", { name: filterLabel(value) })} onClick={() => removeFilter(index)}><X size={13} /></button></span>)}</div>}</div>
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
