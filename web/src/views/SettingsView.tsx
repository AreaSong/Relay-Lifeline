import { Activity, GitCompareArrows, History, Plus, Power, Rocket, RotateCcw, Save, Send, Trash2, Undo2, Zap } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { errorMessage, type ApiClient } from "../api";
import { ThemeSelector } from "../components/ThemeSelector";
import type { ConfirmDialogState } from "../components/ConfirmDialog";
import type { ThemeMode } from "../theme";
import type { Config, ConfigRuntimeState, ConfigVersion, GovernanceBudgetConfig, GovernanceStatus, NotificationDelivery, NotificationStatus, PolicyDecision, PolicyReleaseRecord, PolicyReleaseStatus, PolicyStatus, RuntimeInfo, TelemetryStatus, UpstreamPoolStatus } from "../types";

type Tab = "general" | "retry" | "traffic" | "policy" | "continuity" | "safety" | "capture" | "notifications" | "appearance" | "logging";
type Locale = "zh-CN" | "en-US";
type EditableConfigKey = Exclude<keyof Config, "schemaVersion">;
const configTabs: Record<EditableConfigKey, Tab> = {
	server: "general", upstream: "general", upstreams: "general", localization: "general", retry: "retry", stream: "retry",
  queue: "traffic", history: "traffic", observability: "safety", risk: "safety", capture: "capture",
  notifications: "notifications", logging: "logging", persistence: "continuity", incidents: "continuity",
	lifecycle: "continuity", managementSecurity: "continuity", metricsExport: "continuity", governance: "traffic",
	egress: "safety", slo: "policy", trafficPolicy: "policy",
};

function Toggle({ checked, onChange, label, restart = false, disabled = false }: { checked: boolean; onChange: (value: boolean) => void; label: string; restart?: boolean; disabled?: boolean }) {
  const { t } = useTranslation("settings");
	return <label className={`toggle${disabled ? " disabled" : ""}`}><input type="checkbox" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} /><span className="toggle-track" /><span className="toggle-copy">{label}{restart && <small>{t("restartHint")}</small>}</span></label>;
}

const durationFactors = { ms: 0.001, s: 1, m: 60, h: 3600 } as const;
type DurationUnit = keyof typeof durationFactors;

function durationSeconds(raw: string) {
  raw = typeof raw === "string" ? raw : "";
  let total = 0;
  let matched = 0;
  const pattern = /(\d+(?:\.\d+)?)(ms|h|m|s)/g;
  for (const result of raw.matchAll(pattern)) {
    total += Number(result[1]) * durationFactors[result[2] as DurationUnit];
    matched += result[0].length;
  }
  return matched === raw.length ? total : 0;
}

function durationParts(raw: string): [number, DurationUnit] {
  const seconds = durationSeconds(raw);
  if (seconds >= 3600 && seconds % 3600 === 0) return [seconds / 3600, "h"];
  if (seconds >= 60 && seconds % 60 === 0) return [seconds / 60, "m"];
  if (seconds >= 1) return [seconds, "s"];
  return [seconds * 1000, "ms"];
}

function DurationField({ label, value, onChange, restart = false }: { label: string; value: string; onChange: (value: string) => void; restart?: boolean }) {
  const { t } = useTranslation("settings");
  const [amount, unit] = durationParts(value);
  const update = (nextAmount: number, nextUnit: DurationUnit) => onChange(`${Math.max(0, nextAmount)}${nextUnit}`);
  return <label className="field"><span>{label}</span><div className="compound-input">
    <input type="number" min="0" step={unit === "ms" ? 100 : 1} value={amount} onChange={(event) => update(Number(event.target.value), unit)} />
    <select aria-label={t("units.unit")} value={unit} onChange={(event) => update(amount, event.target.value as DurationUnit)}>
      <option value="ms">{t("units.milliseconds")}</option><option value="s">{t("units.seconds")}</option><option value="m">{t("units.minutes")}</option><option value="h">{t("units.hours")}</option>
    </select>
  </div>{restart && <small className="field-hint">{t("restartHint")}</small>}</label>;
}

const byteFactors = { B: 1, KiB: 1 << 10, MiB: 1 << 20, GiB: 1 << 30 } as const;
type ByteUnit = keyof typeof byteFactors;

function byteParts(raw: string): [number, ByteUnit] {
  const match = raw.trim().match(/^(\d+(?:\.\d+)?)(B|KiB|MiB|GiB)$/i);
  if (!match) return [0, "MiB"];
  const sourceUnit = Object.keys(byteFactors).find((unit) => unit.toLowerCase() === match[2].toLowerCase()) as ByteUnit;
  const bytes = Number(match[1]) * byteFactors[sourceUnit];
  for (const unit of ["GiB", "MiB", "KiB", "B"] as ByteUnit[]) {
    if (bytes >= byteFactors[unit] && bytes % byteFactors[unit] === 0) return [bytes / byteFactors[unit], unit];
  }
  return [bytes, "B"];
}

function ByteSizeField({ label, value, onChange, disabled = false }: { label: string; value: string; onChange: (value: string) => void; disabled?: boolean }) {
  const { t } = useTranslation("settings");
  const [amount, unit] = byteParts(value);
  const update = (nextAmount: number, nextUnit: ByteUnit) => onChange(`${Math.max(0, nextAmount)}${nextUnit}`);
  return <label className="field"><span>{label}</span><div className="compound-input">
    <input type="number" min="0" value={amount} disabled={disabled} onChange={(event) => update(Number(event.target.value), unit)} />
    <select aria-label={t("units.unit")} value={unit} disabled={disabled} onChange={(event) => update(amount, event.target.value as ByteUnit)}>
      {Object.keys(byteFactors).map((option) => <option value={option} key={option}>{option}</option>)}
    </select>
  </div></label>;
}

function LocaleField({ label, value, onChange }: { label: string; value: Locale; onChange: (value: Locale) => void }) {
  const { t } = useTranslation("common");
  return <label className="field"><span>{label}</span><select value={value} onChange={(event) => onChange(event.target.value as Locale)}>
    <option value="zh-CN">{t("language.zhCN")}</option><option value="en-US">{t("language.enUS")}</option>
  </select></label>;
}

function formatGovernanceUtilization(value: number, limit: number) {
	if (limit <= 0) return "-";
	return `${Math.min(999, value / limit * 100).toFixed(1)}%`;
}

interface Props {
	api: ApiClient;
  config: Config;
  setConfig: (value: Config) => void;
  save: () => Promise<void>;
  reload: () => Promise<void>;
  discard: () => void;
  dirty: boolean;
  busy: boolean;
  baseline: Config;
	runtimeInfo: RuntimeInfo | null;
	configState: ConfigRuntimeState | null;
	canSensitive: boolean;
	upstreamStatus: UpstreamPoolStatus | null;
	governanceStatus: GovernanceStatus | null;
	telemetryStatus: TelemetryStatus | null;
	confirm: (state: ConfirmDialogState) => Promise<boolean>;
  themeMode: ThemeMode;
  setThemeMode: (mode: ThemeMode) => void;
}

