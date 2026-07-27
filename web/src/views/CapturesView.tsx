import { Download, Eye, FileLock2, Play, RefreshCw, Square, Trash2, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ApiClient } from "../api";
import { formatBytes } from "../format";
import type { CapturePreview, CaptureRecord, CaptureStatus, Config } from "../types";

interface Props {
  api: ApiClient;
  config: Config;
  onError: (message: string) => void;
  onSuccess: (message: string) => void;
}

export function CapturesView({ api, config, onError, onSuccess }: Props) {
  const { t, i18n } = useTranslation(["captures", "common"]);
  const [status, setStatus] = useState<CaptureStatus | null>(null);
  const [records, setRecords] = useState<CaptureRecord[]>([]);
  const [preview, setPreview] = useState<CapturePreview | null>(null);
  const [requestLimit, setRequestLimit] = useState(config.capture.defaultRequestLimit);
  const [timeoutMinutes, setTimeoutMinutes] = useState(Math.max(1, Number.parseInt(config.capture.activationTimeout) || 10));
  const [busy, setBusy] = useState(false);
  const drawer = useRef<HTMLElement>(null);
  const load = useCallback(async () => {
    try {
      const [nextStatus, nextRecords] = await Promise.all([api.captureStatus(), api.captures()]);
      setStatus(nextStatus); setRecords(nextRecords);
    } catch (reason) { onError(reason instanceof Error ? reason.message : String(reason)); }
  }, [api, onError]);

  useEffect(() => { void load(); const timer = window.setInterval(load, 3000); return () => window.clearInterval(timer); }, [load]);
  useEffect(() => {
    if (!preview) return;
    const close = (event: KeyboardEvent) => { if (event.key === "Escape") setPreview(null); };
    window.addEventListener("keydown", close);
    drawer.current?.querySelector<HTMLElement>("button")?.focus();
    return () => window.removeEventListener("keydown", close);
  }, [preview]);

  async function start() {
    setBusy(true);
    try { setStatus(await api.startCapture(requestLimit, `${timeoutMinutes}m`)); onSuccess(t("captures:started")); }
    catch (reason) { onError(reason instanceof Error ? reason.message : String(reason)); }
    finally { setBusy(false); }
  }
  async function stop() {
    setBusy(true);
    try { setStatus(await api.stopCapture()); onSuccess(t("captures:stopped")); }
    catch (reason) { onError(reason instanceof Error ? reason.message : String(reason)); }
    finally { setBusy(false); }
  }
  async function download(record: CaptureRecord, mode: "filtered" | "raw") {
    if (mode === "raw" && !window.confirm(t("captures:rawConfirm"))) return;
    try { await api.downloadCapture(record.id, mode); onSuccess(t("captures:downloaded")); }
    catch (reason) { onError(reason instanceof Error ? reason.message : String(reason)); }
  }
  async function remove(record: CaptureRecord) {
    if (!window.confirm(t("captures:deleteConfirm"))) return;
    try { await api.deleteCapture(record.id); if (preview?.record.id === record.id) setPreview(null); await load(); }
    catch (reason) { onError(reason instanceof Error ? reason.message : String(reason)); }
  }

  const usage = status && status.maxTotalBytes > 0 ? Math.min(100, status.storageBytes / status.maxTotalBytes * 100) : 0;
  return <div className="capture-shell">
    <section className="content-section"><div className="section-heading captures-heading"><div><h2>{t("captures:controlTitle")}</h2><p>{status?.active ? t("captures:activeSummary", { count: status.remainingRequests }) : t("captures:idleSummary")}</p></div><div className="header-actions">
      <button className="icon-button" data-tooltip={t("common:actions.refresh")} aria-label={t("common:actions.refresh")} onClick={load}><RefreshCw size={17} /></button>
      {status?.active ? <button className="button" disabled={busy} onClick={stop}><Square size={17} />{t("captures:stop")}</button> : <button className="button primary" disabled={busy || status?.available === false} onClick={start}><Play size={17} />{t("captures:start")}</button>}
    </div></div>
      {status?.available === false && <div className="error-banner page-banner">{status.unavailableReason || t("captures:unavailable")}</div>}
      <div className="capture-controls">
        <label className="field"><span>{t("captures:requestLimit")}</span><input type="number" min="1" max="100" value={requestLimit} onChange={(e) => setRequestLimit(Number(e.target.value))} /></label>
        <label className="field"><span>{t("captures:timeout")}</span><div className="compound-input"><input type="number" min="1" max="60" value={timeoutMinutes} onChange={(e) => setTimeoutMinutes(Number(e.target.value))} /><span className="unit-label">{t("captures:minutes")}</span></div></label>
        <div className="capture-usage"><span>{t("captures:storage")}</span><strong>{formatBytes(status?.storageBytes || 0)} / {formatBytes(status?.maxTotalBytes || 0)}</strong><progress max="100" value={usage} aria-label={t("captures:storage")} /></div>
      </div>
    </section>
    <section className="content-section spaced"><div className="section-heading"><div><h2>{t("captures:recordsTitle")}</h2><p>{t("captures:recordsSummary", { count: records.length })}</p></div></div>
      {records.length === 0 ? <div className="empty-state"><FileLock2 />{t("captures:empty")}</div> : <div className="table-wrap capture-table responsive-table"><table><thead><tr><th>{t("captures:request")}</th><th>{t("captures:state")}</th><th>{t("captures:attempts")}</th><th>{t("captures:size")}</th><th>{t("captures:expires")}</th><th /></tr></thead><tbody>
        {records.map((record) => <tr key={record.id}><td data-label={t("captures:request")}><strong>{record.method} {record.path}</strong><code className="subtle">{record.requestId}</code></td><td data-label={t("captures:state")}><span className={`status ${record.state}`}>{t(`common:status.${record.state}`, { defaultValue: record.state })}</span></td><td data-label={t("captures:attempts")}>{record.attempts?.length ?? 0}</td><td data-label={t("captures:size")}>{formatBytes(record.capturedBytes)}</td><td data-label={t("captures:expires")}>{new Date(record.expiresAt).toLocaleString(i18n.resolvedLanguage)}</td><td data-label={t("captures:actions")}><div className="row-actions">
          <button className="icon-button" data-tooltip={t("captures:preview")} aria-label={t("captures:preview")} onClick={() => api.capturePreview(record.id).then(setPreview).catch((reason) => onError(String(reason)))}><Eye size={16} /></button>
          <button className="icon-button" data-tooltip={t("captures:filteredDownload")} aria-label={t("captures:filteredDownload")} onClick={() => download(record, "filtered")}><Download size={16} /></button>
          <button className="icon-button sensitive" data-tooltip={t("captures:rawDownload")} aria-label={t("captures:rawDownload")} onClick={() => download(record, "raw")}><FileLock2 size={16} /></button>
          <button className="icon-button danger" data-tooltip={t("captures:delete")} aria-label={t("captures:delete")} onClick={() => remove(record)}><Trash2 size={16} /></button>
        </div></td></tr>)}
      </tbody></table></div>}
    </section>
    {preview && <div className="drawer-backdrop" onMouseDown={() => setPreview(null)}><aside ref={drawer} className="capture-drawer" role="dialog" aria-modal="true" aria-label={t("captures:preview")} onMouseDown={(e) => e.stopPropagation()}><div className="drawer-header"><div><span className={`status ${preview.record.state}`}>{t(`common:status.${preview.record.state}`, { defaultValue: preview.record.state })}</span><h2>{preview.record.method} {preview.record.path}</h2><p>{preview.record.requestId}</p></div><button className="icon-button" aria-label={t("common:actions.close")} onClick={() => setPreview(null)}><X size={18} /></button></div>
      <div className="capture-parts">{preview.parts.map((part, index) => <section key={`${part.name}-${part.attempt || index}`}><div><strong>{part.name === "attempt" ? t("captures:attemptPart", { count: part.attempt }) : t(`captures:${part.name}Part`)}</strong><span>{formatBytes(part.originalBytes)}{part.truncated ? ` · ${t("captures:truncated")}` : ""}</span></div><details><summary>{t("captures:headers")}</summary><pre>{JSON.stringify(part.headers || {}, null, 2)}</pre></details><pre className="capture-body">{part.body}</pre></section>)}</div>
    </aside></div>}
  </div>;
}
