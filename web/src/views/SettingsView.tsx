import { Power, RotateCcw, Save, Undo2, Zap } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ThemeSelector } from "../components/ThemeSelector";
import type { ThemeMode } from "../theme";
import type { Config, RuntimeInfo } from "../types";

type Tab = "general" | "retry" | "traffic" | "continuity" | "safety" | "capture" | "notifications" | "appearance" | "logging";
type Locale = "zh-CN" | "en-US";
type EditableConfigKey = Exclude<keyof Config, "schemaVersion">;
const configTabs: Record<EditableConfigKey, Tab> = {
  server: "general", upstream: "general", localization: "general", retry: "retry", stream: "retry",
  queue: "traffic", history: "traffic", observability: "safety", risk: "safety", capture: "capture",
  notifications: "notifications", logging: "logging", persistence: "continuity", incidents: "continuity",
  lifecycle: "continuity", managementSecurity: "continuity", metricsExport: "continuity",
};

function Toggle({ checked, onChange, label, restart = false }: { checked: boolean; onChange: (value: boolean) => void; label: string; restart?: boolean }) {
  const { t } = useTranslation("settings");
  return <label className="toggle"><input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} /><span className="toggle-track" /><span className="toggle-copy">{label}{restart && <small>{t("restartHint")}</small>}</span></label>;
}

const durationFactors = { ms: 0.001, s: 1, m: 60, h: 3600 } as const;
type DurationUnit = keyof typeof durationFactors;

