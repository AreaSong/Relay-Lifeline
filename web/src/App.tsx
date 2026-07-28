import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import {
  Activity, Archive, CirclePause, CirclePlay, Clock3, FileLock2, HeartPulse, LogOut,
  Menu, RefreshCw, ScrollText, Settings2, ShieldAlert, ShieldCheck, Stethoscope,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { ApiClient, errorMessage } from "./api";
import { LanguageSelector } from "./components/LanguageSelector";
import { SignalTopology } from "./components/SignalTopology";
import { ThemeSelector } from "./components/ThemeSelector";
import { TimelinePanel } from "./components/TimelinePanel";
import { ViewErrorBoundary } from "./components/ViewErrorBoundary";
import { normalizeLocale } from "./i18n";
import { useTheme } from "./theme";
import type {
  Alert, Config, DiagnosticReport, HistoryRecord, Incident, MetricsErrors, MetricsSnapshot,
  MetricsWindow, MonitoringEvent, RuntimeInfo, SessionInfo, Status,
} from "./types";
import { CapturesView } from "./views/CapturesView";
import { DiagnosticsView } from "./views/DiagnosticsView";
import { HistoryView } from "./views/HistoryView";
import { IncidentsView } from "./views/IncidentsView";
import { LogsView } from "./views/LogsView";
import { OverviewView } from "./views/OverviewView";
import { RequestsView } from "./views/RequestsView";
import { SettingsView } from "./views/SettingsView";

type View = "overview" | "requests" | "history" | "incidents" | "logs" | "captures" | "diagnostics" | "settings";
const allViews: View[] = ["overview", "requests", "history", "incidents", "logs", "captures", "diagnostics", "settings"];
const mobileViews: View[] = ["overview", "requests", "history", "diagnostics", "settings"];
const navigation: Array<{ view: View; icon: LucideIcon }> = [
  { view: "overview", icon: Activity }, { view: "requests", icon: Clock3 }, { view: "history", icon: Archive },
  { view: "incidents", icon: ShieldAlert },
  { view: "logs", icon: ScrollText }, { view: "captures", icon: FileLock2 },
  { view: "diagnostics", icon: Stethoscope }, { view: "settings", icon: Settings2 },
];

function currentView(): View {
  const candidate = window.location.hash.replace(/^#\/?/, "") as View;
  return allViews.includes(candidate) ? candidate : "overview";
}

function Login({ onLogin, themeMode, setThemeMode, sessionExpired }: {
  onLogin: (token: string) => Promise<void>;
  themeMode: ReturnType<typeof useTheme>["mode"];
  setThemeMode: ReturnType<typeof useTheme>["setMode"];
  sessionExpired?: boolean;
}) {
  const { t, i18n } = useTranslation(["auth", "common", "overview"]);
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true); setError("");
    try { await onLogin(token); }
    catch (reason) { setError(errorMessage(reason, "generic")); }
    finally { setBusy(false); }
  }
  const labels = {
    ariaLabel: t("overview:signal.topologyLabel"), codex: t("overview:signal.codex"), relay: t("overview:signal.relay"), cpa: t("overview:signal.cpa"),
    healthy: t("overview:signal.healthy"), degraded: t("overview:signal.degraded"), unknown: t("overview:signal.unknown"), active: t("overview:signal.active"),
    waiting: t("overview:signal.waiting"), nextRetry: t("overview:signal.nextRetry"), retryNow: t("overview:signal.retryNow"), staticFallback: t("overview:signal.staticFallback"),
  };
  return <main className="login-shell">
    <section className="login-visual"><div className="login-wordmark"><span className="rail-brand"><HeartPulse size={19} /></span><span>Transfer Lifeline</span></div>
      <div className="login-topology"><SignalTopology upstreamState="unknown" active={0} waiting={0} labels={labels} locale={i18n.resolvedLanguage} /></div>
      <div className="login-statement"><h1>Transfer Lifeline</h1><strong>{t("auth:statement")}</strong><p>{t("auth:statementDetail")}</p></div>
    </section>
    <section className="login-access"><div className="login-language"><LanguageSelector /><ThemeSelector mode={themeMode} onChange={setThemeMode} compact /></div>
      <form className="login-panel" onSubmit={submit}><h2>{t("auth:title")}</h2><p>{t("auth:description")}</p>
        <input className="sr-only" type="text" name="username" value="relay-lifeline-admin" autoComplete="username" tabIndex={-1} aria-hidden="true" readOnly />
        <label className="field"><span>{t("auth:adminKey")}</span><input name="admin-key" type="password" value={token} onChange={(event) => { setToken(event.target.value); setError(""); }} autoComplete="current-password" autoFocus required /></label>
        {(error || sessionExpired) && <div className="error-banner" role="alert">{error || t("auth:sessionExpired")}</div>}
        <button className="button primary" disabled={busy || !token}><ShieldCheck size={17} />{busy ? t("auth:verifying") : t("auth:enter")}</button>
      </form>
    </section>
  </main>;
}

