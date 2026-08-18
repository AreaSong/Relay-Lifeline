import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  FileLock2, HeartPulse, LogOut, Search, ScrollText, ShieldAlert, ShieldCheck,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { ApiClient, errorMessage } from "./api";
import { AppNavigation, allViews, iconForView, mobileViews } from "./components/AppNavigation";
import type { View } from "./components/AppNavigation";
import { ConfirmDialog } from "./components/ConfirmDialog";
import type { ConfirmDialogState } from "./components/ConfirmDialog";
import { LanguageSelector } from "./components/LanguageSelector";
import { OverviewPriorityPanel } from "./components/OverviewPriorityPanel";
import { SignalTopology } from "./components/SignalTopology";
import { ThemeSelector } from "./components/ThemeSelector";
import { TimelinePanel } from "./components/TimelinePanel";
import { ViewErrorBoundary } from "./components/ViewErrorBoundary";
import { WorkspaceHeader } from "./components/WorkspaceHeader";
import type { SearchTarget } from "./components/GlobalSearch";
import { normalizeLocale } from "./i18n";
import { mergeHistoryPage } from "./historyPagination";
import { useTheme } from "./theme";
import type {
  Alert, Config, DiagnosticReport, HistoryRecord, Incident, MetricsErrors, MetricsSnapshot,
	MetricsWindow, MonitoringEvent, RealtimeSnapshot, RepeatTask, RuntimeInfo, SessionInfo, Status,
} from "./types";
import { CapturesView } from "./views/CapturesView";
import { DiagnosticsView } from "./views/DiagnosticsView";
import { HistoryView } from "./views/HistoryView";
import { IncidentsView } from "./views/IncidentsView";
import { LogsView } from "./views/LogsView";
import { OverviewView } from "./views/OverviewView";
import { RequestsView } from "./views/RequestsView";
import { SettingsView } from "./views/SettingsView";

const railStorageKey = "relay-lifeline-rail-collapsed";
const configSectionLabelKeys: Record<string, string> = {
  server: "service", upstream: "service", retry: "retry", stream: "stream", queue: "traffic", history: "traffic",
  observability: "observability", capture: "capture", risk: "risk", localization: "localization", notifications: "notifications",
  logging: "logging", persistence: "persistence", incidents: "incidents", lifecycle: "lifecycle",
  "management-security": "managementSecurity", "metrics-export": "metrics",
};

