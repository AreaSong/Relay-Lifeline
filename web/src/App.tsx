import { FormEvent, ReactNode, useCallback, useEffect, useMemo, useState } from "react";
import {
  Activity, Archive, CirclePause, CirclePlay, Clock3, FileLock2, HeartPulse, LogOut,
  RefreshCw, RotateCcw, ScrollText, Settings2, ShieldAlert, ShieldCheck, Stethoscope,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { ApiClient, errorMessage } from "./api";
import { AlertsList } from "./components/AlertsList";
import { LanguageSelector } from "./components/LanguageSelector";
import { RequestsTable } from "./components/RequestsTable";
import { TimelinePanel } from "./components/TimelinePanel";
import { normalizeLocale } from "./i18n";
import type { Alert, Config, DiagnosticReport, HistoryRecord, Status } from "./types";
import { DiagnosticsView } from "./views/DiagnosticsView";
import { HistoryView } from "./views/HistoryView";
import { SettingsView } from "./views/SettingsView";
import { CapturesView } from "./views/CapturesView";
import { LogsView } from "./views/LogsView";

type View = "overview" | "requests" | "history" | "logs" | "captures" | "diagnostics" | "settings";

function Login({ onLogin }: { onLogin: (token: string) => Promise<void> }) {
  const { t } = useTranslation(["auth", "common"]);
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try { await onLogin(token); } catch (reason) { setError(errorMessage(reason, "generic")); }
    finally { setBusy(false); }
  }
  return <main className="login-shell"><div className="login-language"><LanguageSelector /></div><form className="login-panel" onSubmit={submit}>
    <div className="brand-mark"><HeartPulse size={24} /></div><h1>Relay-Lifeline</h1><p>{t("common:brandSubtitle")}</p>
    <label className="field"><span>{t("auth:adminKey")}</span><input type="password" value={token} onChange={(event) => setToken(event.target.value)} autoFocus required /></label>
    {error && <div className="error-banner">{error}</div>}
    <button className="button primary" disabled={busy || !token}><ShieldCheck size={17} />{busy ? t("auth:verifying") : t("auth:enter")}</button>
  </form></main>;
}

function Stat({ icon, label, value }: { icon: ReactNode; label: string; value: number | string }) {
  return <div className="stat"><span className="stat-icon">{icon}</span><div><span>{label}</span><strong>{value}</strong></div></div>;
}