function durationSeconds(raw: string) {
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

interface Props {
  config: Config;
  setConfig: (value: Config) => void;
  save: () => Promise<void>;
  reload: () => Promise<void>;
  discard: () => void;
  dirty: boolean;
  busy: boolean;
  baseline: Config;
  runtimeInfo: RuntimeInfo | null;
  themeMode: ThemeMode;
  setThemeMode: (mode: ThemeMode) => void;
}

export function SettingsView({ config, setConfig, save, reload, discard, dirty, busy, baseline, runtimeInfo, themeMode, setThemeMode }: Props) {
  const { t } = useTranslation(["settings", "common"]);
  const [tab, setTab] = useState<Tab>("general");
  const patch = <K extends EditableConfigKey>(key: K, value: Partial<Config[K]>) => setConfig({ ...config, [key]: { ...config[key], ...value } });
  const toggleEvent = (eventType: string, enabled: boolean) => patch("notifications", {
    eventTypes: enabled ? Array.from(new Set([...config.notifications.eventTypes, eventType])) : config.notifications.eventTypes.filter((value) => value !== eventType),
  });
  const tabs: Tab[] = ["general", "retry", "traffic", "continuity", "safety", "capture", "notifications", "appearance", "logging"];
  const changedSections = Array.from(new Set((Object.keys(configTabs) as EditableConfigKey[])
    .filter((key) => JSON.stringify(config[key]) !== JSON.stringify(baseline[key]))
    .map((key) => configTabs[key])));
  const restartHint = <small className="field-hint">{t("settings:restartHint")}</small>;

  return <div className="settings-shell">
    <div className="settings-tabs" role="tablist">{tabs.map((value) => <button role="tab" aria-selected={tab === value} className={tab === value ? "active" : ""} key={value} onClick={() => setTab(value)}>{t(`settings:tabs.${value}`)}</button>)}</div>
    {dirty && <div className="unsaved-banner">{t("settings:unsaved")}</div>}
    <div className="settings-stack">
      <div className="settings-apply-legend"><span><Zap size={14} />{t("settings:hotReload")}</span><span><Power size={14} />{t("settings:restartApply")}</span></div>
      {tab === "general" && <>
        <section><div className="section-heading"><div><h2>{t("settings:sections.service.title")}</h2><p>{t("settings:sections.service.description")}</p></div></div><div className="form-grid">
	          <label className="field"><span>{t("settings:fields.listen")}</span><input required value={config.server.listen} onChange={(event) => patch("server", { listen: event.target.value })} />{restartHint}</label>
	          <ByteSizeField label={t("settings:fields.requestBodyLimit")} value={config.server.maxRequestBody} onChange={(maxRequestBody) => patch("server", { maxRequestBody })} />
	          <label className="field wide"><span>{t("settings:fields.configBackupDir")}</span><input value={config.server.configBackupDir} onChange={(event) => patch("server", { configBackupDir: event.target.value })} placeholder={t("settings:fields.configBackupDefault")} /></label>
	          <label className="field wide"><span>{t("settings:fields.upstreamBaseUrl")}</span><input required type="url" value={config.upstream.baseUrl} onChange={(event) => patch("upstream", { baseUrl: event.target.value })} />{restartHint}</label>
          <DurationField restart label={t("settings:fields.connectTimeout")} value={config.upstream.connectTimeout} onChange={(connectTimeout) => patch("upstream", { connectTimeout })} />
          <DurationField restart label={t("settings:fields.responseHeaderTimeout")} value={config.upstream.responseHeaderTimeout} onChange={(responseHeaderTimeout) => patch("upstream", { responseHeaderTimeout })} />
	          <DurationField restart label={t("settings:fields.responseBodyIdleTimeout")} value={config.upstream.responseBodyIdleTimeout} onChange={(responseBodyIdleTimeout) => patch("upstream", { responseBodyIdleTimeout })} />
	        </div></section>
	        <section><div className="section-heading"><div><h2>{t("settings:sections.runtime.title")}</h2><p>{t("settings:sections.runtime.description")}</p></div></div>
	          <dl className="runtime-info"><div><dt>{t("settings:runtime.version")}</dt><dd>{runtimeInfo?.version || t("common:notAvailable")}</dd></div><div><dt>{t("settings:runtime.revision")}</dt><dd><code>{runtimeInfo?.revision || t("common:notAvailable")}</code></dd></div><div><dt>{t("settings:runtime.builtAt")}</dt><dd>{runtimeInfo?.builtAt || t("common:notAvailable")}</dd></div><div><dt>{t("settings:runtime.startedAt")}</dt><dd>{runtimeInfo ? new Date(runtimeInfo.startedAt).toLocaleString() : t("common:notAvailable")}</dd></div><div><dt>{t("settings:runtime.platform")}</dt><dd>{runtimeInfo ? `${runtimeInfo.platform} · ${runtimeInfo.goVersion}` : t("common:notAvailable")}</dd></div><div><dt>{t("settings:runtime.contract")}</dt><dd>{runtimeInfo ? `API v${runtimeInfo.adminApiVersion} · Config v${runtimeInfo.configSchemaVersion}` : `Config v${config.schemaVersion}`}</dd></div>{runtimeInfo?.imageRef && <div><dt>{t("settings:runtime.image")}</dt><dd><code>{runtimeInfo.imageRef}</code></dd></div>}</dl>
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
          <label className="field wide"><span>{t("settings:fields.tempDir")}</span><input value={config.stream.tempDir} onChange={(event) => patch("stream", { tempDir: event.target.value })} placeholder={t("settings:fields.systemDefault")} /></label>
        </div></section>
      </>}
      {tab === "traffic" && <section><div className="section-heading"><div><h2>{t("settings:sections.traffic.title")}</h2><p>{t("settings:sections.traffic.description")}</p></div></div><div className="form-grid">
        <label className="field"><span>{t("settings:fields.maxActive")}</span><input type="number" min="1" value={config.queue.maxActive} onChange={(event) => patch("queue", { maxActive: Number(event.target.value) })} /></label>
        <label className="field"><span>{t("settings:fields.maxWaiting")}</span><input type="number" min="0" value={config.queue.maxWaiting} onChange={(event) => patch("queue", { maxWaiting: Number(event.target.value) })} /></label>
        <DurationField label={t("settings:fields.recoverySpacing")} value={config.queue.recoverySpacing} onChange={(recoverySpacing) => patch("queue", { recoverySpacing })} />
        <label className="field"><span>{t("settings:fields.historyLimit")}</span><input type="number" min="1" value={config.history.maxItems} onChange={(event) => patch("history", { maxItems: Number(event.target.value) })} /></label>
        <DurationField label={t("settings:fields.historyRetention")} value={config.history.retention} onChange={(retention) => patch("history", { retention })} />
      </div></section>}
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
	      <DurationField label={t("settings:fields.maxRequestDuration")} value={config.lifecycle.maxRequestDuration} onChange={(maxRequestDuration) => patch("lifecycle", { maxRequestDuration })} />
	      <label className="field"><span>{t("settings:fields.clientDisconnectPolicy")}</span><select value={config.lifecycle.clientDisconnectPolicy} onChange={(event) => patch("lifecycle", { clientDisconnectPolicy: event.target.value as Config["lifecycle"]["clientDisconnectPolicy"] })}><option value="cancel">{t("settings:fields.cancel")}</option><option value="finish-attempt">{t("settings:fields.finishAttempt")}</option></select></label>
	    </div></section>
	    <section><div className="section-heading"><div><h2>{t("settings:sections.incidents.title")}</h2><p>{t("settings:sections.incidents.description")}</p></div><Toggle label={t("settings:fields.incidentsEnabled")} checked={config.incidents.enabled} onChange={(enabled) => patch("incidents", { enabled })} /></div><div className="form-grid">
	      <DurationField label={t("settings:fields.correlationWindow")} value={config.incidents.correlationWindow} onChange={(correlationWindow) => patch("incidents", { correlationWindow })} />
	      <DurationField label={t("settings:fields.recoveryStableWindow")} value={config.incidents.recoveryStableWindow} onChange={(recoveryStableWindow) => patch("incidents", { recoveryStableWindow })} />
	      <DurationField label={t("settings:fields.incidentRetention")} value={config.incidents.retention} onChange={(retention) => patch("incidents", { retention })} />
	      <label className="field"><span>{t("settings:fields.incidentLimit")}</span><input type="number" min="1" max="100000" value={config.incidents.maxItems} onChange={(event) => patch("incidents", { maxItems: Number(event.target.value) })} /></label>
	    </div></section>
	    <section><div className="section-heading"><div><h2>{t("settings:sections.managementSecurity.title")}</h2><p>{t("settings:sections.managementSecurity.description")}</p></div></div><div className="form-grid">
	      <label className="field"><span>{t("settings:fields.loginFailuresPerMinute")}</span><input type="number" min="1" max="100" value={config.managementSecurity.loginFailuresPerMinute} onChange={(event) => patch("managementSecurity", { loginFailuresPerMinute: Number(event.target.value) })} /></label>
	      <DurationField label={t("settings:fields.loginCooldown")} value={config.managementSecurity.loginCooldown} onChange={(loginCooldown) => patch("managementSecurity", { loginCooldown })} />
	      <DurationField label={t("settings:fields.sessionIdleTimeout")} value={config.managementSecurity.sessionIdleTimeout} onChange={(sessionIdleTimeout) => patch("managementSecurity", { sessionIdleTimeout })} />
	    </div></section>
	    <section><div className="section-heading"><div><h2>{t("settings:sections.metrics.title")}</h2><p>{t("settings:sections.metrics.description")}</p></div><Toggle restart label={t("settings:fields.metricsEnabled")} checked={config.metricsExport.enabled} onChange={(enabled) => patch("metricsExport", { enabled })} /></div><div className="form-grid">
	      <label className="field"><span>{t("settings:fields.metricsPath")}</span><input value={config.metricsExport.path} onChange={(event) => patch("metricsExport", { path: event.target.value })} />{restartHint}</label>
	    </div></section>
	  </>}
	  {tab === "safety" && <>
        <section><div className="section-heading"><div><h2>{t("settings:sections.observability.title")}</h2><p>{t("settings:sections.observability.description")}</p></div></div><div className="form-grid">
          <label className="field"><span>{t("settings:fields.collectionMode")}</span><select value={config.observability.errorDetails} onChange={(event) => patch("observability", { errorDetails: event.target.value as Config["observability"]["errorDetails"] })}><option value="safe">{t("settings:fields.safeExtraction")}</option><option value="off">{t("settings:fields.off")}</option></select></label>
          <ByteSizeField label={t("settings:fields.detailLimit")} value={config.observability.maxErrorDetail} disabled={config.observability.errorDetails === "off"} onChange={(maxErrorDetail) => patch("observability", { maxErrorDetail })} />
        </div></section>
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
      {tab === "notifications" && <section><div className="section-heading"><div><h2>{t("settings:sections.notifications.title")}</h2><p>{t("settings:sections.notifications.description")}</p></div></div><div className="form-grid">
        <DurationField label={t("settings:fields.stalledAfter")} value={config.notifications.stalledAfter} onChange={(stalledAfter) => patch("notifications", { stalledAfter })} />
        <label className="field"><span>{t("settings:fields.deliveryAttempts")}</span><input type="number" min="1" max="10" value={config.notifications.deliveryAttempts} onChange={(event) => patch("notifications", { deliveryAttempts: Number(event.target.value) })} /></label>
        <DurationField label={t("settings:fields.deliveryBackoff")} value={config.notifications.deliveryBackoff} onChange={(deliveryBackoff) => patch("notifications", { deliveryBackoff })} />
        <LocaleField label={t("settings:fields.notificationLocale")} value={config.notifications.locale} onChange={(locale) => patch("notifications", { locale })} />
        <label className="field wide"><span>{t("settings:fields.webhook")}</span><input type="url" value={config.notifications.webhookUrl} onChange={(event) => patch("notifications", { webhookUrl: event.target.value })} placeholder="https://" /></label>
      </div><div className="event-options">{config.notifications.eventTypes.concat(["stalled", "recovered", "long_running", "many_attempts", "auth_errors", "queue_pressure", "disk_pressure"]).filter((value, index, all) => all.indexOf(value) === index).map((eventType) => <Toggle key={eventType} label={t(`settings:events.${eventType}`, { defaultValue: eventType })} checked={config.notifications.eventTypes.includes(eventType)} onChange={(value) => toggleEvent(eventType, value)} />)}</div><Toggle label={t("settings:fields.notifyOnRecovery")} checked={config.notifications.notifyOnRecovery} onChange={(notifyOnRecovery) => patch("notifications", { notifyOnRecovery })} /></section>}
      {tab === "appearance" && <section><div className="section-heading"><div><h2>{t("settings:sections.appearance.title")}</h2><p>{t("settings:sections.appearance.description")}</p></div></div><ThemeSelector mode={themeMode} onChange={setThemeMode} /><dl className="font-stack-summary"><div><dt>{t("settings:fonts.interface")}</dt><dd>Source Sans 3 · Source Han Sans SC</dd></div><div><dt>{t("settings:fonts.technical")}</dt><dd>Source Code Pro</dd></div><div><dt>{t("settings:fonts.delivery")}</dt><dd>{t("settings:fonts.selfHosted")}</dd></div></dl></section>}
      {tab === "logging" && <section><div className="section-heading"><div><h2>{t("settings:sections.logging.title")}</h2><p>{t("settings:sections.logging.description")}</p></div></div><div className="form-grid">
        <label className="field"><span>{t("settings:fields.logLevel")}</span><select value={config.logging.level} onChange={(event) => patch("logging", { level: event.target.value })}><option value="debug">{t("settings:logLevels.debug")}</option><option value="info">{t("settings:logLevels.info")}</option><option value="warn">{t("settings:logLevels.warn")}</option><option value="error">{t("settings:logLevels.error")}</option></select>{restartHint}</label>
        <LocaleField label={t("settings:fields.loggingLocale")} value={config.logging.locale} onChange={(locale) => patch("logging", { locale })} />
      </div></section>}
    </div>
    <div className={`settings-diff${dirty ? " dirty" : ""}`} aria-live="polite"><strong>{dirty ? t("settings:changeSummary", { count: changedSections.length }) : t("settings:noConfigChanges")}</strong>{dirty && <span>{t("settings:changeSections", { sections: changedSections.map((section) => t(`settings:tabs.${section}`, { defaultValue: section })).join(", ") })}</span>}</div>
    <div className="settings-actions"><button className="button" disabled={!dirty || busy} onClick={discard}><Undo2 size={17} />{t("common:actions.discard")}</button><button className="button" disabled={busy} onClick={reload}><RotateCcw size={17} />{t("common:actions.reload")}</button><button className="button primary" disabled={!dirty || busy} onClick={save}><Save size={17} />{busy ? t("common:loading") : t("common:actions.save")}</button></div>
  </div>;
}