export function SettingsView({ api, config, setConfig, save, reload, discard, dirty, busy, baseline, runtimeInfo, configState, canSensitive, upstreamStatus, governanceStatus, telemetryStatus, confirm, themeMode, setThemeMode }: Props) {
	const { t, i18n } = useTranslation(["settings", "common"]);
  const [tab, setTab] = useState<Tab>("general");
	const [notificationStatus, setNotificationStatus] = useState<NotificationStatus | null>(null);
	const [notificationDeliveries, setNotificationDeliveries] = useState<NotificationDelivery[]>([]);
	const [notificationBusy, setNotificationBusy] = useState(false);
	const [notificationError, setNotificationError] = useState("");
	const [configVersions, setConfigVersions] = useState<ConfigVersion[]>([]);
	const [configHistoryError, setConfigHistoryError] = useState("");
	const [rollbackBusy, setRollbackBusy] = useState("");
	const [policyStatus, setPolicyStatus] = useState<PolicyStatus | null>(null);
	const [policyReleases, setPolicyReleases] = useState<PolicyReleaseStatus | null>(null);
	const [policyDecision, setPolicyDecision] = useState<PolicyDecision | null>(null);
	const [policyError, setPolicyError] = useState("");
	const [policyBusy, setPolicyBusy] = useState("");
	const [canaryPercent, setCanaryPercent] = useState(10);
	const [simulator, setSimulator] = useState({ method: "POST", path: "/v1/responses", model: "", principal: "anonymous" });
	const patch = <K extends EditableConfigKey>(key: K, value: Partial<Config[K]>) => setConfig({ ...config, [key]: { ...config[key], ...value } });
	const patchOIDC = (value: Partial<Config["managementSecurity"]["oidc"]>) => patch("managementSecurity", { oidc: { ...config.managementSecurity.oidc, ...value } });
	const setTargets = (targets: Config["upstreams"]["targets"]) => patch("upstreams", { targets });
	const patchTarget = (index: number, value: Partial<Config["upstreams"]["targets"][number]>) => setTargets(config.upstreams.targets.map((target, targetIndex) => targetIndex === index ? { ...target, ...value } : target));
	const patchTrafficPolicy = (value: Partial<Config["trafficPolicy"]>) => patch("trafficPolicy", value);
	const patchRule = (index: number, value: Partial<Config["trafficPolicy"]["rules"][number]>) => patchTrafficPolicy({ rules: config.trafficPolicy.rules.map((rule, ruleIndex) => ruleIndex === index ? { ...rule, ...value } : rule) });
	const governanceBudgets = config.governance.budgets || [];
	const setGovernanceBudgets = (budgets: GovernanceBudgetConfig[]) => patch("governance", { budgets });
	const patchGovernanceBudget = (index: number, value: Partial<GovernanceBudgetConfig>) => setGovernanceBudgets(governanceBudgets.map((budget, budgetIndex) => budgetIndex === index ? { ...budget, ...value } : budget));
	const addGovernanceBudget = () => setGovernanceBudgets([...governanceBudgets, { scope: "tenant", key: "", maxConcurrent: 0, requestsPerMinute: 0, tokenLimit: 0, costLimitMicros: 0 }]);
	const targetIds = config.upstreams.targets.length ? config.upstreams.targets.map((target) => target.id) : ["primary"];
	const governanceEntries = governanceStatus?.entries || [];
	const policyDirty = JSON.stringify(config.trafficPolicy) !== JSON.stringify(baseline.trafficPolicy);
	const tenantRuntimeEntries = governanceEntries.filter((entry) => entry.scope === "tenant").length;
	const governanceRuntime = useMemo(() => {
		// 主体条目对应全局限制；维度条目是附加视图，不能再次累加。
		const measuredEntries = governanceEntries.filter((entry) => !entry.scope || entry.scope === "principal");
		const totals = (measuredEntries.length ? measuredEntries : governanceEntries).reduce((result, entry) => ({
			active: result.active + (entry.active || 0),
			requests: result.requests + (entry.requests || 0),
			tokens: result.tokens + (entry.tokens || 0),
			costMicros: result.costMicros + (entry.costMicros || 0),
			unknownUsage: result.unknownUsage + (entry.unknownUsage || 0),
		}), { active: 0, requests: 0, tokens: 0, costMicros: 0, unknownUsage: 0 });
		const limits = {
			active: Number(config.governance.maxConcurrent || 0),
			requests: Number(config.governance.requestsPerMinute || 0),
			tokens: Number(config.governance.tokenLimit || 0),
			costMicros: Number(config.governance.costLimitMicros || 0),
		};
		return { totals, limits };
	}, [config.governance, governanceEntries]);
  const toggleEvent = (eventType: string, enabled: boolean) => patch("notifications", {
    eventTypes: enabled ? Array.from(new Set([...config.notifications.eventTypes, eventType])) : config.notifications.eventTypes.filter((value) => value !== eventType),
  });
  const tabs: Tab[] = ["general", "retry", "traffic", "policy", "continuity", "safety", "capture", "notifications", "appearance", "logging"];
  const changedSections = Array.from(new Set((Object.keys(configTabs) as EditableConfigKey[])
    .filter((key) => JSON.stringify(config[key]) !== JSON.stringify(baseline[key]))
    .map((key) => configTabs[key])));
  const restartHint = <small className="field-hint">{t("settings:restartHint")}</small>;
	async function refreshNotifications() {
		try {
			const [status, deliveries] = await Promise.all([api.notificationStatus(), api.notificationDeliveries()]);
			setNotificationStatus(status);
			setNotificationDeliveries(deliveries);
			setNotificationError("");
		} catch (reason) {
			setNotificationError(errorMessage(reason));
		}
	}
	useEffect(() => {
		if (tab === "continuity") void refreshConfigVersions();
	}, [api, tab]);
	useEffect(() => {
		if (tab !== "notifications") return;
		void refreshNotifications();
		const timer = window.setInterval(() => void refreshNotifications(), 10_000);
		return () => window.clearInterval(timer);
	}, [api, tab]);
	useEffect(() => {
		if (tab !== "policy") return;
		void Promise.all([api.policyStatus(), api.policyReleases()]).then(([status, releases]) => { setPolicyStatus(status); setPolicyReleases(releases); }).catch((reason) => setPolicyError(errorMessage(reason)));
	}, [api, tab]);
	async function refreshConfigVersions() {
		try { setConfigVersions((await api.configVersions()).items); setConfigHistoryError(""); }
		catch (reason) { setConfigHistoryError(errorMessage(reason)); }
	}
	async function rollbackConfig(version: ConfigVersion) {
		const authenticationChange = version.applyPlan.fields?.some((field) => field.path === "management-security.authentication") || false;
		const fields = version.diff.fields?.map((field) => field.path).join(", ") || version.diff.changedSections.join(", ") || t("common:notAvailable");
		const accepted = await confirm({
			title: t("settings:history.confirmTitle"),
			description: `${t(authenticationChange ? "settings:history.authConfirm" : "settings:history.confirm", { time: new Date(version.modifiedAt).toLocaleString(i18n.resolvedLanguage) })}\n${fields}`,
			confirmLabel: t("settings:history.rollback"), tone: "danger",
		});
		if (!accepted) return;
		setRollbackBusy(version.name); setConfigHistoryError("");
		try { await api.rollbackConfig(version, authenticationChange); await reload(); await refreshConfigVersions(); }
		catch (reason) { setConfigHistoryError(errorMessage(reason)); }
		finally { setRollbackBusy(""); }
	}
	async function sendTestNotification() {
		setNotificationBusy(true);
		try {
			await api.testNotification();
			await refreshNotifications();
		} catch (reason) {
			setNotificationError(errorMessage(reason));
		} finally {
			setNotificationBusy(false);
		}
	}
	async function refreshPolicy() {
		const [status, releases] = await Promise.all([api.policyStatus(), api.policyReleases()]);
		setPolicyStatus(status); setPolicyReleases(releases);
	}
	async function savePolicyDraft() {
		setPolicyBusy("draft"); setPolicyError("");
		try { await api.savePolicyDraft(config.trafficPolicy, policyReleases?.draftRevision); await refreshPolicy(); }
		catch (reason) { setPolicyError(errorMessage(reason)); }
		finally { setPolicyBusy(""); }
	}
	async function publishPolicy(stage: "shadow" | "canary" | "full") {
		if (!configState) return;
		const accepted = await confirm({ title: t("settings:policy.publishConfirmTitle"), description: t(`settings:policy.publishConfirm.${stage}`, { percent: canaryPercent }), confirmLabel: t(`settings:policy.publish.${stage}`), tone: stage === "full" ? "danger" : "default" });
		if (!accepted) return;
		setPolicyBusy(stage); setPolicyError("");
		try {
			await api.publishPolicy({ configRevision: configState.desiredRevision, draftRevision: policyReleases?.draftRevision, stage, canaryPercent: stage === "canary" ? canaryPercent : 100, policy: policyReleases?.draftRevision ? undefined : config.trafficPolicy });
			await reload(); await refreshPolicy();
		} catch (reason) { setPolicyError(errorMessage(reason)); }
		finally { setPolicyBusy(""); }
	}
	async function rollbackPolicyRelease(release: PolicyReleaseRecord) {
		if (!configState) return;
		const accepted = await confirm({ title: t("settings:policy.rollbackConfirmTitle"), description: t("settings:policy.rollbackConfirm", { revision: release.revision, time: new Date(release.createdAt).toLocaleString(i18n.resolvedLanguage) }), confirmLabel: t("settings:policy.rollback"), tone: "danger" });
		if (!accepted) return;
		setPolicyBusy(release.revision); setPolicyError("");
		try { await api.rollbackPolicy(configState.desiredRevision, release.revision); await reload(); await refreshPolicy(); }
		catch (reason) { setPolicyError(errorMessage(reason)); }
		finally { setPolicyBusy(""); }
	}
	async function simulatePolicy() {
		setPolicyError("");
		try {
			setPolicyDecision(await api.simulatePolicy({ ...simulator, sloHealthy: true, errorBudgetRemaining: 1 }, policyReleases?.draftRevision ? "draft" : "active"));
			setPolicyStatus(await api.policyStatus());
		} catch (reason) {
			setPolicyError(errorMessage(reason));
		}
	}

	return <div className="settings-shell">
    <div className="settings-tabs" role="tablist">{tabs.map((value) => <button role="tab" aria-selected={tab === value} className={tab === value ? "active" : ""} key={value} onClick={() => setTab(value)}>{t(`settings:tabs.${value}`)}</button>)}</div>
		{dirty && <div className="unsaved-banner">{policyDirty ? t("settings:policy.unsavedHint") : t("settings:unsaved")}</div>}
		{configState?.pendingRestart.restartRequired && <div className="pending-restart-banner" role="status"><strong>{t("settings:pendingRestart.title")}</strong><span>{t("settings:pendingRestart.revisions", { active: configState.activeRevision, desired: configState.desiredRevision })}</span><small>{configState.pendingRestart.fields?.filter((field) => field.applyMode === "restart").map((field) => field.path).join(", ") || configState.pendingRestart.restartSections.join(", ")}</small></div>}
    <div className="settings-stack">
      <div className="settings-apply-legend"><span><Zap size={14} />{t("settings:hotReload")}</span><span><Power size={14} />{t("settings:restartApply")}</span></div>
      {tab === "general" && <>
        <section><div className="section-heading"><div><h2>{t("settings:sections.service.title")}</h2><p>{t("settings:sections.service.description")}</p></div></div><div className="form-grid">
	          <label className="field"><span>{t("settings:fields.listen")}</span><input required value={config.server.listen} onChange={(event) => patch("server", { listen: event.target.value })} />{restartHint}</label>
	          <ByteSizeField label={t("settings:fields.requestBodyLimit")} value={config.server.maxRequestBody} onChange={(maxRequestBody) => patch("server", { maxRequestBody })} />
	          <label className="field wide"><span>{t("settings:fields.configBackupDir")}</span><input value={config.server.configBackupDir} onChange={(event) => patch("server", { configBackupDir: event.target.value })} placeholder={t("settings:fields.configBackupDefault")} /></label>
		          <label className="field wide"><span>{t("settings:fields.upstreamBaseUrl")}</span><input required type="url" value={config.upstream.baseUrl} onChange={(event) => patch("upstream", { baseUrl: event.target.value })} /></label>
	          <DurationField label={t("settings:fields.connectTimeout")} value={config.upstream.connectTimeout} onChange={(connectTimeout) => patch("upstream", { connectTimeout })} />
	          <DurationField label={t("settings:fields.responseHeaderTimeout")} value={config.upstream.responseHeaderTimeout} onChange={(responseHeaderTimeout) => patch("upstream", { responseHeaderTimeout })} />
				  <DurationField label={t("settings:fields.responseBodyIdleTimeout")} value={config.upstream.responseBodyIdleTimeout} onChange={(responseBodyIdleTimeout) => patch("upstream", { responseBodyIdleTimeout })} />
				</div></section>
				<section><div className="section-heading"><div><h2>{t("settings:sections.upstreamPool.title")}</h2><p>{t("settings:sections.upstreamPool.description")}</p></div><button className="button compact" type="button" onClick={() => setTargets([...config.upstreams.targets, { id: `relay-${config.upstreams.targets.length + 1}`, baseUrl: config.upstream.baseUrl, priority: 0, weight: 1, maxActive: 0, idempotencyDomain: "default", costMicrosPer1K: 0, capabilityScore: 1 }])}><Plus size={15} />{t("settings:upstreams.add")}</button></div><label className="field"><span>{t("settings:upstreams.strategy")}</span><select value={config.upstreams.strategy} onChange={(event) => patch("upstreams", { strategy: event.target.value as Config["upstreams"]["strategy"] })}><option value="primary-only">{t("settings:upstreams.primaryOnly")}</option><option value="weighted-priority">{t("settings:upstreams.weightedPriority")}</option></select></label><div className="upstream-target-list">{config.upstreams.targets.length === 0 ? <p className="subtle">{t("settings:upstreams.legacy")}</p> : config.upstreams.targets.map((target, index) => <div className="upstream-target-row" key={`${target.id}-${index}`}><input aria-label={t("settings:upstreams.id")} value={target.id} onChange={(event) => patchTarget(index, { id: event.target.value })} /><input aria-label={t("settings:upstreams.url")} type="url" value={target.baseUrl} onChange={(event) => patchTarget(index, { baseUrl: event.target.value })} /><input aria-label={t("settings:upstreams.priority")} type="number" min="0" value={target.priority} onChange={(event) => patchTarget(index, { priority: Number(event.target.value) })} /><input aria-label={t("settings:upstreams.weight")} type="number" min="0" value={target.weight} onChange={(event) => patchTarget(index, { weight: Number(event.target.value) })} /><input aria-label={t("settings:upstreams.maxActive")} type="number" min="0" value={target.maxActive} onChange={(event) => patchTarget(index, { maxActive: Number(event.target.value) })} /><input aria-label={t("settings:upstreams.domain")} value={target.idempotencyDomain} onChange={(event) => patchTarget(index, { idempotencyDomain: event.target.value })} /><input aria-label={t("settings:upstreams.cost")} type="number" min="0" value={target.costMicrosPer1K} onChange={(event) => patchTarget(index, { costMicrosPer1K: Number(event.target.value) })} /><input aria-label={t("settings:upstreams.capability")} type="number" min="0" max="1" step="0.05" value={target.capabilityScore} onChange={(event) => patchTarget(index, { capabilityScore: Number(event.target.value) })} /><button className="icon-button" type="button" aria-label={t("common:actions.delete")} data-tooltip={t("common:actions.delete")} onClick={() => setTargets(config.upstreams.targets.filter((_, targetIndex) => targetIndex !== index))}><Trash2 size={15} /></button></div>)}</div>{upstreamStatus && <div className="upstream-runtime-status" aria-live="polite">{upstreamStatus.targets.map((item) => <div key={item.target.id}><strong>{item.target.id}</strong><span className={`status ${item.circuitState}`}>{t(`settings:upstreams.circuit.${item.circuitState}`)}</span><span>{t("settings:upstreams.active", { count: item.active })}</span><span>{t("settings:upstreams.results", { success: item.successCount, failed: item.failureCount })}</span>{item.lastLatencyMilliseconds != null && <span>{item.lastLatencyMilliseconds} ms</span>}</div>)}</div>}</section>
	        <section><div className="section-heading"><div><h2>{t("settings:sections.runtime.title")}</h2><p>{t("settings:sections.runtime.description")}</p></div></div>
		          <dl className="runtime-info"><div><dt>{t("settings:runtime.version")}</dt><dd>{runtimeInfo?.version || t("common:notAvailable")}</dd></div><div><dt>{t("settings:runtime.revision")}</dt><dd><code>{runtimeInfo?.revision || t("common:notAvailable")}</code></dd></div><div><dt>{t("settings:runtime.builtAt")}</dt><dd>{runtimeInfo?.builtAt || t("common:notAvailable")}</dd></div><div><dt>{t("settings:runtime.startedAt")}</dt><dd>{runtimeInfo ? new Date(runtimeInfo.startedAt).toLocaleString(i18n.resolvedLanguage) : t("common:notAvailable")}</dd></div><div><dt>{t("settings:runtime.platform")}</dt><dd>{runtimeInfo ? `${runtimeInfo.platform} · ${runtimeInfo.goVersion}` : t("common:notAvailable")}</dd></div><div><dt>{t("settings:runtime.contract")}</dt><dd>{runtimeInfo ? `API v${runtimeInfo.adminApiVersion} · Config v${runtimeInfo.configSchemaVersion}` : `Config v${config.schemaVersion}`}</dd></div>{runtimeInfo?.imageRef && <div><dt>{t("settings:runtime.image")}</dt><dd><code>{runtimeInfo.imageRef}</code></dd></div>}</dl>
	        </section>
        <section><div className="section-heading"><div><h2>{t("settings:sections.localization.title")}</h2><p>{t("settings:sections.localization.description")}</p></div></div><div className="form-grid">
          <LocaleField label={t("settings:fields.defaultLocale")} value={config.localization.defaultLocale} onChange={(defaultLocale) => patch("localization", { defaultLocale })} />
          <LocaleField label={t("settings:fields.fallbackLocale")} value={config.localization.fallbackLocale} onChange={(fallbackLocale) => patch("localization", { fallbackLocale })} />
        </div></section>
      </>}
      {tab === "retry" && <>
        <section><div className="section-heading"><div><h2>{t("settings:sections.retry.title")}</h2><p>{t("settings:sections.retry.description")}</p></div><Toggle label={t("settings:fields.enableRetry")} checked={config.retry.enabled} onChange={(enabled) => patch("retry", { enabled })} /></div><div className="form-grid">
          <label className="field"><span>{t("settings:fields.errorScope")}</span><select value={config.retry.mode} onChange={(event) => patch("retry", { mode: event.target.value as Config["retry"]["mode"] })}><option value="all-errors">{t("settings:fields.allErrors")}</option><option value="transient-errors">{t("settings:fields.transientErrors")}</option></select></label>
          <label className="field"><span>{t("settings:fields.maxAttempts")}</span><input type="number" min="0" value={config.retry.maxAttempts} onChange={(event) => patch("retry", { maxAttempts: Number(event.target.value) })} /></label>
          <DurationField label={t("settings:fields.minWait")} value={config.retry.minInterval} onChange={(minInterval) => patch("retry", { minInterval })} />
          <DurationField label={t("settings:fields.maxWait")} value={config.retry.maxInterval} onChange={(maxInterval) => patch("retry", { maxInterval })} />
        </div><Toggle label={t("settings:fields.honorRetryAfter")} checked={config.retry.honorRetryAfter} onChange={(honorRetryAfter) => patch("retry", { honorRetryAfter })} /></section>
        <section><div className="section-heading"><div><h2>{t("settings:sections.stream.title")}</h2><p>{t("settings:sections.stream.description")}</p></div></div><div className="form-grid">
          <DurationField label={t("settings:fields.heartbeat")} value={config.stream.heartbeatInterval} onChange={(heartbeatInterval) => patch("stream", { heartbeatInterval })} />
          <ByteSizeField label={t("settings:fields.memoryLimit")} value={config.stream.memoryLimit} onChange={(memoryLimit) => patch("stream", { memoryLimit })} />
          <ByteSizeField label={t("settings:fields.maxResponseBody")} value={config.stream.maxResponseBody} onChange={(maxResponseBody) => patch("stream", { maxResponseBody })} />
          <ByteSizeField label={t("settings:fields.maxTotalCache")} value={config.stream.maxTotalCache} onChange={(maxTotalCache) => patch("stream", { maxTotalCache })} />
          <label className="field wide"><span>{t("settings:fields.tempDir")}</span><input value={config.stream.tempDir} onChange={(event) => patch("stream", { tempDir: event.target.value })} placeholder={t("settings:fields.systemDefault")} /></label>
        </div></section>
      </>}
		  {tab === "traffic" && <><section><div className="section-heading"><div><h2>{t("settings:sections.traffic.title")}</h2><p>{t("settings:sections.traffic.description")}</p></div></div><div className="form-grid">
        <label className="field"><span>{t("settings:fields.maxActive")}</span><input type="number" min="1" value={config.queue.maxActive} onChange={(event) => patch("queue", { maxActive: Number(event.target.value) })} /></label>
        <label className="field"><span>{t("settings:fields.maxWaiting")}</span><input type="number" min="0" value={config.queue.maxWaiting} onChange={(event) => patch("queue", { maxWaiting: Number(event.target.value) })} /></label>
        <DurationField label={t("settings:fields.recoverySpacing")} value={config.queue.recoverySpacing} onChange={(recoverySpacing) => patch("queue", { recoverySpacing })} />
        <label className="field"><span>{t("settings:fields.historyLimit")}</span><input type="number" min="1" value={config.history.maxItems} onChange={(event) => patch("history", { maxItems: Number(event.target.value) })} /></label>
        <DurationField label={t("settings:fields.historyRetention")} value={config.history.retention} onChange={(retention) => patch("history", { retention })} />
		  </div></section><section><div className="section-heading"><div><h2>{t("settings:sections.governance.title")}</h2><p>{t("settings:sections.governance.description")}</p></div></div><div className="form-grid"><label className="field"><span>{t("settings:governance.mode")}</span><select value={config.governance.mode} onChange={(event) => patch("governance", { mode: event.target.value as Config["governance"]["mode"] })}><option value="observe">{t("settings:governance.observe")}</option><option value="enforce">{t("settings:governance.enforce")}</option></select></label><label className="field"><span>{t("settings:governance.unknownUsagePolicy")}</span><select value={config.governance.unknownUsagePolicy} onChange={(event) => patch("governance", { unknownUsagePolicy: event.target.value as Config["governance"]["unknownUsagePolicy"] })}><option value="observe">{t("settings:governance.unknownObserve")}</option><option value="deny">{t("settings:governance.unknownDeny")}</option></select></label><label className="field"><span>{t("settings:governance.concurrent")}</span><input type="number" min="0" value={config.governance.maxConcurrent} onChange={(event) => patch("governance", { maxConcurrent: Number(event.target.value) })} /></label><label className="field"><span>{t("settings:governance.rpm")}</span><input type="number" min="0" value={config.governance.requestsPerMinute} onChange={(event) => patch("governance", { requestsPerMinute: Number(event.target.value) })} /></label><label className="field"><span>{t("settings:governance.tokens")}</span><input type="number" min="0" value={config.governance.tokenLimit} onChange={(event) => patch("governance", { tokenLimit: Number(event.target.value) })} /></label><label className="field"><span>{t("settings:governance.cost")}</span><input type="number" min="0" value={config.governance.costLimitMicros} onChange={(event) => patch("governance", { costLimitMicros: Number(event.target.value) })} /></label><label className="field"><span>{t("settings:governance.tokenReservation")}</span><input type="number" min="0" value={config.governance.tokenReservation} onChange={(event) => patch("governance", { tokenReservation: Number(event.target.value) })} /></label><label className="field"><span>{t("settings:governance.costReservation")}</span><input type="number" min="0" value={config.governance.costReservationMicros} onChange={(event) => patch("governance", { costReservationMicros: Number(event.target.value) })} /></label><label className="field"><span>{t("settings:governance.reservationMinTokens")}</span><input type="number" min="0" value={config.governance.reservationMinTokens} onChange={(event) => patch("governance", { reservationMinTokens: Number(event.target.value) })} /></label><label className="field"><span>{t("settings:governance.reservationMaxTokens")}</span><input type="number" min="0" value={config.governance.reservationMaxTokens} onChange={(event) => patch("governance", { reservationMaxTokens: Number(event.target.value) })} /></label><label className="field"><span>{t("settings:governance.reservationMinCost")}</span><input type="number" min="0" value={config.governance.reservationMinCostMicros} onChange={(event) => patch("governance", { reservationMinCostMicros: Number(event.target.value) })} /></label><label className="field"><span>{t("settings:governance.reservationMaxCost")}</span><input type="number" min="0" value={config.governance.reservationMaxCostMicros} onChange={(event) => patch("governance", { reservationMaxCostMicros: Number(event.target.value) })} /></label><label className="field"><span>{t("settings:governance.softThresholdPercent")}</span><input type="number" min="0" max="100" value={config.governance.softThresholdPercent} onChange={(event) => patch("governance", { softThresholdPercent: Number(event.target.value) })} /></label><DurationField label={t("settings:governance.forecastWindow")} value={config.governance.forecastWindow} onChange={(forecastWindow) => patch("governance", { forecastWindow })} /></div><div className="governance-budget-heading"><div><strong>{t("settings:governance.budgetsTitle")}</strong><p>{t("settings:governance.budgetsDescription")}</p></div><button className="button compact" type="button" onClick={addGovernanceBudget}><Plus size={15} />{t("settings:governance.addBudget")}</button></div><div className="governance-budget-list">{governanceBudgets.length === 0 ? <p className="subtle">{t("settings:governance.noBudgets")}</p> : governanceBudgets.map((budget, index) => <div className="governance-budget-row" key={`${budget.scope}-${budget.key}-${index}`}><select aria-label={t("settings:governance.scope")} value={budget.scope} onChange={(event) => patchGovernanceBudget(index, { scope: event.target.value })}><option value="principal">{t("settings:governance.scopePrincipal")}</option><option value="tenant">{t("settings:governance.scopeTenant")}</option><option value="model">{t("settings:governance.scopeModel")}</option><option value="upstream">{t("settings:governance.scopeUpstream")}</option></select><input aria-label={t("settings:governance.key")} value={budget.key} placeholder={t("settings:governance.keyPlaceholder")} onChange={(event) => patchGovernanceBudget(index, { key: event.target.value })} /><input aria-label={t("settings:governance.budgetConcurrent")} type="number" min="0" value={budget.maxConcurrent} onChange={(event) => patchGovernanceBudget(index, { maxConcurrent: Number(event.target.value) })} /><input aria-label={t("settings:governance.budgetRpm")} type="number" min="0" value={budget.requestsPerMinute} onChange={(event) => patchGovernanceBudget(index, { requestsPerMinute: Number(event.target.value) })} /><input aria-label={t("settings:governance.budgetTokens")} type="number" min="0" value={budget.tokenLimit} onChange={(event) => patchGovernanceBudget(index, { tokenLimit: Number(event.target.value) })} /><input aria-label={t("settings:governance.budgetCost")} type="number" min="0" value={budget.costLimitMicros} onChange={(event) => patchGovernanceBudget(index, { costLimitMicros: Number(event.target.value) })} /><button className="icon-button" type="button" aria-label={t("common:actions.delete")} data-tooltip={t("common:actions.delete")} onClick={() => setGovernanceBudgets(governanceBudgets.filter((_, budgetIndex) => budgetIndex !== index))}><Trash2 size={15} /></button></div>)}</div><div className="governance-tenant-context"><div><strong>{t("settings:governance.tenantContext")}</strong><p>{t("settings:governance.tenantContextDescription")}</p><small>{t("settings:governance.tenantRuntimeEntries", { count: tenantRuntimeEntries })}</small></div><span className={`status ${governanceBudgets.some((budget) => budget.scope === "tenant") ? "healthy" : "neutral"}`}>{governanceBudgets.some((budget) => budget.scope === "tenant") ? t("settings:governance.tenantConfigured") : t("settings:governance.tenantNotConfigured")}</span></div>{governanceStatus && <><dl className="operational-health"><div><dt>{t("settings:governance.runtimePrincipals")}</dt><dd>{governanceStatus.principals}</dd></div><div><dt>{t("settings:governance.runtimeReservations")}</dt><dd>{governanceStatus.reservations}</dd></div><div><dt>{t("settings:governance.settlements")}</dt><dd>{governanceStatus.counters.settlements}</dd></div><div><dt>{t("settings:governance.unknownSettlements")}</dt><dd>{governanceStatus.counters.unknownSettlements}</dd></div><div><dt>{t("settings:governance.persistenceFailures")}</dt><dd>{governanceStatus.counters.persistenceFailures}</dd></div><div><dt>{t("settings:governance.ledger")}</dt><dd className={governanceStatus.ledger.healthy ? "success-text" : "danger-text"}>{governanceStatus.ledger.healthy ? t("settings:health.healthy") : t("settings:health.degraded")}</dd></div><div><dt>{t("settings:governance.softThreshold")}</dt><dd className={governanceStatus.softThreshold ? "danger-text" : "success-text"}>{governanceStatus.softThreshold ? t("settings:governance.softThresholdActive") : t("settings:governance.softThresholdClear")}</dd></div><div><dt>{t("settings:governance.forecast")}</dt><dd>{governanceStatus.estimatedExhaustionMinutes && governanceStatus.estimatedExhaustionMinutes > 0 ? t("settings:governance.forecastMinutes", { minutes: governanceStatus.estimatedExhaustionMinutes.toFixed(1) }) : t("settings:governance.forecastUnavailable")}</dd></div></dl><div className="governance-runtime-summary"><strong>{t("settings:governance.runtimeUtilization")}</strong><div className="governance-utilization-grid">{(["active", "requests", "tokens", "costMicros"] as const).map((key) => <div key={key}><span>{t(`settings:governance.utilization.${key}`)}</span><strong>{formatGovernanceUtilization(governanceRuntime.totals[key], governanceRuntime.limits[key])}</strong><small>{governanceRuntime.totals[key].toLocaleString(i18n.resolvedLanguage)} / {governanceRuntime.limits[key] > 0 ? governanceRuntime.limits[key].toLocaleString(i18n.resolvedLanguage) : "--"}</small></div>)}</div></div><div className="governance-entry-list">{governanceEntries.length === 0 ? <p className="subtle">{t("settings:governance.noRuntimeEntries")}</p> : governanceEntries.map((entry) => <div className="governance-entry-row" key={`${entry.scope}-${entry.key}-${entry.windowStarted}`}><strong>{`${entry.scope || "principal"}:${entry.key || entry.principal || t("common:notAvailable")}`}</strong><span>{t("settings:governance.entryActive", { count: entry.active })}</span><span>{t("settings:governance.entryRequests", { count: entry.requests })}</span><span>{(entry.tokens || 0).toLocaleString(i18n.resolvedLanguage)} {t("settings:governance.entryTokens")}</span><span>{(entry.costMicros || 0).toLocaleString(i18n.resolvedLanguage)} {t("settings:governance.entryCost")}</span></div>)}</div></>}</section></>}
		{tab === "policy" && <>
			<section><div className="section-heading"><div><h2>{t("settings:sections.slo.title")}</h2><p>{t("settings:sections.slo.description")}</p></div><Toggle label={t("settings:policy.sloEnabled")} checked={config.slo.enabled} onChange={(enabled) => patch("slo", { enabled })} /></div><div className="form-grid"><label className="field"><span>{t("settings:policy.availabilityTarget")}</span><input type="number" min="0" max="1" step="0.001" value={config.slo.availabilityTarget} onChange={(event) => patch("slo", { availabilityTarget: Number(event.target.value) })} /></label><DurationField label={t("settings:policy.recoveryTarget")} value={config.slo.recoveryLatencyTarget} onChange={(recoveryLatencyTarget) => patch("slo", { recoveryLatencyTarget })} /><DurationField label={t("settings:policy.sloWindow")} value={config.slo.window} onChange={(window) => patch("slo", { window })} /></div></section>
			<section className="policy-release-panel"><div className="section-heading"><div><h2>{t("settings:sections.policyRelease.title")}</h2><p>{t("settings:sections.policyRelease.description")}</p></div><span className={`status ${policyStatus?.adaptiveStopped ? "degraded" : "healthy"}`}>{t(`settings:policy.stage.${policyReleases?.currentStage || config.trafficPolicy.releaseStage}`)}</span></div>
				<div className="policy-release-actions"><button className="button compact" disabled={!!policyBusy} type="button" onClick={() => void savePolicyDraft()}><Save size={15} />{policyBusy === "draft" ? t("settings:policy.saving") : t("settings:policy.saveDraft")}</button><button className="button compact" disabled={!!policyBusy} type="button" onClick={() => void publishPolicy("shadow")}><GitCompareArrows size={15} />{t("settings:policy.publish.shadow")}</button><label className="field compact-field"><span>{t("settings:policy.canaryPercent")}</span><input type="number" min="1" max="100" value={canaryPercent} onChange={(event) => setCanaryPercent(Number(event.target.value))} /></label><button className="button compact" disabled={!!policyBusy} type="button" onClick={() => void publishPolicy("canary")}><Rocket size={15} />{t("settings:policy.publish.canary")}</button><button className="button compact danger" disabled={!!policyBusy} type="button" onClick={() => void publishPolicy("full")}><Rocket size={15} />{t("settings:policy.publish.full")}</button></div>
				<dl className="operational-health"><div><dt>{t("settings:policy.activeRevision")}</dt><dd><code>{policyReleases?.currentRevision || config.trafficPolicy.revision || t("common:notAvailable")}</code></dd></div><div><dt>{t("settings:policy.draftRevision")}</dt><dd><code>{policyReleases?.draftRevision || t("common:notAvailable")}</code></dd></div><div><dt>{t("settings:policy.adaptiveState")}</dt><dd className={policyStatus?.adaptiveStopped ? "danger-text" : "success-text"}>{policyStatus?.adaptiveStopped ? t("settings:policy.adaptiveStopped", { reason: policyStatus.adaptiveStopReason }) : t("settings:policy.adaptiveRunning")}</dd></div></dl>
			</section>
			<section><div className="section-heading"><div><h2>{t("settings:sections.policy.title")}</h2><p>{t("settings:sections.policy.description")}</p></div><Toggle label={t("settings:policy.enabled")} checked={config.trafficPolicy.enabled} onChange={(enabled) => patchTrafficPolicy({ enabled })} /></div><div className="form-grid"><label className="field"><span>{t("settings:policy.mode")}</span><select value={config.trafficPolicy.mode} onChange={(event) => patchTrafficPolicy({ mode: event.target.value as Config["trafficPolicy"]["mode"] })}><option value="observe">{t("settings:governance.observe")}</option><option value="enforce">{t("settings:governance.enforce")}</option></select></label></div><div className="policy-rule-list">{config.trafficPolicy.rules.map((rule, index) => <div className="policy-rule-row" key={`${rule.id}-${index}`}><Toggle label={rule.id || t("settings:policy.newRule")} checked={rule.enabled} onChange={(enabled) => patchRule(index, { enabled })} /><input aria-label={t("settings:policy.ruleId")} value={rule.id} onChange={(event) => patchRule(index, { id: event.target.value })} /><input aria-label={t("settings:policy.pathPrefix")} value={rule.pathPrefix} onChange={(event) => patchRule(index, { pathPrefix: event.target.value })} /><input aria-label={t("settings:policy.model")} value={rule.model} onChange={(event) => patchRule(index, { model: event.target.value })} /><select aria-label={t("settings:policy.action")} value={rule.action} onChange={(event) => patchRule(index, { action: event.target.value as "route" | "deny" })}><option value="route">{t("settings:policy.route")}</option><option value="deny">{t("settings:policy.deny")}</option></select><select aria-label={t("settings:policy.target")} value={rule.targetId} disabled={rule.action === "deny"} onChange={(event) => patchRule(index, { targetId: event.target.value })}>{targetIds.map((id) => <option key={id}>{id}</option>)}</select><button type="button" className="icon-button" aria-label={t("common:actions.delete")} onClick={() => patchTrafficPolicy({ rules: config.trafficPolicy.rules.filter((_, ruleIndex) => ruleIndex !== index) })}><Trash2 size={15} /></button></div>)}</div><button className="button compact" type="button" onClick={() => patchTrafficPolicy({ rules: [...config.trafficPolicy.rules, { id: `policy-${config.trafficPolicy.rules.length + 1}`, enabled: true, priority: config.trafficPolicy.rules.length, method: "POST", pathPrefix: "/v1/", model: "", principalPrefix: "", action: "route", targetId: targetIds[0] }] })}><Plus size={15} />{t("settings:policy.addRule")}</button></section>
			<section><div className="section-heading"><div><h2>{t("settings:sections.shadow.title")}</h2><p>{t("settings:sections.shadow.description")}</p></div><Toggle label={t("settings:policy.shadowEnabled")} checked={config.trafficPolicy.shadow.enabled} onChange={(enabled) => patchTrafficPolicy({ shadow: { ...config.trafficPolicy.shadow, enabled } })} /></div><div className="form-grid"><label className="field"><span>{t("settings:policy.target")}</span><select value={config.trafficPolicy.shadow.targetId} onChange={(event) => patchTrafficPolicy({ shadow: { ...config.trafficPolicy.shadow, targetId: event.target.value } })}>{targetIds.map((id) => <option key={id}>{id}</option>)}</select></label><label className="field"><span>{t("settings:policy.samplePercent")}</span><input type="number" min="0" max="100" value={config.trafficPolicy.shadow.samplePercent} onChange={(event) => patchTrafficPolicy({ shadow: { ...config.trafficPolicy.shadow, samplePercent: Number(event.target.value) } })} /></label><label className="field"><span>{t("settings:policy.shadowConcurrent")}</span><input type="number" min="1" value={config.trafficPolicy.shadow.maxConcurrent} onChange={(event) => patchTrafficPolicy({ shadow: { ...config.trafficPolicy.shadow, maxConcurrent: Number(event.target.value) } })} /></label><ByteSizeField label={t("settings:policy.shadowBodyLimit")} value={config.trafficPolicy.shadow.maxRequestBody} onChange={(maxRequestBody) => patchTrafficPolicy({ shadow: { ...config.trafficPolicy.shadow, maxRequestBody } })} /><label className="field"><span>{t("settings:policy.requestBudget")}</span><input type="number" min="0" value={config.trafficPolicy.shadow.requestBudgetPerHour} onChange={(event) => patchTrafficPolicy({ shadow: { ...config.trafficPolicy.shadow, requestBudgetPerHour: Number(event.target.value) } })} /></label><label className="field"><span>{t("settings:policy.costBudget")}</span><input type="number" min="0" value={config.trafficPolicy.shadow.costBudgetMicrosPerHour} onChange={(event) => patchTrafficPolicy({ shadow: { ...config.trafficPolicy.shadow, costBudgetMicrosPerHour: Number(event.target.value) } })} /></label><label className="field"><span>{t("settings:policy.costReservation")}</span><input type="number" min="0" value={config.trafficPolicy.shadow.costReservationMicros} onChange={(event) => patchTrafficPolicy({ shadow: { ...config.trafficPolicy.shadow, costReservationMicros: Number(event.target.value) } })} /></label><Toggle label={t("settings:policy.requireIdempotency")} checked={config.trafficPolicy.shadow.requireIdempotency} onChange={(requireIdempotency) => patchTrafficPolicy({ shadow: { ...config.trafficPolicy.shadow, requireIdempotency } })} /></div></section>
			<section><div className="section-heading"><div><h2>{t("settings:sections.adaptive.title")}</h2><p>{t("settings:sections.adaptive.description")}</p></div><Toggle label={t("settings:policy.adaptiveEnabled")} checked={config.trafficPolicy.adaptive.enabled} onChange={(enabled) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, enabled } })} /></div><div className="form-grid">
				<label className="field"><span>{t("settings:policy.errorBudgetFloor")}</span><input type="number" min="0" max="1" step="0.05" value={config.trafficPolicy.adaptive.errorBudgetFloor} onChange={(event) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, errorBudgetFloor: Number(event.target.value) } })} /></label>
				<label className="field"><span>{t("settings:policy.minimumObservations")}</span><input type="number" min="1" value={config.trafficPolicy.adaptive.minimumObservations} onChange={(event) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, minimumObservations: Number(event.target.value) } })} /></label>
				<label className="field"><span>{t("settings:policy.maximumLatency")}</span><input type="number" min="1" value={config.trafficPolicy.adaptive.maximumLatencyMilliseconds} onChange={(event) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, maximumLatencyMilliseconds: Number(event.target.value) } })} /></label>
				<label className="field"><span>{t("settings:policy.latencyWeight")}</span><input type="number" min="0" step="0.05" value={config.trafficPolicy.adaptive.latencyWeight} onChange={(event) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, latencyWeight: Number(event.target.value) } })} /></label>
				<label className="field"><span>{t("settings:policy.errorWeight")}</span><input type="number" min="0" step="0.05" value={config.trafficPolicy.adaptive.errorRateWeight} onChange={(event) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, errorRateWeight: Number(event.target.value) } })} /></label>
				<label className="field"><span>{t("settings:policy.costWeight")}</span><input type="number" min="0" step="0.05" value={config.trafficPolicy.adaptive.costWeight} onChange={(event) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, costWeight: Number(event.target.value) } })} /></label>
				<label className="field"><span>{t("settings:policy.capabilityWeight")}</span><input type="number" min="0" step="0.05" value={config.trafficPolicy.adaptive.capabilityWeight} onChange={(event) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, capabilityWeight: Number(event.target.value) } })} /></label>
				<DurationField label={t("settings:policy.switchCooldown")} value={config.trafficPolicy.adaptive.switchCooldown} onChange={(switchCooldown) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, switchCooldown } })} />
				<label className="field"><span>{t("settings:policy.fallbackTarget")}</span><select value={config.trafficPolicy.adaptive.fallbackTargetId} onChange={(event) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, fallbackTargetId: event.target.value } })}><option value="">{t("settings:policy.default")}</option>{targetIds.map((id) => <option key={id}>{id}</option>)}</select></label>
				<label className="field"><span>{t("settings:policy.autoStopBurnRate")}</span><input type="number" min="0" step="0.1" value={config.trafficPolicy.adaptive.autoStopBurnRate} onChange={(event) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, autoStopBurnRate: Number(event.target.value) } })} /></label>
				<label className="field"><span>{t("settings:policy.autoStopFailureRate")}</span><input type="number" min="0" max="1" step="0.05" value={config.trafficPolicy.adaptive.autoStopFailureRate} onChange={(event) => patchTrafficPolicy({ adaptive: { ...config.trafficPolicy.adaptive, autoStopFailureRate: Number(event.target.value) } })} /></label>
			</div></section>
			<section><div className="section-heading"><div><h2>{t("settings:sections.simulator.title")}</h2><p>{t("settings:sections.simulator.description")}</p></div><button className="button compact" type="button" onClick={() => void simulatePolicy()}><Zap size={15} />{t("settings:policy.simulate")}</button></div><div className="form-grid"><label className="field"><span>{t("settings:policy.method")}</span><select value={simulator.method} onChange={(event) => setSimulator({ ...simulator, method: event.target.value })}><option>POST</option><option>GET</option><option>DELETE</option></select></label><label className="field wide"><span>{t("settings:policy.path")}</span><input value={simulator.path} onChange={(event) => setSimulator({ ...simulator, path: event.target.value })} /></label><label className="field"><span>{t("settings:policy.model")}</span><input value={simulator.model} onChange={(event) => setSimulator({ ...simulator, model: event.target.value })} /></label><label className="field"><span>{t("settings:policy.principal")}</span><input value={simulator.principal} onChange={(event) => setSimulator({ ...simulator, principal: event.target.value })} /></label></div>{policyError && <div className="error-banner">{policyError}</div>}{policyDecision && <><dl className="operational-health"><div><dt>{t("settings:policy.result")}</dt><dd>{policyDecision.denied ? t("settings:policy.deny") : policyDecision.targetId || t("settings:policy.default")}</dd></div><div><dt>{t("settings:policy.matchedRule")}</dt><dd>{policyDecision.matchedRuleId || t("common:notAvailable")}</dd></div><div><dt>{t("settings:policy.enforced")}</dt><dd>{policyDecision.enforced ? t("settings:governance.enforce") : t("settings:governance.observe")}</dd></div><div><dt>{t("settings:policy.shadow")}</dt><dd>{policyDecision.shadowEligible ? policyDecision.shadowTargetId : t("settings:policy.notEligible")}</dd></div><div><dt>{t("settings:policy.score")}</dt><dd>{policyDecision.adaptiveScore?.toFixed(4) || t("common:notAvailable")}</dd></div></dl><ol className="decision-explanation">{policyDecision.explanation?.map((item) => <li key={item}>{item}</li>)}</ol></>}{policyStatus && <p className="subtle">{t("settings:policy.runtimeSummary", { decisions: policyStatus.decisions, routed: policyStatus.routed, denied: policyStatus.denied, shadows: policyStatus.shadowSent })}</p>}</section>
			<section><div className="section-heading"><div><h2>{t("settings:sections.policyHistory.title")}</h2><p>{t("settings:sections.policyHistory.description")}</p></div></div><div className="config-history-list">{!policyReleases?.history.length ? <p className="subtle">{t("settings:policy.noHistory")}</p> : [...policyReleases.history].reverse().map((release) => <article className="config-history-item" key={`${release.revision}-${release.createdAt}`}><div><strong>{t(`settings:policy.stage.${release.stage}`)} · <code>{release.revision}</code></strong><span>{new Date(release.createdAt).toLocaleString(i18n.resolvedLanguage)} · {release.actor || t("common:notAvailable")}</span></div><button className="button compact" type="button" disabled={!!policyBusy || release.revision === policyReleases.currentRevision} onClick={() => void rollbackPolicyRelease(release)}><Undo2 size={15} />{t("settings:policy.rollback")}</button></article>)}</div></section>
		</>}
	  {tab === "continuity" && <>
	    <section><div className="section-heading"><div><h2>{t("settings:sections.persistence.title")}</h2><p>{t("settings:sections.persistence.description")}</p></div><Toggle restart label={t("settings:fields.persistenceEnabled")} checked={config.persistence.enabled} onChange={(enabled) => patch("persistence", { enabled })} /></div><div className="form-grid">
	      <label className="field wide"><span>{t("settings:fields.persistenceDirectory")}</span><input value={config.persistence.directory} onChange={(event) => patch("persistence", { directory: event.target.value })} />{restartHint}</label>
	      <DurationField restart label={t("settings:fields.persistenceRetention")} value={config.persistence.retention} onChange={(retention) => patch("persistence", { retention })} />
	      <Toggle restart label={t("settings:fields.syncWrites")} checked={config.persistence.syncWrites} onChange={(syncWrites) => patch("persistence", { syncWrites })} />
	    </div></section>
	    <section><div className="section-heading"><div><h2>{t("settings:sections.lifecycle.title")}</h2><p>{t("settings:sections.lifecycle.description")}</p></div></div><div className="form-grid">
	      <Toggle label={t("settings:fields.trackUncertainDelivery")} checked={config.lifecycle.trackUncertainDelivery} onChange={(trackUncertainDelivery) => patch("lifecycle", { trackUncertainDelivery })} />
	      <Toggle label={t("settings:fields.preserveIdempotencyKey")} checked={config.lifecycle.preserveIdempotencyKey} onChange={(preserveIdempotencyKey) => patch("lifecycle", { preserveIdempotencyKey })} />
	      <Toggle label={t("settings:fields.generateIdempotencyKey")} checked={config.lifecycle.generateIdempotencyKey} onChange={(generateIdempotencyKey) => patch("lifecycle", { generateIdempotencyKey })} />
	      <Toggle label={t("settings:fields.allowUncertainRetry")} checked={config.lifecycle.allowUncertainRetry} onChange={(allowUncertainRetry) => patch("lifecycle", { allowUncertainRetry })} />
	      <Toggle label={t("settings:fields.allowCrossDomainFailover")} checked={config.lifecycle.allowCrossDomainFailover} onChange={(allowCrossDomainFailover) => patch("lifecycle", { allowCrossDomainFailover })} />
	      <DurationField label={t("settings:fields.maxRequestDuration")} value={config.lifecycle.maxRequestDuration} onChange={(maxRequestDuration) => patch("lifecycle", { maxRequestDuration })} />
	      <DurationField label={t("settings:fields.uncertainResolutionTarget")} value={config.lifecycle.uncertainResolutionTarget} onChange={(uncertainResolutionTarget) => patch("lifecycle", { uncertainResolutionTarget })} />
	      <label className="field"><span>{t("settings:fields.clientDisconnectPolicy")}</span><select value={config.lifecycle.clientDisconnectPolicy} onChange={(event) => patch("lifecycle", { clientDisconnectPolicy: event.target.value as Config["lifecycle"]["clientDisconnectPolicy"] })}><option value="cancel">{t("settings:fields.cancel")}</option><option value="finish-attempt">{t("settings:fields.finishAttempt")}</option></select></label>
	    </div></section>
	    <section><div className="section-heading"><div><h2>{t("settings:sections.incidents.title")}</h2><p>{t("settings:sections.incidents.description")}</p></div><Toggle label={t("settings:fields.incidentsEnabled")} checked={config.incidents.enabled} onChange={(enabled) => patch("incidents", { enabled })} /></div><div className="form-grid">
	      <DurationField label={t("settings:fields.correlationWindow")} value={config.incidents.correlationWindow} onChange={(correlationWindow) => patch("incidents", { correlationWindow })} />
	      <DurationField label={t("settings:fields.recoveryStableWindow")} value={config.incidents.recoveryStableWindow} onChange={(recoveryStableWindow) => patch("incidents", { recoveryStableWindow })} />
	      <DurationField label={t("settings:fields.incidentRetention")} value={config.incidents.retention} onChange={(retention) => patch("incidents", { retention })} />
	      <label className="field"><span>{t("settings:fields.incidentLimit")}</span><input type="number" min="1" max="100000" value={config.incidents.maxItems} onChange={(event) => patch("incidents", { maxItems: Number(event.target.value) })} /></label>
	    </div></section>
			<section><div className="section-heading"><div><h2>{t("settings:sections.managementSecurity.title")}</h2><p>{t("settings:sections.managementSecurity.description")}</p></div></div><div className="form-grid">
			  <Toggle restart disabled={!canSensitive} label={t("settings:fields.localAccessEnabled")} checked={config.managementSecurity.localAccessEnabled} onChange={(localAccessEnabled) => patch("managementSecurity", { localAccessEnabled })} />
	      <label className="field"><span>{t("settings:fields.loginFailuresPerMinute")}</span><input type="number" min="1" max="100" value={config.managementSecurity.loginFailuresPerMinute} onChange={(event) => patch("managementSecurity", { loginFailuresPerMinute: Number(event.target.value) })} /></label>
	      <DurationField label={t("settings:fields.loginCooldown")} value={config.managementSecurity.loginCooldown} onChange={(loginCooldown) => patch("managementSecurity", { loginCooldown })} />
			  <DurationField label={t("settings:fields.sessionIdleTimeout")} value={config.managementSecurity.sessionIdleTimeout} onChange={(sessionIdleTimeout) => patch("managementSecurity", { sessionIdleTimeout })} />
			  <DurationField label={t("settings:fields.sessionMaxLifetime")} value={config.managementSecurity.sessionMaxLifetime} onChange={(sessionMaxLifetime) => patch("managementSecurity", { sessionMaxLifetime })} />
			  <Toggle restart disabled={!canSensitive} label={t("settings:fields.oidcEnabled")} checked={config.managementSecurity.oidc.enabled} onChange={(enabled) => patchOIDC({ enabled })} />
			  <label className="field wide"><span>{t("settings:fields.oidcIssuer")}</span><input disabled={!canSensitive} type="url" value={config.managementSecurity.oidc.issuerUrl} onChange={(event) => patchOIDC({ issuerUrl: event.target.value })} />{restartHint}</label>
			  <label className="field"><span>{t("settings:fields.oidcClientId")}</span><input disabled={!canSensitive} value={config.managementSecurity.oidc.clientId} onChange={(event) => patchOIDC({ clientId: event.target.value })} />{restartHint}</label>
			  <label className="field wide"><span>{t("settings:fields.oidcRedirect")}</span><input disabled={!canSensitive} type="url" value={config.managementSecurity.oidc.redirectUrl} onChange={(event) => patchOIDC({ redirectUrl: event.target.value })} />{restartHint}</label>
			  <label className="field"><span>{t("settings:fields.oidcRoleClaim")}</span><input disabled={!canSensitive} value={config.managementSecurity.oidc.roleClaim} onChange={(event) => patchOIDC({ roleClaim: event.target.value })} />{restartHint}</label>
			  {(["viewerValues", "operatorValues", "sensitiveValues"] as const).map((key) => <label className="field" key={key}><span>{t(`settings:fields.${key}`)}</span><input disabled={!canSensitive} value={config.managementSecurity.oidc[key].join(", ")} onChange={(event) => patchOIDC({ [key]: event.target.value.split(",").map((value) => value.trim()).filter(Boolean) })} />{restartHint}</label>)}
			</div>{!canSensitive && <p className="field-hint">{t("settings:oidc.sensitiveRequired")}</p>}</section>
	    <section><div className="section-heading"><div><h2>{t("settings:sections.metrics.title")}</h2><p>{t("settings:sections.metrics.description")}</p></div><Toggle restart label={t("settings:fields.metricsEnabled")} checked={config.metricsExport.enabled} onChange={(enabled) => patch("metricsExport", { enabled })} /></div><div className="form-grid">
	      <label className="field"><span>{t("settings:fields.metricsPath")}</span><input value={config.metricsExport.path} onChange={(event) => patch("metricsExport", { path: event.target.value })} />{restartHint}</label>
	    </div></section>
		<section><div className="section-heading"><div><h2>{t("settings:sections.configHistory.title")}</h2><p>{t("settings:sections.configHistory.description")}</p></div><button className="icon-button" type="button" aria-label={t("common:actions.refresh")} data-tooltip={t("common:actions.refresh")} onClick={() => void refreshConfigVersions()}><RotateCcw size={16} /></button></div>
			{configHistoryError && <div className="error-banner">{configHistoryError}</div>}
			{dirty && <div className="warning-banner">{t("settings:history.unsavedBlocked")}</div>}
			{configVersions.length === 0 ? <div className="empty-state compact"><History size={20} />{t("settings:history.empty")}</div> : <div className="config-version-list">{configVersions.map((version) => { const authenticationChange = version.applyPlan.fields?.some((field) => field.path === "management-security.authentication") || false; return <article key={version.name}><div><strong>{new Date(version.modifiedAt).toLocaleString(i18n.resolvedLanguage)}</strong><code>{version.sha256?.slice(0, 12)}</code><small>{t("settings:history.schema", { version: version.schemaVersion })} · {t("settings:history.size", { size: version.sizeBytes })}</small></div><div className="config-version-diff"><span>{t("settings:history.changed")}</span><strong>{version.diff.changedSections.join(", ") || t("settings:history.noChanges")}</strong><small>{version.applyPlan.restartRequired ? t("settings:history.restartRequired") : t("settings:history.hotReload")}</small></div><button className="button compact" type="button" disabled={dirty || !version.valid || !version.sha256 || rollbackBusy !== "" || authenticationChange && !canSensitive} onClick={() => void rollbackConfig(version)}><History size={15} />{rollbackBusy === version.name ? t("common:loading") : t("settings:history.rollback")}</button></article>; })}</div>}
		</section>
	  </>}
		  {tab === "safety" && <>
			<section><div className="section-heading"><div><h2>{t("settings:sections.egress.title")}</h2><p>{t("settings:sections.egress.description")}</p></div><Toggle label={t("settings:fields.denyPrivateNetworks")} checked={config.egress.denyPrivateNetworks} onChange={(denyPrivateNetworks) => patch("egress", { denyPrivateNetworks })} /></div><label className="field wide"><span>{t("settings:fields.allowedHosts")}</span><input value={config.egress.allowedHosts.join(", ")} onChange={(event) => patch("egress", { allowedHosts: event.target.value.split(",").map((value) => value.trim()).filter(Boolean) })} /></label></section>
        <section><div className="section-heading"><div><h2>{t("settings:sections.observability.title")}</h2><p>{t("settings:sections.observability.description")}</p></div></div><div className="form-grid">
          <label className="field"><span>{t("settings:fields.collectionMode")}</span><select value={config.observability.errorDetails} onChange={(event) => patch("observability", { errorDetails: event.target.value as Config["observability"]["errorDetails"] })}><option value="safe">{t("settings:fields.safeExtraction")}</option><option value="off">{t("settings:fields.off")}</option></select></label>
          <ByteSizeField label={t("settings:fields.detailLimit")} value={config.observability.maxErrorDetail} disabled={config.observability.errorDetails === "off"} onChange={(maxErrorDetail) => patch("observability", { maxErrorDetail })} />
        </div></section>
		<section><div className="section-heading"><div><h2>{t("settings:sections.telemetry.title")}</h2><p>{t("settings:sections.telemetry.description")}</p></div><Toggle restart label={t("settings:fields.telemetryEnabled")} checked={config.observability.telemetry.enabled} onChange={(enabled) => patch("observability", { telemetry: { ...config.observability.telemetry, enabled } })} /></div><div className="form-grid">
		  <label className="field"><span>{t("settings:fields.telemetryProtocol")}</span><select value={config.observability.telemetry.protocol} onChange={(event) => patch("observability", { telemetry: { ...config.observability.telemetry, protocol: event.target.value as Config["observability"]["telemetry"]["protocol"] } })}><option value="grpc">gRPC</option><option value="http/protobuf">HTTP/Protobuf</option><option value="stdout">stdout</option></select>{restartHint}</label>
		  <label className="field wide"><span>{t("settings:fields.telemetryEndpoint")}</span><input disabled={config.observability.telemetry.protocol === "stdout"} value={config.observability.telemetry.endpoint} onChange={(event) => patch("observability", { telemetry: { ...config.observability.telemetry, endpoint: event.target.value } })} />{restartHint}</label>
		  <Toggle restart label={t("settings:fields.telemetryInsecure")} checked={config.observability.telemetry.insecure} onChange={(insecure) => patch("observability", { telemetry: { ...config.observability.telemetry, insecure } })} />
		  <label className="field"><span>{t("settings:fields.telemetrySampleRatio")}</span><input type="number" min="0" max="1" step="0.05" value={config.observability.telemetry.sampleRatio} onChange={(event) => patch("observability", { telemetry: { ...config.observability.telemetry, sampleRatio: Number(event.target.value) } })} />{restartHint}</label>
		  <label className="field"><span>{t("settings:fields.telemetryServiceName")}</span><input value={config.observability.telemetry.serviceName} onChange={(event) => patch("observability", { telemetry: { ...config.observability.telemetry, serviceName: event.target.value } })} />{restartHint}</label>
		  <label className="field"><span>{t("settings:fields.telemetryEnvironment")}</span><input value={config.observability.telemetry.environment} onChange={(event) => patch("observability", { telemetry: { ...config.observability.telemetry, environment: event.target.value } })} />{restartHint}</label>
		  <DurationField restart label={t("settings:fields.telemetryExportTimeout")} value={config.observability.telemetry.exportTimeout} onChange={(exportTimeout) => patch("observability", { telemetry: { ...config.observability.telemetry, exportTimeout } })} />
		  <DurationField restart label={t("settings:fields.telemetryMetricInterval")} value={config.observability.telemetry.metricInterval} onChange={(metricInterval) => patch("observability", { telemetry: { ...config.observability.telemetry, metricInterval } })} />
		</div>{telemetryStatus && <dl className="operational-health"><div><dt>{t("settings:telemetry.runtime")}</dt><dd className={telemetryStatus.healthy ? "success-text" : "danger-text"}>{telemetryStatus.enabled ? telemetryStatus.healthy ? t("settings:health.healthy") : t("settings:health.degraded") : t("settings:health.disabled")}</dd></div><div><dt>{t("settings:telemetry.traces")}</dt><dd>{telemetryStatus.traceHealthy ? t("settings:health.healthy") : t("settings:health.degraded")}</dd></div><div><dt>{t("settings:telemetry.metrics")}</dt><dd>{telemetryStatus.metricHealthy ? t("settings:health.healthy") : t("settings:health.degraded")}</dd></div><div><dt>{t("settings:telemetry.traceFailures")}</dt><dd>{telemetryStatus.traceExportFailures}</dd></div><div><dt>{t("settings:telemetry.metricFailures")}</dt><dd>{telemetryStatus.metricExportFailures}</dd></div><div><dt>{t("settings:telemetry.lastSuccess")}</dt><dd>{telemetryStatus.lastSuccessAt ? new Date(telemetryStatus.lastSuccessAt).toLocaleString(i18n.resolvedLanguage) : t("common:notAvailable")}</dd></div></dl>}</section>
        <section><div className="section-heading"><div><h2>{t("settings:sections.risk.title")}</h2><p>{t("settings:sections.risk.description")}</p></div></div><div className="form-grid">
          <DurationField label={t("settings:fields.warningAfter")} value={config.risk.warningAfter} onChange={(warningAfter) => patch("risk", { warningAfter })} />
          <label className="field"><span>{t("settings:fields.warningAttempts")}</span><input type="number" min="1" value={config.risk.warningAttempts} onChange={(event) => patch("risk", { warningAttempts: Number(event.target.value) })} /></label>
          <label className="field"><span>{t("settings:fields.authErrorAttempts")}</span><input type="number" min="1" value={config.risk.authErrorAttempts} onChange={(event) => patch("risk", { authErrorAttempts: Number(event.target.value) })} /></label>
          <label className="field"><span>{t("settings:fields.queueWarningPercent")}</span><input type="number" min="1" max="100" value={config.risk.queueWarningPercent} onChange={(event) => patch("risk", { queueWarningPercent: Number(event.target.value) })} /></label>
          <ByteSizeField label={t("settings:fields.minimumFreeDisk")} value={config.risk.minimumFreeDisk} onChange={(minimumFreeDisk) => patch("risk", { minimumFreeDisk })} />
        </div></section>
	  </>}
	  {tab === "capture" && <section><div className="section-heading"><div><h2>{t("settings:sections.capture.title")}</h2><p>{t("settings:sections.capture.description")}</p></div><Toggle label={t("settings:fields.captureOnStartup")} checked={config.capture.enabled} onChange={(enabled) => patch("capture", { enabled })} /></div><div className="form-grid">
		<label className="field wide"><span>{t("settings:fields.captureStorageDir")}</span><input value={config.capture.storageDir} onChange={(event) => patch("capture", { storageDir: event.target.value })} />{restartHint}</label>
		<DurationField label={t("settings:fields.captureRetention")} value={config.capture.retention} onChange={(retention) => patch("capture", { retention })} />
		<label className="field"><span>{t("settings:fields.captureRequestLimit")}</span><input type="number" min="1" max="100" value={config.capture.defaultRequestLimit} onChange={(event) => patch("capture", { defaultRequestLimit: Number(event.target.value) })} /></label>
		<DurationField label={t("settings:fields.captureActivationTimeout")} value={config.capture.activationTimeout} onChange={(activationTimeout) => patch("capture", { activationTimeout })} />
		<ByteSizeField label={t("settings:fields.captureBodyLimit")} value={config.capture.maxBodySize} onChange={(maxBodySize) => patch("capture", { maxBodySize })} />
		<ByteSizeField label={t("settings:fields.captureTotalLimit")} value={config.capture.maxTotalSize} onChange={(maxTotalSize) => patch("capture", { maxTotalSize })} />
		<label className="field"><span>{t("settings:fields.captureAttemptLimit")}</span><input type="number" min="1" max="1000" value={config.capture.maxAttemptsPerRequest} onChange={(event) => patch("capture", { maxAttemptsPerRequest: Number(event.target.value) })} /></label>
		<ByteSizeField label={t("settings:fields.captureMinimumDisk")} value={config.capture.minimumFreeDisk} onChange={(minimumFreeDisk) => patch("capture", { minimumFreeDisk })} />
		<label className="field"><span>{t("settings:fields.runtimeLogLimit")}</span><input type="number" min="100" max="100000" value={config.capture.logMaxItems} onChange={(event) => patch("capture", { logMaxItems: Number(event.target.value) })} /></label>
		<DurationField label={t("settings:fields.runtimeLogRetention")} value={config.capture.logRetention} onChange={(logRetention) => patch("capture", { logRetention })} />
	  </div></section>}
      {tab === "notifications" && <section><div className="section-heading"><div><h2>{t("settings:sections.notifications.title")}</h2><p>{t("settings:sections.notifications.description")}</p></div><button className="button compact" disabled={notificationBusy || !notificationStatus?.configured || !notificationStatus.signingConfigured} onClick={() => void sendTestNotification()}><Send size={15} />{t("settings:notifications.test")}</button></div><div className="form-grid">
        <DurationField label={t("settings:fields.stalledAfter")} value={config.notifications.stalledAfter} onChange={(stalledAfter) => patch("notifications", { stalledAfter })} />
        <label className="field"><span>{t("settings:fields.deliveryAttempts")}</span><input type="number" min="1" max="10" value={config.notifications.deliveryAttempts} onChange={(event) => patch("notifications", { deliveryAttempts: Number(event.target.value) })} /></label>
        <DurationField label={t("settings:fields.deliveryBackoff")} value={config.notifications.deliveryBackoff} onChange={(deliveryBackoff) => patch("notifications", { deliveryBackoff })} />
        <LocaleField label={t("settings:fields.notificationLocale")} value={config.notifications.locale} onChange={(locale) => patch("notifications", { locale })} />
        <label className="field wide"><span>{t("settings:fields.webhook")}</span><input type="url" value={config.notifications.webhookUrl} onChange={(event) => patch("notifications", { webhookUrl: event.target.value })} placeholder="https://" /></label>
      </div><div className="event-options">{config.notifications.eventTypes.concat(["stalled", "recovered", "long_running", "many_attempts", "auth_errors", "queue_pressure", "disk_pressure"]).filter((value, index, all) => all.indexOf(value) === index).map((eventType) => <Toggle key={eventType} label={t(`settings:events.${eventType}`, { defaultValue: eventType })} checked={config.notifications.eventTypes.includes(eventType)} onChange={(value) => toggleEvent(eventType, value)} />)}</div><Toggle label={t("settings:fields.notifyOnRecovery")} checked={config.notifications.notifyOnRecovery} onChange={(notifyOnRecovery) => patch("notifications", { notifyOnRecovery })} />
		<div className="notification-operations" aria-live="polite">
			{notificationError && <div className="error-banner">{notificationError}</div>}
				{notificationStatus && <dl className="notification-health"><div><dt>{t("settings:notifications.queue")}</dt><dd>{notificationStatus.queueDepth} / {notificationStatus.queueCapacity}</dd></div><div><dt>{t("settings:notifications.delivered")}</dt><dd>{notificationStatus.delivered}</dd></div><div><dt>{t("settings:notifications.failed")}</dt><dd>{notificationStatus.failed}</dd></div><div><dt>{t("settings:notifications.dropped")}</dt><dd>{notificationStatus.dropped}</dd></div><div><dt>{t("settings:notifications.lastFailure")}</dt><dd>{notificationStatus.lastFailureAt ? new Date(notificationStatus.lastFailureAt).toLocaleString(i18n.resolvedLanguage) : t("common:notAvailable")}</dd></div><div><dt>{t("settings:notifications.signing")}</dt><dd>{notificationStatus.signingConfigured ? t("settings:notifications.signingConfigured") : t("settings:notifications.signingMissing")}{notificationStatus.signingKeyId && <small className="subtle">{notificationStatus.signingKeyId}</small>}</dd></div></dl>}
				{notificationDeliveries.length > 0 && <div className="notification-deliveries">{notificationDeliveries.slice(0, 10).map((delivery) => <div key={delivery.id}><Activity size={14} /><strong>{delivery.eventType}</strong><span className={`status ${delivery.outcome}`}>{t(`settings:notifications.outcome.${delivery.outcome}`)}</span><time>{new Date(delivery.completedAt).toLocaleString(i18n.resolvedLanguage)}</time></div>)}</div>}
		</div></section>}
      {tab === "appearance" && <section><div className="section-heading"><div><h2>{t("settings:sections.appearance.title")}</h2><p>{t("settings:sections.appearance.description")}</p></div></div><ThemeSelector mode={themeMode} onChange={setThemeMode} /><dl className="font-stack-summary"><div><dt>{t("settings:fonts.interface")}</dt><dd>Source Sans 3 · Source Han Sans SC</dd></div><div><dt>{t("settings:fonts.technical")}</dt><dd>Source Code Pro</dd></div><div><dt>{t("settings:fonts.delivery")}</dt><dd>{t("settings:fonts.selfHosted")}</dd></div></dl></section>}
      {tab === "logging" && <section><div className="section-heading"><div><h2>{t("settings:sections.logging.title")}</h2><p>{t("settings:sections.logging.description")}</p></div></div><div className="form-grid">
        <label className="field"><span>{t("settings:fields.logLevel")}</span><select value={config.logging.level} onChange={(event) => patch("logging", { level: event.target.value })}><option value="debug">{t("settings:logLevels.debug")}</option><option value="info">{t("settings:logLevels.info")}</option><option value="warn">{t("settings:logLevels.warn")}</option><option value="error">{t("settings:logLevels.error")}</option></select>{restartHint}</label>
        <LocaleField label={t("settings:fields.loggingLocale")} value={config.logging.locale} onChange={(locale) => patch("logging", { locale })} />
      </div></section>}
    </div>
    <div className={`settings-diff${dirty ? " dirty" : ""}`} aria-live="polite"><strong>{dirty ? t("settings:changeSummary", { count: changedSections.length }) : t("settings:noConfigChanges")}</strong>{dirty && <span>{t("settings:changeSections", { sections: changedSections.map((section) => t(`settings:tabs.${section}`, { defaultValue: section })).join(", ") })}</span>}{policyDirty && <small className="policy-draft-hint">{t("settings:policy.draftHint")}</small>}</div>
    <div className="settings-actions"><button className="button" disabled={!dirty || busy} onClick={discard}><Undo2 size={17} />{t("common:actions.discard")}</button><button className="button" disabled={busy} onClick={reload}><RotateCcw size={17} />{t("common:actions.reload")}</button><button className="button primary" disabled={!dirty || (changedSections.length > 0 && !changedSections.some((section) => section !== "policy")) || busy} onClick={save}><Save size={17} />{busy ? t("common:loading") : t("common:actions.save")}</button></div>
  </div>;
}