export function App() {
  const { t, i18n } = useTranslation(["common", "overview", "settings", "diagnostics", "requests", "errors"]);
  const locale = normalizeLocale(i18n.resolvedLanguage);
  const theme = useTheme();
  const [authenticated, setAuthenticated] = useState(true);
  const [authReason, setAuthReason] = useState<"required" | "expired" | null>(null);
  const [session, setSession] = useState<SessionInfo | null>(null);
  const [view, setView] = useState<View>(currentView);
  const [status, setStatus] = useState<Status | null>(null);
  const [config, setConfig] = useState<Config | null>(null);
  const [savedConfig, setSavedConfig] = useState<Config | null>(null);
  const [runtimeInfo, setRuntimeInfo] = useState<RuntimeInfo | null>(null);
  const [alerts, setAlerts] = useState<Alert[]>([]);
  const [history, setHistory] = useState<HistoryRecord[]>([]);
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [timeline, setTimeline] = useState<HistoryRecord | null>(null);
  const [diagnostics, setDiagnostics] = useState<DiagnosticReport | null>(null);
  const [diagnosticBusy, setDiagnosticBusy] = useState(false);
  const [metricsWindow, setMetricsWindow] = useState<MetricsWindow>("1h");
  const [metrics, setMetrics] = useState<MetricsSnapshot | null>(null);
  const [metricErrors, setMetricErrors] = useState<MetricsErrors | null>(null);
  const [events, setEvents] = useState<MonitoringEvent[]>([]);
  const [mobileTools, setMobileTools] = useState(false);
  const [message, setMessage] = useState("");
  const [messageKind, setMessageKind] = useState<"success" | "error">("success");
  const resetAuthentication = useCallback((reason: "required" | "expired" | null) => {
    setAuthenticated(false); setAuthReason(reason); setSession(null); setStatus(null); setConfig(null); setSavedConfig(null); setRuntimeInfo(null);
    setAlerts([]); setHistory([]); setIncidents([]); setTimeline(null); setDiagnostics(null); setMetrics(null); setMetricErrors(null); setEvents([]); setMessage("");
  }, []);
  const api = useMemo(() => new ApiClient(locale, (code) => resetAuthentication(code === "SESSION_EXPIRED" ? "expired" : "required")), [locale, resetAuthentication]);
  const dirty = useMemo(() => !!config && !!savedConfig && JSON.stringify(config) !== JSON.stringify(savedConfig), [config, savedConfig]);
  const canOperate = session?.capabilities.includes("operate") || false;
  const canSensitive = session?.capabilities.includes("sensitive") || false;
  const visibleNavigation = useMemo(() => navigation.filter(({ view: itemView }) => canOperate || itemView !== "settings"), [canOperate]);

  const showMessage = useCallback((value: string, kind: "success" | "error" = "success") => {
    setMessage(value); setMessageKind(kind); window.setTimeout(() => setMessage(""), 4000);
  }, []);
  const refresh = useCallback(async () => {
    const [nextStatus, nextAlerts, nextIncidents] = await Promise.all([api.status(), api.alerts(), api.incidents()]);
    setStatus(nextStatus); setAlerts(nextAlerts); setIncidents(nextIncidents);
  }, [api]);
  const refreshMonitoring = useCallback(async () => {
    const [nextMetrics, nextErrors, nextEvents] = await Promise.all([
      api.metrics(metricsWindow), api.metricErrors(metricsWindow), api.events(0, 200),
    ]);
    setMetrics(nextMetrics); setMetricErrors(nextErrors); setEvents(nextEvents.events);
  }, [api, metricsWindow]);

  useEffect(() => { document.title = `${t(`common:title.${view}`)} · Transfer Lifeline`; }, [locale, t, view]);
  useEffect(() => {
    const changed = () => setView(currentView());
    window.addEventListener("hashchange", changed);
    return () => window.removeEventListener("hashchange", changed);
  }, []);
  useEffect(() => {
    if (!authenticated) return;
    let disposed = false;
    void api.session().then(async (nextSession) => {
      if (disposed) return;
      setSession(nextSession);
      await Promise.all([
        refresh(), api.config().then((value) => { setConfig(value); setSavedConfig(value); }), api.history().then(setHistory), api.runtimeInfo().then(setRuntimeInfo),
      ]);
    }).catch((reason) => { if (!disposed) showMessage(errorMessage(reason), "error"); });
    setTimeline(null); setDiagnostics(null);
    return () => { disposed = true; };
  }, [api, authenticated, refresh, showMessage]);
  useEffect(() => {
    if (!authenticated || !session) return;
    return api.subscribe((snapshot) => {
      setStatus(snapshot.status); setAlerts(snapshot.alerts); setIncidents(snapshot.incidents);
      if (snapshot.metrics) setMetrics(snapshot.metrics);
    }, () => { void refresh().catch(() => undefined); });
  }, [api, authenticated, refresh, session]);
  useEffect(() => {
    if (session && !canOperate && view === "settings") {
      setView("overview"); window.location.hash = "/overview";
    }
  }, [canOperate, session, view]);
  useEffect(() => {
    if (!authenticated || !session) return;
    void refreshMonitoring().catch((reason) => showMessage(errorMessage(reason), "error"));
    const metricsTimer = window.setInterval(() => refreshMonitoring().catch(() => undefined), 10_000);
    return () => window.clearInterval(metricsTimer);
  }, [authenticated, refreshMonitoring, session, showMessage]);
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty) event.preventDefault(); };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);
  useEffect(() => {
    if (!mobileTools) return;
    const close = (event: KeyboardEvent) => { if (event.key === "Escape") setMobileTools(false); };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [mobileTools]);

  async function login(value: string) {
    const nextSession = await api.login(value);
    setAuthReason(null); setSession(nextSession); setAuthenticated(true);
  }
  async function logout() {
    await api.logout().catch(() => undefined);
    resetAuthentication(null);
  }
  async function selectView(next: View) {
    if (next === "settings" && !canOperate) return;
    if (view === "settings" && next !== "settings" && dirty && !window.confirm(t("settings:leaveConfirm"))) return;
    setView(next); setMobileTools(false); setTimeline(null);
    window.location.hash = `/${next}`;
    if (next === "history") await Promise.all([api.history().then(setHistory), refreshMonitoring()]).catch((reason) => showMessage(errorMessage(reason), "error"));
  }
  async function openTimeline(id: string) {
    try { setTimeline(await api.timeline(id)); }
    catch (reason) { showMessage(errorMessage(reason), "error"); }
  }
  async function togglePause() {
    try { status?.paused ? await api.resume() : await api.pause(); await refresh(); await refreshMonitoring(); }
    catch (reason) { showMessage(errorMessage(reason), "error"); }
  }
  async function save() {
    if (!config) return;
    try {
      const plan = await api.validateConfig(config);
      if (plan.restartRequired && !window.confirm(t("settings:restartConfirm", { count: plan.restartSections.length }))) return;
      const result = await api.saveConfig(config);
      setSavedConfig(config);
      showMessage(result.backupPath ? t("settings:savedBackup", { path: result.backupPath }) : result.restartRequired ? t("settings:savedRestart") : t("settings:saved"));
      await refreshMonitoring();
    }
    catch (reason) { showMessage(errorMessage(reason, "save"), "error"); }
  }
  async function reload() {
    try { await api.reloadConfig(); const value = await api.config(); setConfig(value); setSavedConfig(value); showMessage(t("settings:reloaded")); await refreshMonitoring(); }
    catch (reason) { showMessage(errorMessage(reason, "reload"), "error"); }
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

  if (!authenticated) return <Login onLogin={login} themeMode={theme.mode} setThemeMode={theme.setMode} sessionExpired={authReason === "expired"} />;
  if (!status || !config || !savedConfig || !session) return <div className="loading"><span><HeartPulse size={26} />{t("common:loading")}</span></div>;

  const upstreamLabel = status.upstream.state === "healthy" ? "upstreamHealthy" : status.upstream.state === "degraded" ? "upstreamDegraded" : "upstreamUnknown";
  const incident = status.upstream.state === "degraded" && (status.waiting + status.queued > 0 || (status.upstream.lastChecked ? Date.now() - new Date(status.upstream.lastChecked).getTime() >= 10_000 : false));
  return <div className={`app-shell${timeline ? " inspector-open" : ""}${incident ? " incident-mode" : ""}`}>
    <aside className="app-rail" aria-label={t("common:brandSubtitle")}><button className="rail-brand" aria-label="Transfer Lifeline" data-tooltip="Transfer Lifeline" onClick={() => selectView("overview")}><HeartPulse size={21} /></button>
      <nav>{visibleNavigation.map(({ view: itemView, icon: Icon }) => <button key={itemView} aria-label={t(`common:nav.${itemView}`)} aria-current={view === itemView ? "page" : undefined} data-tooltip={t(`common:nav.${itemView}`)} className={view === itemView ? "active" : ""} onClick={() => selectView(itemView)}><Icon size={18} /></button>)}</nav>
      <div className="rail-footer"><ThemeSelector mode={theme.mode} onChange={theme.setMode} compact /><LanguageSelector compact /><button className="rail-action" aria-label={t("common:actions.logout")} data-tooltip={t("common:actions.logout")} onClick={logout}><LogOut size={17} /></button></div>
    </aside>

    <main className="workspace"><header className="workspace-header"><div className="mobile-topbar"><span className="rail-brand"><HeartPulse size={17} /></span><div><strong>Transfer Lifeline</strong><span>{t(`common:nav.${view}`)}</span></div></div>
      <div className="desktop-heading"><h1>{t(`common:title.${view}`)}</h1><div className="health-row"><span className="connection"><i />{t("common:status.gatewayOnline")}</span><span className={`connection upstream-${status.upstream.state}`}><i />{t(`common:status.${upstreamLabel}`)}</span><span className="mode">{t(`common:roles.${session.role}`)}</span></div></div>
      <div className="header-actions"><button className="icon-button mobile-tools-toggle" aria-label={t("common:tools")} data-tooltip={t("common:tools")} onClick={() => setMobileTools((open) => !open)}><Menu size={17} /></button><button className="icon-button" aria-label={t("common:actions.refresh")} data-tooltip={t("common:actions.refresh")} onClick={() => { void refresh(); void refreshMonitoring(); }}><RefreshCw size={17} /></button>{canOperate && <button className={`button ${status.paused ? "primary" : ""}`} aria-label={status.paused ? t("common:actions.resume") : t("common:actions.pause")} data-tooltip={status.paused ? t("common:actions.resume") : t("common:actions.pause")} onClick={togglePause}>{status.paused ? <CirclePlay size={17} /> : <CirclePause size={17} />}<span>{status.paused ? t("common:actions.resume") : t("common:actions.pause")}</span></button>}</div>
    </header>
      {message && <div className={messageKind === "success" ? "success-banner page-banner" : "error-banner page-banner"} role="status">{message}</div>}
      <ViewErrorBoundary key={view} title={t("common:viewError.title")} description={t("common:viewError.description")} reloadLabel={t("common:viewError.reload")}>
        {view === "overview" && <OverviewView status={status} metrics={metrics} errors={metricErrors} alerts={alerts} api={api} refresh={refresh} onOpen={openTimeline} onError={(value) => showMessage(value, "error")} locale={locale} dark={theme.resolved === "dark"} incident={incident} canOperate={canOperate} />}
        {view === "requests" && <RequestsView status={status} metrics={metrics} api={api} refresh={refresh} onOpen={openTimeline} onError={(value) => showMessage(value, "error")} canOperate={canOperate} />}
        {view === "history" && <HistoryView records={history} onOpen={setTimeline} metrics={metrics} errors={metricErrors} events={events} window={metricsWindow} onWindowChange={setMetricsWindow} locale={locale} dark={theme.resolved === "dark"} />}
        {view === "incidents" && <IncidentsView incidents={incidents} />}
        {view === "logs" && <LogsView api={api} onError={(value) => showMessage(value, "error")} />}
        {view === "captures" && <CapturesView api={api} config={config} onError={(value) => showMessage(value, "error")} onSuccess={showMessage} canOperate={canOperate} canSensitive={canSensitive} />}
        {view === "diagnostics" && <DiagnosticsView report={diagnostics} busy={diagnosticBusy} run={runDiagnostics} download={downloadDiagnostics} canOperate={canOperate} />}
        {view === "settings" && canOperate && <SettingsView config={config} baseline={savedConfig} runtimeInfo={runtimeInfo} setConfig={setConfig} save={save} reload={reload} dirty={dirty} discard={() => setConfig(savedConfig)} themeMode={theme.mode} setThemeMode={theme.setMode} />}
      </ViewErrorBoundary>
    </main>

    <nav className="mobile-bottom-nav" aria-label={t("common:brandSubtitle")}>{mobileViews.filter((itemView) => canOperate || itemView !== "settings").map((itemView) => { const Icon = navigation.find((item) => item.view === itemView)!.icon; return <button key={itemView} aria-current={view === itemView ? "page" : undefined} className={view === itemView ? "active" : ""} onClick={() => selectView(itemView)}><Icon size={19} /><span>{t(`common:nav.${itemView}`)}</span></button>; })}</nav>
    <div className="mobile-tools" hidden={!mobileTools}><div className="mobile-tools-row"><button className="button" onClick={() => selectView("logs")}><ScrollText size={17} />{t("common:nav.logs")}</button><button className="button" onClick={() => selectView("captures")}><FileLock2 size={17} />{t("common:nav.captures")}</button></div><ThemeSelector mode={theme.mode} onChange={theme.setMode} /><div className="mobile-tools-row"><LanguageSelector /><button className="button" onClick={logout}><LogOut size={17} />{t("common:actions.logout")}</button></div></div>
    {timeline && <TimelinePanel record={timeline} onClose={() => setTimeline(null)} />}
  </div>;
}