function storedRailState() {
  return localStorage.getItem(railStorageKey) === "true";
}

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
    <section className="login-visual"><div className="login-wordmark"><span className="rail-brand"><HeartPulse size={19} /></span><span>Relay-Lifeline</span></div>
      <div className="login-topology"><SignalTopology upstreamState="unknown" active={0} waiting={0} labels={labels} locale={i18n.resolvedLanguage} /></div>
      <div className="login-statement"><h1>Relay-Lifeline</h1><strong>{t("auth:statement")}</strong><p>{t("auth:statementDetail")}</p></div>
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
	const [historyCursor, setHistoryCursor] = useState<string | undefined>();
	const [historyHasMore, setHistoryHasMore] = useState(false);
  const [incidents, setIncidents] = useState<Incident[]>([]);
	const [incidentCursor, setIncidentCursor] = useState<string | undefined>();
	const [incidentsHaveMore, setIncidentsHaveMore] = useState(false);
  const [repeatTasks, setRepeatTasks] = useState<RepeatTask[]>([]);
  const [timeline, setTimeline] = useState<HistoryRecord | null>(null);
  const [diagnostics, setDiagnostics] = useState<DiagnosticReport | null>(null);
  const [diagnosticBusy, setDiagnosticBusy] = useState(false);
  const [metricsWindow, setMetricsWindow] = useState<MetricsWindow>("1h");
  const [metrics, setMetrics] = useState<MetricsSnapshot | null>(null);
  const [metricErrors, setMetricErrors] = useState<MetricsErrors | null>(null);
  const [events, setEvents] = useState<MonitoringEvent[]>([]);
  const [mobileTools, setMobileTools] = useState(false);
	const [pageVisible, setPageVisible] = useState(() => document.visibilityState !== "hidden");
  const [searchTarget, setSearchTarget] = useState<SearchTarget | null>(null);
  const [selectedOverviewRequestId, setSelectedOverviewRequestId] = useState<string | undefined>(undefined);
  const [railCollapsed, setRailCollapsed] = useState(storedRailState);
  const [message, setMessage] = useState("");
  const [messageKind, setMessageKind] = useState<"success" | "error">("success");
  const [bootstrapError, setBootstrapError] = useState("");
  const [saving, setSaving] = useState(false);
  const [confirmation, setConfirmation] = useState<{ options: ConfirmDialogState; resolve: (value: boolean) => void } | null>(null);
  const confirmationPending = useRef(false);
  const resetAuthentication = useCallback((reason: "required" | "expired" | null) => {
    setAuthenticated(false); setAuthReason(reason); setSession(null); setStatus(null); setConfig(null); setSavedConfig(null); setRuntimeInfo(null);
		setAlerts([]); setHistory([]); setHistoryCursor(undefined); setHistoryHasMore(false); setIncidents([]); setIncidentCursor(undefined); setIncidentsHaveMore(false); setRepeatTasks([]); setTimeline(null); setDiagnostics(null); setMetrics(null); setMetricErrors(null); setEvents([]); setMessage(""); setBootstrapError(""); setSearchTarget(null); setMobileTools(false);
    confirmationPending.current = false;
    setConfirmation((current) => { current?.resolve(false); return null; });
  }, []);
  const api = useMemo(() => new ApiClient(locale, (code) => resetAuthentication(code === "SESSION_EXPIRED" ? "expired" : "required")), [locale, resetAuthentication]);
  const dirty = useMemo(() => !!config && !!savedConfig && JSON.stringify(config) !== JSON.stringify(savedConfig), [config, savedConfig]);
  const canOperate = session?.capabilities.includes("operate") || false;
  const canSensitive = session?.capabilities.includes("sensitive") || false;
  const requestConfirmation = useCallback((options: ConfirmDialogState) => {
    if (confirmationPending.current) return Promise.resolve(false);
    confirmationPending.current = true;
    return new Promise<boolean>((resolve) => setConfirmation({ options, resolve }));
  }, []);
  const finishConfirmation = useCallback((result: boolean) => {
    confirmationPending.current = false;
    setConfirmation((current) => { current?.resolve(result); return null; });
  }, []);

  const showMessage = useCallback((value: string, kind: "success" | "error" = "success") => {
    setMessage(value); setMessageKind(kind); window.setTimeout(() => setMessage(""), 4000);
  }, []);
	const loadHistory = useCallback(async (cursor?: string, preserveLoaded = false) => {
		const page = await api.history({ cursor, limit: 100 });
		setHistory((current) => cursor
			? [...current, ...page.items.filter((item) => !current.some((existing) => existing.id === item.id))]
			: preserveLoaded ? mergeHistoryPage(current, page.items) : page.items);
		if (cursor || !preserveLoaded) {
			setHistoryCursor(page.nextCursor); setHistoryHasMore(page.hasMore);
		}
	}, [api]);
	const loadIncidents = useCallback(async (cursor?: string) => {
		const page = await api.incidents({ cursor, limit: 100 });
		setIncidents((current) => cursor ? [...current, ...page.items.filter((item) => !current.some((existing) => existing.id === item.id))] : page.items);
		setIncidentCursor(page.nextCursor); setIncidentsHaveMore(page.hasMore);
	}, [api]);
  const refresh = useCallback(async () => {
		const [nextStatus, nextAlerts, nextIncidents, nextRepeats] = await Promise.all([api.status(), api.alerts(), api.incidents({ limit: 100 }), api.repeatTasks()]);
		setStatus(nextStatus); setAlerts(nextAlerts); setIncidents(nextIncidents.items); setIncidentCursor(nextIncidents.nextCursor); setIncidentsHaveMore(nextIncidents.hasMore); setRepeatTasks(nextRepeats);
  }, [api]);
  const refreshMonitoring = useCallback(async () => {
    const [nextMetrics, nextErrors, nextEvents, nextRuntimeInfo] = await Promise.all([
      api.metrics(metricsWindow), api.metricErrors(metricsWindow), api.events(0, 200), api.runtimeInfo(),
    ]);
    setMetrics(nextMetrics); setMetricErrors(nextErrors); setEvents(nextEvents.events); setRuntimeInfo(nextRuntimeInfo);
  }, [api, metricsWindow]);

  const confirmSettingsLeave = useCallback(async () => {
    if (!dirty) return true;
    const confirmed = await requestConfirmation({
      title: t("settings:leaveConfirmTitle"), description: t("settings:leaveConfirm"), confirmLabel: t("settings:leaveConfirmAction"), tone: "danger",
    });
    if (confirmed && savedConfig) setConfig(savedConfig);
    return confirmed;
  }, [dirty, requestConfirmation, savedConfig, t]);

  const selectView = useCallback(async (next: View, updateHash = true) => {
    if (next === "settings" && !canOperate) {
      if (!updateHash) window.history.replaceState(null, "", `#/${view}`);
      return;
    }
    if (next === view) { setMobileTools(false); return; }
    if (view === "settings" && next !== "settings" && !await confirmSettingsLeave()) {
      if (!updateHash) window.history.replaceState(null, "", `#/${view}`);
      return;
    }
    setView(next); setMobileTools(false); setTimeline(null);
    if (updateHash) window.history.pushState(null, "", `#/${next}`);
		if (next === "history") await Promise.all([loadHistory(), refreshMonitoring()]).catch((reason) => showMessage(errorMessage(reason), "error"));
	}, [canOperate, confirmSettingsLeave, loadHistory, refreshMonitoring, showMessage, view]);

  useEffect(() => { document.title = `${t(`common:title.${view}`)} · Relay-Lifeline`; }, [locale, t, view]);
  useEffect(() => {
    const changed = () => { const next = currentView(); if (next !== view) void selectView(next, false); };
    window.addEventListener("hashchange", changed);
    window.addEventListener("popstate", changed);
    return () => { window.removeEventListener("hashchange", changed); window.removeEventListener("popstate", changed); };
  }, [selectView, view]);
  useEffect(() => {
    if (!authenticated) return;
    let disposed = false;
    setBootstrapError("");
    void api.session().then(async (nextSession) => {
      if (disposed) return;
      setSession(nextSession);
      await Promise.all([
			refresh(), api.config().then((value) => { setConfig(value); setSavedConfig(value); }), loadHistory(), api.runtimeInfo().then(setRuntimeInfo),
      ]);
    }).catch((reason) => { if (!disposed) setBootstrapError(errorMessage(reason)); });
    setTimeline(null); setDiagnostics(null);
    return () => { disposed = true; };
	}, [api, authenticated, loadHistory, refresh]);
  useEffect(() => {
    if (!authenticated || !session) return;
		return api.subscribe((event) => {
			if (event.type === "sync" || event.type === "reset") {
				const snapshot = event.data as RealtimeSnapshot;
				setStatus(snapshot.status); setAlerts(snapshot.alerts); setIncidents(snapshot.incidents); setRepeatTasks(snapshot.repeatTasks || []);
				if (snapshot.metrics) setMetrics(snapshot.metrics);
				return;
			}
			if (event.type === "status") setStatus(event.data as Status);
			else if (event.type === "alerts") setAlerts(event.data as Alert[]);
			else if (event.type === "incidents") setIncidents(event.data as Incident[]);
			else if (event.type === "metrics") setMetrics(event.data as MetricsSnapshot);
			else if (event.type === "repeat_tasks") setRepeatTasks(event.data as RepeatTask[]);
    }, () => { void refresh().catch(() => undefined); });
  }, [api, authenticated, refresh, session]);
	useEffect(() => {
		const changed = () => setPageVisible(document.visibilityState !== "hidden");
		document.addEventListener("visibilitychange", changed);
		return () => document.removeEventListener("visibilitychange", changed);
	}, []);
  useEffect(() => {
    if (session && !canOperate && view === "settings") {
      setView("overview"); window.history.replaceState(null, "", "#/overview");
    }
  }, [canOperate, session, view]);
  useEffect(() => {
		if (!authenticated || !session || !pageVisible) return;
    void refreshMonitoring().catch((reason) => showMessage(errorMessage(reason), "error"));
    const metricsTimer = window.setInterval(() => refreshMonitoring().catch(() => undefined), 10_000);
    return () => window.clearInterval(metricsTimer);
	}, [authenticated, pageVisible, refreshMonitoring, session, showMessage]);
  useEffect(() => {
		if (!authenticated || !session || !pageVisible || view !== "history") return;
		const historyTimer = window.setInterval(() => loadHistory(undefined, true).catch(() => undefined), 10_000);
    return () => window.clearInterval(historyTimer);
	}, [authenticated, loadHistory, pageVisible, session, view]);
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty) event.preventDefault(); };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);
  useEffect(() => {
    if (!mobileTools) return;
    const panel = document.getElementById("mobile-tools-panel");
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    panel?.querySelector<HTMLElement>("button, select")?.focus();
    const close = (event: KeyboardEvent) => {
      if (event.key === "Escape") { setMobileTools(false); return; }
      if (event.key !== "Tab" || !panel) return;
      const focusable = Array.from(panel.querySelectorAll<HTMLElement>('button, select, [tabindex]:not([tabindex="-1"])')).filter((element) => !element.hasAttribute("disabled"));
      if (!focusable.length) return;
      const first = focusable[0]; const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", close);
    return () => { window.removeEventListener("keydown", close); previous?.focus(); };
  }, [mobileTools]);
  useEffect(() => {
    localStorage.setItem(railStorageKey, String(railCollapsed));
  }, [railCollapsed]);

  async function login(value: string) {
    const nextSession = await api.login(value);
    setAuthReason(null); setSession(nextSession); setAuthenticated(true);
  }
  async function logout() {
    await api.logout().catch(() => undefined);
    resetAuthentication(null);
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
    if (!config || saving) return;
    setSaving(true);
    try {
      const plan = await api.validateConfig(config);
      const sectionNames = (sections: string[]) => Array.from(new Set(sections.map((section) => {
        const key = configSectionLabelKeys[section];
        return key ? t(`settings:sections.${key}.title`) : section;
      }))).join(", ") || t("common:notAvailable");
      const summary = t("settings:reviewPlan", {
        changed: sectionNames(plan.changedSections), hot: sectionNames(plan.hotReloadSections), restart: sectionNames(plan.restartSections),
      });
      const confirmed = await requestConfirmation({
        title: t("settings:restartConfirmTitle"),
        description: `${plan.restartRequired
          ? t("settings:restartConfirm", { count: plan.restartSections.length })
          : t("settings:reviewConfirm", { count: plan.changedSections.length })}\n${summary}`,
        confirmLabel: t("common:actions.save"),
      });
      if (!confirmed) return;
      const result = await api.saveConfig(config);
      setSavedConfig(config);
      showMessage(result.backupPath ? t("settings:savedBackup", { path: result.backupPath }) : result.restartRequired ? t("settings:savedRestart") : t("settings:saved"));
      await refreshMonitoring();
    }
    catch (reason) { showMessage(errorMessage(reason, "save"), "error"); }
    finally { setSaving(false); }
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
  if (bootstrapError) return <main className="bootstrap-error"><span className="rail-brand"><HeartPulse size={22} /></span><h1>{t("common:connectionError.title")}</h1><p>{bootstrapError}</p><button className="button primary" onClick={() => window.location.reload()}><ShieldCheck size={17} />{t("common:connectionError.retry")}</button></main>;
  if (!status || !config || !savedConfig || !session) return <div className="loading"><span><HeartPulse size={26} />{t("common:loading")}</span></div>;

  const incident = status.upstream.state === "degraded" && (status.waiting + status.queued > 0 || (status.upstream.lastChecked ? Date.now() - new Date(status.upstream.lastChecked).getTime() >= 10_000 : false));
  const mobileNavigation = mobileViews.filter((itemView) => canOperate || itemView !== "settings");
  return <div className={`app-shell view-${view}${railCollapsed ? " rail-collapsed" : ""}${timeline ? " inspector-open" : ""}${incident ? " incident-mode" : ""}`}>
    <AppNavigation view={view} collapsed={railCollapsed} session={session} config={config} runtimeInfo={runtimeInfo} themeMode={theme.mode} onThemeChange={theme.setMode} onSelect={(next) => { setSearchTarget(null); void selectView(next); }} onCollapse={() => setRailCollapsed((value) => !value)} onLogout={() => void logout()} />

    <main className={`workspace workspace-${view}`}><WorkspaceHeader
      api={api} config={config} view={view} status={status} session={session} requests={status.requests} history={history} incidents={incidents} alerts={alerts}
      metricsWindow={metricsWindow} canOperate={canOperate} mobileToolsOpen={mobileTools} onWindowChange={setMetricsWindow} onOpen={(id) => void openTimeline(id)}
		onNavigate={(next, target) => { setSearchTarget(target || null); void selectView(next); }} onRefresh={() => { void refresh(); void refreshMonitoring(); void loadHistory(undefined, true); }} onPauseToggle={() => void togglePause()} onMobileTools={() => setMobileTools((open) => !open)}
    />
      {message && <div className={messageKind === "success" ? "success-banner page-banner" : "error-banner page-banner"} role="status">{message}</div>}
      <ViewErrorBoundary key={view} title={t("common:viewError.title")} description={t("common:viewError.description")} reloadLabel={t("common:viewError.reload")}>
        {view === "overview" && <OverviewView status={status} metrics={metrics} errors={metricErrors} alerts={alerts} incidents={incidents} window={metricsWindow} onOpen={(id) => setSelectedOverviewRequestId(id)} locale={locale} dark={theme.resolved === "dark"} incident={incident} selectedRequestId={selectedOverviewRequestId} />}
        {view === "requests" && <RequestsView status={status} metrics={metrics} repeatTasks={repeatTasks} api={api} refresh={refresh} onOpen={openTimeline} onError={(value) => showMessage(value, "error")} onSuccess={showMessage} canOperate={canOperate} confirm={requestConfirmation} />}
		{view === "history" && <HistoryView records={history} onOpen={setTimeline} metrics={metrics} errors={metricErrors} events={events} window={metricsWindow} onWindowChange={setMetricsWindow} locale={locale} dark={theme.resolved === "dark"} hasMore={historyHasMore} onLoadMore={() => void loadHistory(historyCursor)} />}
		{view === "incidents" && <IncidentsView api={api} incidents={incidents} selectedId={searchTarget?.kind === "incident" ? searchTarget.id : undefined} onOpen={setTimeline} hasMore={incidentsHaveMore} onLoadMore={() => void loadIncidents(incidentCursor)} />}
        {view === "logs" && <LogsView api={api} onError={(value) => showMessage(value, "error")} initialRequestId={searchTarget?.kind === "log" ? searchTarget.id : undefined} initialEvent={searchTarget?.kind === "log" ? searchTarget.detail : undefined} />}
        {view === "captures" && <CapturesView api={api} config={config} onError={(value) => showMessage(value, "error")} onSuccess={showMessage} canOperate={canOperate} canSensitive={canSensitive} confirm={requestConfirmation} selectedId={searchTarget?.kind === "capture" ? searchTarget.id : undefined} />}
        {view === "diagnostics" && <DiagnosticsView runtimeInfo={runtimeInfo} report={diagnostics} busy={diagnosticBusy} run={runDiagnostics} download={downloadDiagnostics} canOperate={canOperate} />}
		{view === "settings" && canOperate && <SettingsView api={api} config={config} baseline={savedConfig} runtimeInfo={runtimeInfo} setConfig={setConfig} save={save} reload={reload} dirty={dirty} busy={saving} discard={() => setConfig(savedConfig)} themeMode={theme.mode} setThemeMode={theme.setMode} />}
      </ViewErrorBoundary>
    </main>

    {view === "overview" && <aside className="desktop-overview-inspector" aria-label={t("overview:priority.title")}>
      <OverviewPriorityPanel alerts={alerts} incidents={incidents} requests={status.requests} locale={locale} onOpen={openTimeline} idSuffix="inspector" paused={status.paused} selectedRequestId={selectedOverviewRequestId} onClearSelected={() => setSelectedOverviewRequestId(undefined)} />
    </aside>}

    <nav className={`mobile-bottom-nav items-${mobileNavigation.length}`} aria-label={t("common:brandSubtitle")}>{mobileNavigation.map((itemView) => { const Icon = iconForView(itemView); return <button key={itemView} aria-current={view === itemView ? "page" : undefined} className={view === itemView ? "active" : ""} onClick={() => { setSearchTarget(null); void selectView(itemView); }}><Icon size={19} /><span>{t(`common:nav.${itemView}`)}</span></button>; })}</nav>
    <div id="mobile-tools-panel" className="mobile-tools" role="dialog" aria-modal="true" aria-label={t("common:tools")} hidden={!mobileTools}><button className="button mobile-search-action" onClick={() => { setMobileTools(false); window.dispatchEvent(new Event("relay:open-search")); }}><Search size={17} />{t("common:search.label")}</button><div className="mobile-tools-row mobile-tools-nav"><button className="button" onClick={() => void selectView("incidents")}><ShieldAlert size={17} />{t("common:nav.incidents")}</button><button className="button" onClick={() => void selectView("logs")}><ScrollText size={17} />{t("common:nav.logs")}</button><button className="button" onClick={() => void selectView("captures")}><FileLock2 size={17} />{t("common:nav.captures")}</button></div><ThemeSelector mode={theme.mode} onChange={theme.setMode} /><div className="mobile-tools-row"><LanguageSelector /><button className="button" onClick={() => void logout()}><LogOut size={17} />{t("common:actions.logout")}</button></div></div>
    {timeline && <TimelinePanel record={timeline} onClose={() => setTimeline(null)} />}
    {confirmation && <ConfirmDialog state={confirmation.options} onConfirm={() => finishConfirmation(true)} onCancel={() => finishConfirmation(false)} />}
  </div>;
}
