import { Archive, Clock3, FileLock2, Search, Server, ShieldAlert, ScrollText, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import type { ApiClient } from "../api";
import type { Alert, CaptureRecord, HistoryRecord, Incident, RequestInfo, RuntimeLogEntry } from "../types";
import type { View } from "./AppNavigation";

type SearchResult = {
  key: string;
  kind: "request" | "history" | "incident" | "alert" | "log" | "capture" | "upstream";
  id: string;
  title: string;
  detail: string;
};

export interface SearchTarget {
  kind: SearchResult["kind"];
  id: string;
  detail?: string;
}

function includesQuery(values: Array<string | undefined>, query: string) {
  return values.some((value) => value?.toLocaleLowerCase().includes(query));
}

export function GlobalSearch({ api, upstream, canOperate, requests, history, incidents, alerts, onOpen, onNavigate }: {
  api: ApiClient;
  upstream: string;
  canOperate: boolean;
  requests: RequestInfo[];
  history: HistoryRecord[];
  incidents: Incident[];
  alerts: Alert[];
  onOpen: (id: string) => void;
  onNavigate: (view: View, target?: SearchTarget) => void;
}) {
  const { t } = useTranslation("common");
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const [logs, setLogs] = useState<RuntimeLogEntry[]>([]);
  const [captures, setCaptures] = useState<CaptureRecord[]>([]);
  const input = useRef<HTMLInputElement>(null);
  const root = useRef<HTMLDivElement>(null);
  const normalized = query.trim().toLocaleLowerCase();

  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLocaleLowerCase() === "k") {
        event.preventDefault();
        if (window.matchMedia("(max-width: 820px)").matches) setMobileOpen(true);
        input.current?.focus(); setOpen(true);
      } else if (event.key === "Escape" && (open || mobileOpen)) {
        setOpen(false); setMobileOpen(false);
        if (mobileOpen) document.getElementById("mobile-tools-trigger")?.focus();
      }
    };
    const openMobile = () => { setMobileOpen(true); setOpen(true); window.requestAnimationFrame(() => input.current?.focus()); };
    const pointerdown = (event: MouseEvent) => {
      if (root.current && !root.current.contains(event.target as Node)) { setOpen(false); setMobileOpen(false); }
    };
    window.addEventListener("keydown", keydown);
    window.addEventListener("mousedown", pointerdown);
    window.addEventListener("relay:open-search", openMobile);
    return () => { window.removeEventListener("keydown", keydown); window.removeEventListener("mousedown", pointerdown); window.removeEventListener("relay:open-search", openMobile); };
  }, [mobileOpen, open]);

  useEffect(() => {
    if (!open || normalized.length < 2) return;
    let disposed = false;
    const timer = window.setTimeout(() => {
      void Promise.allSettled([api.runtimeLogs({ limit: 200, tail: true }), api.captures()]).then(([logResult, captureResult]) => {
        if (disposed) return;
        if (logResult.status === "fulfilled") setLogs(logResult.value.entries);
        if (captureResult.status === "fulfilled") setCaptures(captureResult.value);
      });
    }, 220);
    return () => { disposed = true; window.clearTimeout(timer); };
  }, [api, normalized, open]);

  const results = useMemo<SearchResult[]>(() => {
    if (!normalized) return [];
    const items: SearchResult[] = [];
    requests.forEach((request) => {
      if (includesQuery([request.id, request.method, request.path, request.state, request.lastErrorCode], normalized)) {
        items.push({ key: `request-${request.id}`, kind: "request", id: request.id, title: `${request.method} ${request.path}`, detail: request.id });
      }
    });
    history.forEach((record) => {
      if (includesQuery([record.id, record.method, record.path, record.state, record.lastErrorCode], normalized)) {
        items.push({ key: `history-${record.id}`, kind: "history", id: record.id, title: `${record.method} ${record.path}`, detail: record.id });
      }
    });
    incidents.forEach((incident) => {
      if (includesQuery([incident.id, incident.state, ...incident.affectedRequests, ...Object.keys(incident.categories)], normalized)) {
        items.push({ key: `incident-${incident.id}`, kind: "incident", id: incident.id, title: t("search.incidentResult"), detail: incident.id });
      }
    });
    alerts.forEach((alert) => {
      if (includesQuery([alert.id, alert.requestId, alert.type, alert.message], normalized)) {
        items.push({ key: `alert-${alert.id}`, kind: "alert", id: alert.requestId || alert.id, title: alert.message, detail: alert.requestId || alert.id });
      }
    });
    logs.forEach((entry) => {
      if (includesQuery([String(entry.id), entry.event, entry.message, entry.requestId, entry.level, JSON.stringify(entry.fields || {})], normalized)) {
        items.push({ key: `log-${entry.id}`, kind: "log", id: entry.requestId || "", title: entry.message, detail: entry.event });
      }
    });
    captures.forEach((record) => {
      if (includesQuery([record.id, record.requestId, record.method, record.path, record.state], normalized)) {
        items.push({ key: `capture-${record.id}`, kind: "capture", id: record.id, title: `${record.method} ${record.path}`, detail: record.requestId });
      }
    });
    if (includesQuery([upstream], normalized)) items.push({ key: "upstream", kind: "upstream", id: upstream, title: t("search.upstreamResult"), detail: upstream });
    return items.slice(0, 8);
  }, [alerts, captures, history, incidents, logs, normalized, requests, t, upstream]);

  useEffect(() => { setActiveIndex(results.length ? 0 : -1); }, [normalized, results.length]);

  function select(result: SearchResult) {
    setOpen(false); setMobileOpen(false); setQuery("");
    const target: SearchTarget = { kind: result.kind, id: result.id, detail: result.detail };
    if (result.kind === "incident") onNavigate("incidents", target);
    else if (result.kind === "log") onNavigate("logs", target);
    else if (result.kind === "capture") onNavigate("captures", target);
    else if (result.kind === "upstream") onNavigate(canOperate ? "settings" : "diagnostics", target);
    else if (result.kind === "alert" && !alerts.find((alert) => alert.id === result.key.replace("alert-", ""))?.requestId) onNavigate("incidents", target);
    else onOpen(result.id);
  }

  function handleInputKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    if (!results.length) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const direction = event.key === "ArrowDown" ? 1 : -1;
      setActiveIndex((current) => (current + direction + results.length) % results.length);
    } else if (event.key === "Enter" && activeIndex >= 0) {
      event.preventDefault(); select(results[activeIndex]);
    }
  }

  return <div className={`global-search${mobileOpen ? " mobile-open" : ""}`} ref={root} role="search">
    <Search size={17} aria-hidden="true" />
    <input
      ref={input}
      type="search"
      value={query}
      aria-expanded={open && !!normalized}
      aria-controls="global-search-results"
      aria-activedescendant={activeIndex >= 0 ? `global-search-option-${activeIndex}` : undefined}
      role="combobox"
      aria-autocomplete="list"
      aria-label={t("search.label")}
      placeholder={t("search.placeholder")}
      onFocus={() => setOpen(true)}
      onChange={(event) => { setQuery(event.target.value); setOpen(true); }}
      onKeyDown={handleInputKeyDown}
    />
    {query ? <button className="search-clear" aria-label={t("search.clear")} onClick={() => { setQuery(""); input.current?.focus(); }}><X size={14} /></button> : <kbd>⌘K</kbd>}
    {open && normalized && <div className="search-results" id="global-search-results" role="listbox" aria-label={t("search.results")}>
      <header><span>{t("search.results")}</span><b>{results.length}</b></header>
      {results.map((result, index) => {
        const Icon = result.kind === "request" ? Clock3 : result.kind === "history" ? Archive : result.kind === "log" ? ScrollText : result.kind === "capture" ? FileLock2 : result.kind === "upstream" ? Server : ShieldAlert;
        return <button id={`global-search-option-${index}`} role="option" aria-selected={activeIndex === index} key={result.key} onMouseEnter={() => setActiveIndex(index)} onClick={() => select(result)}>
          <span><Icon size={15} /></span><div><strong>{result.title}</strong><small>{t(`search.kind.${result.kind}`)} · {result.detail}</small></div>
        </button>;
      })}
      {!results.length && <p>{t("search.empty")}</p>}
    </div>}
  </div>;
}
