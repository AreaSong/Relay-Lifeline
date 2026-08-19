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
import { mergeMonitoringEvents, readMonitoringEvents } from "./eventFeed";
import { useTheme } from "./theme";
import type {
  Alert, Config, DiagnosticReport, HistoryRecord, Incident, MetricsErrors, MetricsSnapshot,
		ConfigRuntimeState, GovernanceStatus, HealthSummary, ListFilters, LoginOptions, MetricsWindow, MonitoringEvent, PolicyStatus, RealtimeSnapshot, RepeatTask, RuntimeInfo, SessionInfo, Status, TelemetryStatus, UpstreamPoolStatus,
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
  logging: "logging", persistence: "persistence", incidents: "incidents", lifecycle: "lifecycle", governance: "governance", slo: "slo", "traffic-policy": "policy", upstreams: "upstreamPool", egress: "egress",
  "management-security": "managementSecurity", "metrics-export": "metrics",
};

function storedRailState() {
  return localStorage.getItem(railStorageKey) === "true";
}

function currentView(): View {
	const candidate = window.location.hash.replace(/^#\/?/, "").split("?", 1)[0] as View;
  return allViews.includes(candidate) ? candidate : "overview";
}

function filtersFromHash(target: View): ListFilters {
	if (currentView() !== target) return { q: "", state: "", from: "", to: "" };
	const query = window.location.hash.split("?", 2)[1] || "";
	const params = new URLSearchParams(query);
	return { q: params.get("q") || "", state: params.get("state") || "", from: params.get("from") || "", to: params.get("to") || "" };
}

function viewHash(view: View, filters?: ListFilters) {
	const params = new URLSearchParams();
	if (filters) Object.entries(filters).forEach(([key, value]) => { if (value) params.set(key, value); });
	const query = params.toString();
	return `#/${view}${query ? `?${query}` : ""}`;
}

function Login({ onLogin, onOIDC, options, themeMode, setThemeMode, sessionExpired, oidcFailed }: {
	onLogin: (token: string) => Promise<void>;
	onOIDC: () => void;
	options: LoginOptions | null;
	themeMode: ReturnType<typeof useTheme>["mode"];
  setThemeMode: ReturnType<typeof useTheme>["setMode"];
	sessionExpired?: boolean;
	oidcFailed?: boolean;
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
		  <div className="login-panel"><h2>{t("auth:title")}</h2><p>{t("auth:description")}</p>
			{options?.oidc.enabled && <button className="button primary oidc-login" type="button" disabled={!options.oidc.available} onClick={onOIDC}><ShieldCheck size={17} />{options.oidc.available ? t("auth:sso") : t("auth:ssoUnavailable")}</button>}
			{oidcFailed && <div className="error-banner" role="alert">{t("auth:ssoFailed")}</div>}
			{(options?.localEnabled ?? true) && <form className="break-glass-login" onSubmit={submit}><strong>{options?.oidc.enabled ? t("auth:breakGlass") : t("auth:localAccess")}</strong>
			<input className="sr-only" type="text" name="username" value="relay-lifeline-admin" autoComplete="username" tabIndex={-1} aria-hidden="true" readOnly />
			<label className="field"><span>{t("auth:adminKey")}</span><input name="admin-key" type="password" value={token} onChange={(event) => { setToken(event.target.value); setError(""); }} autoComplete="current-password" autoFocus required /></label>
			{(error || sessionExpired) && <div className="error-banner" role="alert">{error || t("auth:sessionExpired")}</div>}
			<button className="button primary" disabled={busy || !token}><ShieldCheck size={17} />{busy ? t("auth:verifying") : t("auth:enter")}</button>
			</form>}</div>
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
  const [healthSummary, setHealthSummary] = useState<HealthSummary | null>(null);
  const [config, setConfig] = useState<Config | null>(null);
	const [savedConfig, setSavedConfig] = useState<Config | null>(null);
	const [configState, setConfigState] = useState<ConfigRuntimeState | null>(null);
	const [loginOptions, setLoginOptions] = useState<LoginOptions | null>(null);
	const [upstreamStatus, setUpstreamStatus] = useState<UpstreamPoolStatus | null>(null);
	const [governanceStatus, setGovernanceStatus] = useState<GovernanceStatus | null>(null);
	const [policyStatus, setPolicyStatus] = useState<PolicyStatus | null>(null);
	const [telemetryStatus, setTelemetryStatus] = useState<TelemetryStatus | null>(null);
  const [runtimeInfo, setRuntimeInfo] = useState<RuntimeInfo | null>(null);
  const [alerts, setAlerts] = useState<Alert[]>([]);
  const [history, setHistory] = useState<HistoryRecord[]>([]);
	const [historyFilters, setHistoryFilters] = useState<ListFilters>(() => filtersFromHash("history"));
	const [historyCursor, setHistoryCursor] = useState<string | undefined>();
	const [historyHasMore, setHistoryHasMore] = useState(false);
  const [incidents, setIncidents] = useState<Incident[]>([]);
	const [incidentResults, setIncidentResults] = useState<Incident[]>([]);
	const [incidentFilters, setIncidentFilters] = useState<ListFilters>(() => filtersFromHash("incidents"));
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
	const eventCursor = useRef(0);
	const eventLoad = useRef<Promise<void> | null>(null);
  const [mobileTools, setMobileTools] = useState(false);
	const [pageVisible, setPageVisible] = useState(() => document.visibilityState !== "hidden");
  const [searchTarget, setSearchTarget] = useState<SearchTarget | null>(null);
  const [selectedOverviewRequestId, setSelectedOverviewRequestId] = useState<string | undefined>(undefined);
  const [railCollapsed, setRailCollapsed] = useState(storedRailState);
  const [message, setMessage] = useState("");
  const [messageKind, setMessageKind] = useState<"success" | "error">("success");
  const messageTimer = useRef<number | undefined>(undefined);
  const [bootstrapError, setBootstrapError] = useState("");
  const [saving, setSaving] = useState(false);
  const [confirmation, setConfirmation] = useState<{ options: ConfirmDialogState; resolve: (value: boolean) => void } | null>(null);
  const confirmationPending = useRef(false);
  const resetAuthentication = useCallback((reason: "required" | "expired" | null) => {
		setAuthenticated(false); setAuthReason(reason); setSession(null); setStatus(null); setHealthSummary(null); setConfig(null); setSavedConfig(null); setConfigState(null); setUpstreamStatus(null); setGovernanceStatus(null); setPolicyStatus(null); setTelemetryStatus(null); setRuntimeInfo(null);
		setAlerts([]); setHistory([]); setHistoryCursor(undefined); setHistoryHasMore(false); setIncidents([]); setIncidentResults([]); setIncidentCursor(undefined); setIncidentsHaveMore(false); setRepeatTasks([]); setTimeline(null); setDiagnostics(null); setMetrics(null); setMetricErrors(null); setEvents([]); setMessage(""); setBootstrapError(""); setSearchTarget(null); setMobileTools(false);
		eventCursor.current = 0;
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
    setMessage(value); setMessageKind(kind);
    if (messageTimer.current) window.clearTimeout(messageTimer.current);
    messageTimer.current = window.setTimeout(() => setMessage(""), 4000);
  }, []);
	const loadHistory = useCallback(async (cursor?: string, preserveLoaded = false) => {
		const page = await api.history({ ...historyFilters, cursor, limit: 100 });
		setHistory((current) => cursor
			? [...current, ...page.items.filter((item) => !current.some((existing) => existing.id === item.id))]
			: preserveLoaded ? mergeHistoryPage(current, page.items) : page.items);
			setHistoryCursor(page.nextCursor); setHistoryHasMore(page.hasMore);
	}, [api, historyFilters]);
	const loadIncidents = useCallback(async (cursor?: string) => {
		const page = await api.incidents({ ...incidentFilters, cursor, limit: 100 });
		setIncidentResults((current) => cursor ? [...current, ...page.items.filter((item) => !current.some((existing) => existing.id === item.id))] : page.items);
		setIncidentCursor(page.nextCursor); setIncidentsHaveMore(page.hasMore);
	}, [api, incidentFilters]);
	const applyHistoryFilters = useCallback(async (next: ListFilters) => {
		setHistoryFilters(next);
		if (currentView() === "history") window.history.replaceState(null, "", viewHash("history", next));
		try {
			const page = await api.history({ ...next, limit: 100 });
			setHistory(page.items); setHistoryCursor(page.nextCursor); setHistoryHasMore(page.hasMore);
		} catch (reason) { showMessage(errorMessage(reason), "error"); }
	}, [api, showMessage]);
	const applyIncidentFilters = useCallback(async (next: ListFilters) => {
		setIncidentFilters(next);
		if (currentView() === "incidents") window.history.replaceState(null, "", viewHash("incidents", next));
		try {
			const page = await api.incidents({ ...next, limit: 100 });
			setIncidentResults(page.items); setIncidentCursor(page.nextCursor); setIncidentsHaveMore(page.hasMore);
		} catch (reason) { showMessage(errorMessage(reason), "error"); }
	}, [api, showMessage]);
  const refresh = useCallback(async () => {
		const [nextStatus, nextHealth, nextAlerts, nextIncidents, nextRepeats, nextUpstreams, nextGovernance, nextTelemetry] = await Promise.all([api.status(), api.healthSummary(), api.alerts(), api.incidents({ limit: 100 }), api.repeatTasks(), api.upstreamStatus(), api.governanceStatus(), api.telemetryStatus()]);
		setStatus(nextStatus); setAlerts(nextAlerts); setIncidents(nextIncidents.items); setIncidentCursor(nextIncidents.nextCursor); setIncidentsHaveMore(nextIncidents.hasMore); setRepeatTasks(nextRepeats);
		setHealthSummary(nextHealth);
		setUpstreamStatus(nextUpstreams); setGovernanceStatus(nextGovernance); setTelemetryStatus(nextTelemetry);
  }, [api]);
	const refreshEvents = useCallback(async () => {
		if (eventLoad.current) return eventLoad.current;
		const task = (async () => {
			const batch = await readMonitoringEvents((after, limit) => api.events(after, limit), eventCursor.current);
			eventCursor.current = batch.nextAfter;
			setEvents((current) => mergeMonitoringEvents(current, batch.events, batch.reset));
		})();
		eventLoad.current = task;
		try { await task; }
		finally { if (eventLoad.current === task) eventLoad.current = null; }
	}, [api]);
  const refreshMonitoring = useCallback(async () => {
		const [nextMetrics, nextErrors, , nextRuntimeInfo, nextPolicyStatus] = await Promise.all([
		  api.metrics(metricsWindow), api.metricErrors(metricsWindow), refreshEvents(), api.runtimeInfo(), api.policyStatus().catch(() => null),
		]);
		setMetrics(nextMetrics); setMetricErrors(nextErrors); setRuntimeInfo(nextRuntimeInfo); setPolicyStatus(nextPolicyStatus);
	}, [api, metricsWindow, refreshEvents]);

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
		if (updateHash) window.history.pushState(null, "", viewHash(next, next === "history" ? historyFilters : next === "incidents" ? incidentFilters : undefined));
		if (next === "history") await Promise.all([loadHistory(), refreshMonitoring()]).catch((reason) => showMessage(errorMessage(reason), "error"));
		if (next === "incidents") await loadIncidents().catch((reason) => showMessage(errorMessage(reason), "error"));
	}, [canOperate, confirmSettingsLeave, historyFilters, incidentFilters, loadHistory, loadIncidents, refreshMonitoring, showMessage, view]);

	useEffect(() => { document.title = `${t(`common:title.${view}`)} · Relay-Lifeline`; }, [locale, t, view]);
	useEffect(() => {
		if (authenticated) return;
		void api.loginOptions().then(setLoginOptions).catch(() => setLoginOptions({ localEnabled: true, oidc: { enabled: false, available: false } }));
	}, [api, authenticated]);
  useEffect(() => {
    const changed = () => {
		const next = currentView();
		if (next === "history") { const filters = filtersFromHash("history"); setHistoryFilters(filters); void applyHistoryFilters(filters); }
		if (next === "incidents") { const filters = filtersFromHash("incidents"); setIncidentFilters(filters); void applyIncidentFilters(filters); }
		if (next !== view) void selectView(next, false);
	};
    window.addEventListener("hashchange", changed);
    window.addEventListener("popstate", changed);
    return () => { window.removeEventListener("hashchange", changed); window.removeEventListener("popstate", changed); };
  }, [applyHistoryFilters, applyIncidentFilters, selectView, view]);
  useEffect(() => {
    if (!authenticated) return;
    let disposed = false;
    setBootstrapError("");
    void api.session().then(async (nextSession) => {
      if (disposed) return;
      setSession(nextSession);
      await Promise.all([
				refresh(), api.config().then((value) => { setConfig(value); setSavedConfig(value); }), api.configState().then(setConfigState), loadHistory(), loadIncidents(), api.runtimeInfo().then(setRuntimeInfo),
      ]);
    }).catch((reason) => { if (!disposed) setBootstrapError(errorMessage(reason)); });
    setTimeline(null); setDiagnostics(null);
    return () => { disposed = true; };
	}, [api, authenticated, loadHistory, loadIncidents, refresh]);
  useEffect(() => {
		if (!authenticated || !session || !pageVisible) return;
		void refresh().catch(() => undefined);
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
	}, [api, authenticated, pageVisible, refresh, session]);
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
    try { status?.mode === "draining" || status?.mode === "maintenance" || status?.paused ? await api.resume() : await api.pause(); await refresh(); await refreshMonitoring(); }
    catch (reason) { showMessage(errorMessage(reason), "error"); }
  }
  async function setControlMode(mode: "drain" | "maintenance") {
		const accepted = await requestConfirmation({
			title: t(`common:control.${mode}.title`), description: t(`common:control.${mode}.description`), confirmLabel: t(`common:control.${mode}.confirm`), tone: "danger",
		});
		if (!accepted) return;
		try { mode === "drain" ? await api.drain() : await api.maintenance(); await refresh(); await refreshMonitoring(); }
		catch (reason) { showMessage(errorMessage(reason), "error"); }
	}
  async function save() {
    if (!config || saving) return;
		const ordinaryDirty = savedConfig && (Object.keys(config) as Array<keyof Config>).some((key) => key !== "trafficPolicy" && JSON.stringify(config[key]) !== JSON.stringify(savedConfig[key]));
		if (!ordinaryDirty) {
			showMessage(t("settings:policy.unsavedHint"), "error");
			return;
		}
    setSaving(true);
    try {
      // Traffic policy revisions have their own draft/release journal. Keep
      // them out of the ordinary settings save payload so a global Save can
      // never hot-apply an unreviewed route or deny rule.
      const configForGlobalSave = { ...config, trafficPolicy: savedConfig?.trafficPolicy || config.trafficPolicy };
      const plan = await api.validateConfig(configForGlobalSave);
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
		  const authenticationChanged = plan.fields?.some((field) => field.path === "management-security.authentication") || false;
		  const result = await api.saveConfig(configForGlobalSave, authenticationChanged);
		  setSavedConfig(configForGlobalSave);
		  setConfigState(await api.configState());
      showMessage(result.backupPath ? t("settings:savedBackup", { path: result.backupPath }) : result.restartRequired ? t("settings:savedRestart") : t("settings:saved"));
      await refreshMonitoring();
    }
    catch (reason) { showMessage(errorMessage(reason, "save"), "error"); }
    finally { setSaving(false); }
  }
  async function reload() {
		try { await api.reloadConfig(); const [value, nextConfigState] = await Promise.all([api.config(), api.configState()]); setConfig(value); setSavedConfig(value); setConfigState(nextConfigState); showMessage(t("settings:reloaded")); await refreshMonitoring(); }
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

	if (!authenticated) return <Login onLogin={login} onOIDC={() => api.oidcLogin()} options={loginOptions} themeMode={theme.mode} setThemeMode={theme.setMode} sessionExpired={authReason === "expired"} oidcFailed={new URLSearchParams(window.location.search).get("auth") === "oidc_failed"} />;
  if (bootstrapError) return <main className="bootstrap-error"><span className="rail-brand"><HeartPulse size={22} /></span><h1>{t("common:connectionError.title")}</h1><p>{bootstrapError}</p><button className="button primary" onClick={() => window.location.reload()}><ShieldCheck size={17} />{t("common:connectionError.retry")}</button></main>;
  if (!status || !config || !savedConfig || !session) return <div className="loading"><span><HeartPulse size={26} />{t("common:loading")}</span></div>;

  const incident = status.upstream.state === "degraded" && (status.waiting + status.queued > 0 || (status.upstream.lastChecked ? Date.now() - new Date(status.upstream.lastChecked).getTime() >= 10_000 : false));
  const mobileNavigation = mobileViews.filter((itemView) => canOperate || itemView !== "settings");
  return <div className={`app-shell view-${view}${railCollapsed ? " rail-collapsed" : ""}${timeline ? " inspector-open" : ""}${incident ? " incident-mode" : ""}`}>
    <AppNavigation view={view} collapsed={railCollapsed} session={session} config={config} runtimeInfo={runtimeInfo} themeMode={theme.mode} onThemeChange={theme.setMode} onSelect={(next) => { setSearchTarget(null); void selectView(next); }} onCollapse={() => setRailCollapsed((value) => !value)} onLogout={() => void logout()} />

    <main className={`workspace workspace-${view}`}><WorkspaceHeader
      api={api} config={config} view={view} status={status} healthSummary={healthSummary} session={session} requests={status.requests} history={history} incidents={incidents} alerts={alerts}
      metricsWindow={metricsWindow} canOperate={canOperate} mobileToolsOpen={mobileTools} onWindowChange={setMetricsWindow} onOpen={(id) => void openTimeline(id)}
			onNavigate={(next, target) => { setSearchTarget(target || null); void selectView(next); }} onRefresh={() => { void refresh(); void refreshMonitoring(); void loadHistory(undefined, true); }} onPauseToggle={() => void togglePause()} onDrain={() => void setControlMode("drain")} onMaintenance={() => void setControlMode("maintenance")} onMobileTools={() => setMobileTools((open) => !open)}
    />
      {message && <div className={messageKind === "success" ? "success-banner page-banner" : "error-banner page-banner"} role="status">{message}</div>}
		{configState?.pendingRestart.restartRequired && <div className="warning-banner page-banner pending-restart-global" role="status"><strong>{t("settings:pendingRestart.title")}</strong><span>{t("settings:pendingRestart.revisions", { active: configState.activeRevision, desired: configState.desiredRevision })}</span><small>{configState.pendingRestart.fields?.filter((field) => field.applyMode === "restart").map((field) => field.path).join(", ") || configState.pendingRestart.restartSections.join(", ")}</small><button className="link-button" onClick={() => void selectView("settings")}>{t("settings:pendingRestart.openSettings")}</button></div>}
	  {status.persistenceDegraded && <div className="error-banner page-banner" role="alert">{t("common:persistenceDegraded", { count: status.persistencePending || 0 })}</div>}
      <ViewErrorBoundary key={view} title={t("common:viewError.title")} description={t("common:viewError.description")} reloadLabel={t("common:viewError.reload")}>
		{view === "overview" && <OverviewView status={status} healthSummary={healthSummary} governanceStatus={governanceStatus} policyStatus={policyStatus} metrics={metrics} errors={metricErrors} alerts={alerts} incidents={incidents} window={metricsWindow} onOpen={(id) => setSelectedOverviewRequestId(id)} locale={locale} dark={theme.resolved === "dark"} incident={incident} selectedRequestId={selectedOverviewRequestId} />}
        {view === "requests" && <RequestsView status={status} metrics={metrics} repeatTasks={repeatTasks} api={api} refresh={refresh} onOpen={openTimeline} onError={(value) => showMessage(value, "error")} onSuccess={showMessage} canOperate={canOperate} confirm={requestConfirmation} />}
		{view === "history" && <HistoryView records={history} filters={historyFilters} onApplyFilters={applyHistoryFilters} onOpen={setTimeline} metrics={metrics} errors={metricErrors} events={events} window={metricsWindow} onWindowChange={setMetricsWindow} locale={locale} dark={theme.resolved === "dark"} hasMore={historyHasMore} onLoadMore={() => void loadHistory(historyCursor)} />}
		{view === "incidents" && <IncidentsView api={api} incidents={incidentResults} filters={incidentFilters} onApplyFilters={applyIncidentFilters} selectedId={searchTarget?.kind === "incident" ? searchTarget.id : undefined} onOpen={setTimeline} hasMore={incidentsHaveMore} onLoadMore={() => void loadIncidents(incidentCursor)} />}
		{view === "logs" && <LogsView api={api} pageVisible={pageVisible} onError={(value) => showMessage(value, "error")} initialRequestId={searchTarget?.kind === "log" ? searchTarget.id : undefined} initialEvent={searchTarget?.kind === "log" ? searchTarget.detail : undefined} />}
		{view === "captures" && <CapturesView api={api} config={config} pageVisible={pageVisible} onError={(value) => showMessage(value, "error")} onSuccess={showMessage} canOperate={canOperate} canSensitive={canSensitive} confirm={requestConfirmation} selectedId={searchTarget?.kind === "capture" ? searchTarget.id : undefined} />}
        {view === "diagnostics" && <DiagnosticsView runtimeInfo={runtimeInfo} report={diagnostics} busy={diagnosticBusy} run={runDiagnostics} download={downloadDiagnostics} canOperate={canOperate} />}
		{view === "settings" && canOperate && <SettingsView api={api} config={config} baseline={savedConfig} runtimeInfo={runtimeInfo} configState={configState} canSensitive={canSensitive} upstreamStatus={upstreamStatus} governanceStatus={governanceStatus} telemetryStatus={telemetryStatus} confirm={requestConfirmation} setConfig={setConfig} save={save} reload={reload} dirty={dirty} busy={saving} discard={() => setConfig(savedConfig)} themeMode={theme.mode} setThemeMode={theme.setMode} />}
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