export function App() {
  const { t, i18n } = useTranslation(["common", "overview", "settings", "diagnostics", "requests", "errors"]);
  const locale = normalizeLocale(i18n.resolvedLanguage);
  const [token, setToken] = useState(() => sessionStorage.getItem("relay-lifeline-token") || "");
  const [view, setView] = useState<View>("overview");
  const [status, setStatus] = useState<Status | null>(null);
  const [config, setConfig] = useState<Config | null>(null);
  const [savedConfig, setSavedConfig] = useState<Config | null>(null);
  const [alerts, setAlerts] = useState<Alert[]>([]);
  const [history, setHistory] = useState<HistoryRecord[]>([]);
  const [timeline, setTimeline] = useState<HistoryRecord | null>(null);
  const [diagnostics, setDiagnostics] = useState<DiagnosticReport | null>(null);
  const [diagnosticBusy, setDiagnosticBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [messageKind, setMessageKind] = useState<"success" | "error">("success");
  const api = useMemo(() => new ApiClient(token, locale), [token, locale]);
  const dirty = useMemo(() => !!config && !!savedConfig && JSON.stringify(config) !== JSON.stringify(savedConfig), [config, savedConfig]);
  const refresh = useCallback(async () => {
    const [nextStatus, nextAlerts] = await Promise.all([api.status(), api.alerts()]);
    setStatus(nextStatus);
    setAlerts(nextAlerts);
  }, [api]);

  async function login(value: string) {
    await new ApiClient(value, locale).session();
    sessionStorage.setItem("relay-lifeline-token", value);
    setToken(value);
  }
  function logout() {
    sessionStorage.removeItem("relay-lifeline-token");
    setToken(""); setStatus(null); setConfig(null); setSavedConfig(null); setTimeline(null);
  }
  useEffect(() => {
    document.title = `${t(`common:title.${view}`)} · Relay-Lifeline`;
  }, [t, view, locale]);
  useEffect(() => {
    if (!token) return;
    Promise.all([
      refresh(),
      api.config().then((value) => { setConfig(value); setSavedConfig(value); }),
      api.history().then(setHistory),
    ]).catch((reason) => showMessage(errorMessage(reason), "error"));
    setTimeline(null);
    setDiagnostics(null);
    const timer = window.setInterval(() => refresh().catch(() => undefined), 2000);
    return () => clearInterval(timer);
  }, [token, api, refresh]);
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty) event.preventDefault(); };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const showMessage = useCallback((value: string, kind: "success" | "error" = "success") => {
	setMessage(value); setMessageKind(kind); window.setTimeout(() => setMessage(""), 4000);
  }, []);
  async function togglePause() {
    try { status?.paused ? await api.resume() : await api.pause(); await refresh(); }
    catch (reason) { showMessage(errorMessage(reason), "error"); }
  }
  async function save() {
    if (!config) return;
    try {
      const result = await api.saveConfig(config);
      setSavedConfig(config);
      showMessage(result.restartRequired ? t("settings:savedRestart") : t("settings:saved"));
    } catch (reason) { showMessage(errorMessage(reason, "save"), "error"); }
  }
  async function reload() {
    try {
      await api.reloadConfig();
      const value = await api.config();
      setConfig(value); setSavedConfig(value); showMessage(t("settings:reloaded"));
    } catch (reason) { showMessage(errorMessage(reason, "reload"), "error"); }
  }
  async function selectView(next: View) {
    if (view === "settings" && next !== "settings" && dirty && !window.confirm(t("settings:leaveConfirm"))) return;
    setView(next);
    if (next === "history") api.history().then(setHistory).catch((reason) => showMessage(errorMessage(reason), "error"));
  }
  async function openTimeline(id: string) {
    try { setTimeline(await api.timeline(id)); }
    catch (reason) { showMessage(errorMessage(reason), "error"); }
  }
  async function runDiagnostics() {
    setDiagnosticBusy(true);
    try { setDiagnostics(await api.runDiagnostics()); await refresh(); }
    catch (reason) { showMessage(errorMessage(reason), "error"); }
    finally { setDiagnosticBusy(false); }
  }
  async function downloadDiagnostics() {
    try { await api.downloadDiagnostics(); showMessage(t("diagnostics:exported")); }
    catch (reason) { showMessage(errorMessage(reason), "error"); }
  }

  if (!token) return <Login onLogin={login} />;
  if (!status || !config || !savedConfig) return <div className="loading"><HeartPulse size={26} />{t("common:loading")}</div>;

	const navigation: Array<{ view: View; icon: ReactNode }> = [
	  { view: "overview", icon: <Activity size={18} /> }, { view: "requests", icon: <Clock3 size={18} /> },
	  { view: "history", icon: <Archive size={18} /> }, { view: "logs", icon: <ScrollText size={18} /> },
	  { view: "captures", icon: <FileLock2 size={18} /> }, { view: "diagnostics", icon: <Stethoscope size={18} /> },
    { view: "settings", icon: <Settings2 size={18} /> },
  ];
  const upstreamLabel = status.upstream.state === "healthy" ? "upstreamHealthy" : status.upstream.state === "degraded" ? "upstreamDegraded" : "upstreamUnknown";
  return <div className="app-shell">
    <aside><div className="brand"><span className="brand-mark"><HeartPulse size={21} /></span><div><strong>Relay-Lifeline</strong><span>{t("common:brandSubtitle")}</span></div></div>
      <nav>{navigation.map((item) => <button key={item.view} aria-label={t(`common:nav.${item.view}`)} data-tooltip={t(`common:nav.${item.view}`)} className={view === item.view ? "active" : ""} onClick={() => selectView(item.view)}>{item.icon}{t(`common:nav.${item.view}`)}</button>)}</nav>
      <div className="aside-footer"><LanguageSelector compact /><button className="logout" onClick={logout}><LogOut size={17} />{t("common:actions.logout")}</button></div>
    </aside>
    <main className="workspace"><header><div><h1>{t(`common:title.${view}`)}</h1><div className="health-row"><span className="connection"><i />{t("common:status.gatewayOnline")}</span><span className={`connection upstream-${status.upstream.state}`}><i />{t(`common:status.${upstreamLabel}`)}</span></div></div>
      <div className="header-actions"><button className="icon-button" aria-label={t("common:actions.refresh")} data-tooltip={t("common:actions.refresh")} onClick={refresh}><RefreshCw size={17} /></button><button className={`button ${status.paused ? "primary" : ""}`} onClick={togglePause}>{status.paused ? <CirclePlay size={17} /> : <CirclePause size={17} />}{status.paused ? t("common:actions.resume") : t("common:actions.pause")}</button></div></header>
      {message && <div className={messageKind === "success" ? "success-banner" : "error-banner page-banner"}>{message}</div>}
      {view === "overview" && <><div className="stats"><Stat icon={<Activity />} label={t("overview:stats.active")} value={status.active} /><Stat icon={<Clock3 />} label={t("overview:stats.recovering")} value={status.waiting + status.queued} /><Stat icon={<ShieldCheck />} label={t("overview:stats.successful")} value={status.successful} /><Stat icon={<RotateCcw />} label={t("overview:stats.failedAttempts")} value={status.failedAttempts} /></div>
        <section className="content-section"><div className="section-heading"><div><h2>{t("overview:alerts.title")}</h2><p>{t("overview:alerts.description")}</p></div><ShieldAlert size={18} /></div><AlertsList alerts={alerts} /></section>
        <section className="content-section spaced"><div className="section-heading"><div><h2>{t("overview:recent.title")}</h2><p>{t("overview:recent.description")}</p></div><span className={`mode ${status.paused ? "paused" : ""}`}>{status.paused ? t("common:status.paused") : t("common:status.running")}</span></div><RequestsTable requests={status.requests.slice(0, 6)} api={api} refresh={refresh} onOpen={openTimeline} onError={(value) => showMessage(value, "error")} /></section></>}
      {view === "requests" && <section className="content-section"><div className="section-heading"><div><h2>{t("overview:queue.title")}</h2><p>{t("overview:queue.description")}</p></div><span>{t("common:requestCount", { count: status.requests.length })}</span></div><RequestsTable requests={status.requests} api={api} refresh={refresh} onOpen={openTimeline} onError={(value) => showMessage(value, "error")} /></section>}
	  {view === "history" && <HistoryView records={history} onOpen={setTimeline} />}
	  {view === "logs" && <LogsView api={api} onError={(value) => showMessage(value, "error")} />}
	  {view === "captures" && <CapturesView api={api} config={config} onError={(value) => showMessage(value, "error")} onSuccess={showMessage} />}
      {view === "diagnostics" && <DiagnosticsView report={diagnostics} busy={diagnosticBusy} run={runDiagnostics} download={downloadDiagnostics} />}
      {view === "settings" && <SettingsView config={config} setConfig={setConfig} save={save} reload={reload} dirty={dirty} discard={() => setConfig(savedConfig)} />}
    </main>
    {timeline && <TimelinePanel record={timeline} onClose={() => setTimeline(null)} />}
  </div>;
}
