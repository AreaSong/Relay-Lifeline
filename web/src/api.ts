import type { Alert, BatchActionResponse, CaptureKeyRewrapResult, CaptureKeyStatus, CapturePreview, CaptureRecord, CaptureStatus, Config, ConfigChangePlan, ConfigRuntimeState, ConfigSaveResult, ConfigVersion, DiagnosticReport, GovernanceStatus, HealthSummary, HistoryPage, HistoryRecord, IncidentDetail, IncidentPage, LoginOptions, MetricsErrors, MetricsSnapshot, MetricsWindow, MonitoringEvents, NotificationDelivery, NotificationStatus, PolicyDecision, PolicyInput, PolicyReleaseRecord, PolicyReleaseStatus, PolicyStatus, RealtimeEvent, RepeatTask, RetryPolicyInput, RuntimeInfo, RuntimeLogPage, SessionInfo, Status, TelemetryStatus, UncertainPreview, UncertainResolutionAction, UncertainResolutionInput, UncertainResolutionResponse, UpstreamPoolStatus, SLOSnapshot } from "./types";
import i18n, { normalizeLocale } from "./i18n";

export class ApiError extends Error {
  constructor(public code: string, message: string, public details?: Record<string, unknown>) {
    super(message);
    this.name = "ApiError";
  }
}

export function errorMessage(reason: unknown, fallbackKey = "generic") {
  if (reason instanceof ApiError) {
    const translated = i18n.t(`errors:api.${reason.code}`, { defaultValue: "" });
    if (translated) return translated;
  }
  if (reason instanceof Error && reason.message) return reason.message;
  return i18n.t(`errors:${fallbackKey}`);
}

function expectObject<T>(value: unknown, label: string): T {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("INVALID_API_RESPONSE", `${label}: expected object`);
  return value as T;
}

function expectArray<T>(value: unknown, label: string): T[] {
  if (!Array.isArray(value)) throw new ApiError("INVALID_API_RESPONSE", `${label}: expected array`);
  return value as T[];
}

function expectObjectArrays<T>(value: unknown, label: string, fields: string[]): T {
  const object = expectObject<Record<string, unknown>>(value, label);
  fields.forEach((field) => expectArray(object[field], `${label}.${field}`));
  return object as T;
}

function normalizeConfigCollections(config: Config): Config {
	// Empty YAML sequences are commonly omitted by the server as null; keep the
	// editor contract stable so views can safely use array operations.
	return {
		...config,
		upstreams: { ...config.upstreams, targets: config.upstreams.targets ?? [] },
		egress: { ...config.egress, allowedHosts: config.egress.allowedHosts ?? [] },
		notifications: { ...config.notifications, eventTypes: config.notifications.eventTypes ?? [] },
		governance: { ...config.governance, budgets: config.governance.budgets ?? [], prices: config.governance.prices ?? [] },
		managementSecurity: {
			...config.managementSecurity,
			oidc: {
				...config.managementSecurity.oidc,
				scopes: config.managementSecurity.oidc.scopes ?? [],
				signingAlgorithms: config.managementSecurity.oidc.signingAlgorithms ?? [],
				viewerValues: config.managementSecurity.oidc.viewerValues ?? [],
				operatorValues: config.managementSecurity.oidc.operatorValues ?? [],
				sensitiveValues: config.managementSecurity.oidc.sensitiveValues ?? [],
			},
		},
		trafficPolicy: { ...config.trafficPolicy, rules: config.trafficPolicy.rules ?? [] },
	};
}

function expectCaptureRecord(value: unknown, label: string): CaptureRecord {
  const record = expectObject<Record<string, unknown>>(value, label);
  expectArray(record.attempts, `${label}.attempts`);
  return record as unknown as CaptureRecord;
}

function expectUncertainPreview(value: unknown, label: string): UncertainPreview {
  const preview = expectObject<Record<string, unknown>>(value, label);
  if (typeof preview.confirmationToken !== "string" || !preview.confirmationToken) {
    throw new ApiError("INVALID_API_RESPONSE", `${label}.confirmationToken: expected token`);
  }
  if (typeof preview.expiresAt !== "string") {
    throw new ApiError("INVALID_API_RESPONSE", `${label}.expiresAt: expected timestamp`);
  }
  const evidence = expectObject<Record<string, unknown>>(preview.evidence, `${label}.evidence`);
  expectArray(evidence.attempts, `${label}.evidence.attempts`);
  return preview as unknown as UncertainPreview;
}

export class ApiClient {
  private unauthorizedNotified = false;
  private csrfToken = "";

  constructor(
    private locale = normalizeLocale(i18n.resolvedLanguage),
    private onUnauthorized?: (code: string) => void,
  ) {}

  private notifyUnauthorized(status: number, code: string) {
    if (status !== 401 || this.unauthorizedNotified) return;
    this.unauthorizedNotified = true;
    this.onUnauthorized?.(code);
  }

  private async throwDownloadError(response: Response): Promise<never> {
    const payload = await response.json().catch(() => ({})) as { code?: string; error?: string; details?: Record<string, unknown> };
    const code = payload.code || `HTTP_${response.status}`;
    this.notifyUnauthorized(response.status, code);
    throw new ApiError(code, payload.error || i18n.t("common:httpError", { status: response.status }), payload.details);
  }

  private async request<T>(path: string, init?: RequestInit, validate: (value: unknown) => T = (value) => value as T): Promise<T> {
    const response = await fetch(`/admin/api${path}`, {
      ...init,
      credentials: "same-origin",
      headers: {
        "Accept-Language": this.locale,
        "Content-Type": "application/json",
        ...(init?.method && init.method !== "GET" ? { "X-CSRF-Token": this.csrfToken } : {}),
        ...init?.headers,
      },
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) {
      const code = payload.code || `HTTP_${response.status}`;
      this.notifyUnauthorized(response.status, code);
      throw new ApiError(code, payload.error || i18n.t("common:httpError", { status: response.status }), payload.details);
    }
    return validate(payload);
  }

  session() {
    return this.request<SessionInfo>("/session", undefined, (value) => {
      const session = expectObjectArrays<SessionInfo>(value, "session", ["capabilities"]);
      this.csrfToken = session.csrfToken || "";
      return session;
    });
  }

  async login(key: string) {
    const session = await this.request<SessionInfo>("/session/login", { method: "POST", body: JSON.stringify({ key }) }, (value) => expectObjectArrays(value, "session", ["capabilities"]));
    this.csrfToken = session.csrfToken || "";
    this.unauthorizedNotified = false;
    return session;
  }

  logout() {
    return this.request<{ loggedOut: boolean }>("/session/logout", { method: "POST" });
  }

  runtimeInfo() {
    return this.request<RuntimeInfo>("/meta", undefined, (value) => expectObject(value, "meta"));
  }

  status() {
    return this.request<Status>("/status", undefined, (value) => expectObjectArrays(value, "status", ["requests"]));
  }

  healthSummary() {
    return this.request<HealthSummary>("/health/summary", undefined, (value) => expectObjectArrays(value, "healthSummary", ["components"]));
  }

  slo() { return this.request<SLOSnapshot>("/slo", undefined, (value) => expectObject(value, "slo")); }

	config() {
		return this.request<Config>("/config", undefined, (value) => {
			const config = expectObject<Config>(value, "config");
			if (config.schemaVersion !== 5) throw new ApiError("UNSUPPORTED_CONFIG_SCHEMA", `Unsupported config schema ${config.schemaVersion}`);
			return normalizeConfigCollections(config);
		});
	}

	loginOptions() {
		return this.request<LoginOptions>("/session/login-options", undefined, (value) => expectObject(value, "loginOptions"));
	}

	oidcLogin() {
		window.location.assign("/admin/api/session/oidc/start");
	}

  alerts() {
    return this.request<Alert[]>("/alerts", undefined, (value) => expectArray(value, "alerts"));
  }

	notificationStatus() {
		return this.request<NotificationStatus>("/notifications/status", undefined, (value) => expectObject(value, "notificationStatus"));
	}

	notificationDeliveries(limit = 20) {
		return this.request<NotificationDelivery[]>(`/notifications/deliveries?limit=${limit}`, undefined, (value) => expectArray(value, "notificationDeliveries"));
	}

	testNotification() {
		return this.request<{ queued: boolean }>("/notifications/test", { method: "POST" }, (value) => expectObject(value, "notificationTest"));
	}

	incidents(filters: { cursor?: string; limit?: number; from?: string; to?: string; state?: string; q?: string } = {}) {
		const query = new URLSearchParams();
		Object.entries(filters).forEach(([key, value]) => { if (value !== undefined && value !== "") query.set(key, String(value)); });
		return this.request<IncidentPage>(`/incidents?${query}`, undefined, (value) => expectObjectArrays(value, "incidents", ["items"]));
	}

	incident(id: string) {
		return this.request<IncidentDetail>(`/incidents/${encodeURIComponent(id)}`, undefined, (value) => expectObjectArrays(value, "incidentDetail", ["requests", "timeline"]));
  }

	subscribe(onEvent: (event: RealtimeEvent) => void, onError: () => void) {
    const source = new EventSource(`/admin/api/stream?locale=${encodeURIComponent(this.locale)}`);
		const receive = (message: Event) => {
			try {
				const event = expectObject<RealtimeEvent>(JSON.parse((message as MessageEvent).data), "realtimeEvent");
				if (event.version !== 1 || !Number.isSafeInteger(event.sequence) || typeof event.type !== "string") throw new ApiError("INVALID_API_RESPONSE", "Invalid realtime event");
				onEvent(event);
			} catch { onError(); }
		};
		source.addEventListener("sync", receive);
		source.addEventListener("reset", receive);
		source.addEventListener("update", receive);
    source.onerror = onError;
    return () => source.close();
  }

	history(filters: { cursor?: string; limit?: number; from?: string; to?: string; state?: string; q?: string } = {}) {
		const query = new URLSearchParams();
		Object.entries(filters).forEach(([key, value]) => { if (value !== undefined && value !== "") query.set(key, String(value)); });
		return this.request<HistoryPage>(`/history?${query}`, undefined, (value) => expectObjectArrays(value, "history", ["items"]));
  }

  metrics(window: MetricsWindow = "1h") {
    return this.request<MetricsSnapshot>(`/metrics?window=${window}`, undefined, (value) => expectObjectArrays(value, "metrics", ["series"]));
  }

  metricErrors(window: MetricsWindow = "1h") {
    return this.request<MetricsErrors>(`/metrics/errors?window=${window}`, undefined, (value) => expectObjectArrays(value, "metricErrors", ["categories"]));
  }

  events(after = 0, limit = 200) {
    return this.request<MonitoringEvents>(`/events?after=${after}&limit=${limit}`, undefined, (value) => expectObjectArrays(value, "events", ["events"]));
  }

  runtimeLogs(filters: { after?: number; limit?: number; tail?: boolean; level?: string; event?: string; requestId?: string; q?: string; since?: string } = {}) {
    const query = new URLSearchParams();
    Object.entries(filters).forEach(([key, value]) => { if (value !== undefined && value !== "") query.set(key, String(value)); });
    return this.request<RuntimeLogPage>(`/runtime-logs?${query}`, undefined, (value) => expectObjectArrays(value, "runtimeLogs", ["entries"]));
  }

  captureStatus() {
    return this.request<CaptureStatus>("/capture/status", undefined, (value) => expectObject(value, "captureStatus"));
  }

  captureKeyStatus() {
    return this.request<CaptureKeyStatus>("/capture/keys", undefined, (value) => expectObjectArrays(value, "captureKeys", ["configured"]));
  }

  rewrapCaptureKeys() {
    return this.request<CaptureKeyRewrapResult>("/capture/keys/rewrap", { method: "POST" }, (value) => expectObject(value, "captureKeyRewrap"));
  }

  captures() {
    return this.request<CaptureRecord[]>("/captures", undefined, (value) => expectArray<unknown>(value, "captures").map((record, index) => expectCaptureRecord(record, `captures[${index}]`)));
  }

  startCapture(requestLimit: number, activationTimeout: string) {
    return this.request<CaptureStatus>("/capture/start", { method: "POST", body: JSON.stringify({ requestLimit, activationTimeout }) }, (value) => expectObject(value, "captureStart"));
  }

  stopCapture() {
    return this.request<CaptureStatus>("/capture/stop", { method: "POST" }, (value) => expectObject(value, "captureStop"));
  }

  capturePreview(id: string) {
    return this.request<CapturePreview>(`/captures/${encodeURIComponent(id)}/preview`, undefined, (value) => {
      const preview = expectObject<Record<string, unknown>>(value, "capturePreview");
      expectCaptureRecord(preview.record, "capturePreview.record");
      expectArray(preview.parts, "capturePreview.parts");
      return preview as unknown as CapturePreview;
    });
  }

  deleteCapture(id: string) {
    return this.request<{ deleted: boolean }>(`/captures/${encodeURIComponent(id)}`, { method: "DELETE" });
  }

  deleteExpiredCaptures() {
    return this.request<{ deleted: number }>("/captures/expired", { method: "DELETE" });
  }

  timeline(id: string) {
    return this.request<HistoryRecord>(`/requests/${encodeURIComponent(id)}/timeline`);
  }

  runDiagnostics() {
    return this.request<DiagnosticReport>("/diagnostics/run", { method: "POST" });
  }

  async downloadDiagnostics() {
    const response = await fetch("/admin/api/diagnostics/export", {
      credentials: "same-origin",
      headers: { "Accept-Language": this.locale },
    });
    if (!response.ok) await this.throwDownloadError(response);
    const blob = await response.blob();
    const link = document.createElement("a");
    link.href = URL.createObjectURL(blob);
    link.download = "relay-lifeline-diagnostics.zip";
    link.click();
    URL.revokeObjectURL(link.href);
  }

  downloadRuntimeLogs() {
    return this.download("/runtime-logs/export", "relay-lifeline-runtime-logs.json");
  }

  downloadCapture(id: string, mode: "filtered" | "raw") {
    return this.download(`/captures/${encodeURIComponent(id)}/download?mode=${mode}`, `relay-lifeline-capture-${id}-${mode}.zip`, mode === "raw" ? { "X-Relay-Lifeline-Confirm": "download-sensitive" } : undefined);
  }

  private async download(path: string, filename: string, extraHeaders?: Record<string, string>) {
    const response = await fetch(`/admin/api${path}`, { credentials: "same-origin", headers: { "Accept-Language": this.locale, ...extraHeaders } });
    if (!response.ok) await this.throwDownloadError(response);
    const blob = await response.blob();
    const link = document.createElement("a");
    link.href = URL.createObjectURL(blob);
    link.download = filename;
    link.click();
    URL.revokeObjectURL(link.href);
  }

	validateConfig(config: Config) {
    return this.request<ConfigChangePlan>("/config/validate", { method: "POST", body: JSON.stringify(config) }, (value) => expectObjectArrays(value, "configPlan", ["changedSections", "hotReloadSections", "restartSections"]));
	}

	configState() {
		return this.request<ConfigRuntimeState>("/config/state", undefined, (value) => expectObject(value, "configState"));
	}

	configVersions() {
		return this.request<{ items: ConfigVersion[] }>("/config/backups", undefined, (value) => expectObjectArrays(value, "configVersions", ["items"]));
	}

	rollbackConfig(version: ConfigVersion, authenticationChange: boolean) {
		return this.request<{ restored: boolean }>(`/config/backups/${encodeURIComponent(version.name)}/rollback`, {
			method: "POST", body: JSON.stringify({ sha256: version.sha256 }),
			headers: { "X-Relay-Lifeline-Confirm": authenticationChange ? "rollback-config-auth" : "rollback-config" },
		}, (value) => expectObject(value, "configRollback"));
	}

	upstreamStatus() {
		return this.request<UpstreamPoolStatus>("/upstreams/status", undefined, (value) => expectObjectArrays(value, "upstreamStatus", ["targets"]));
	}

	governanceStatus() {
		return this.request<GovernanceStatus>("/governance/status", undefined, (value) => expectObjectArrays(value, "governanceStatus", ["entries"]));
	}

	telemetryStatus() {
		return this.request<TelemetryStatus>("/telemetry/status", undefined, (value) => expectObject(value, "telemetryStatus"));
	}

	policyStatus(limit = 50) {
		return this.request<PolicyStatus>(`/policies/status?limit=${limit}`, undefined, (value) => expectObjectArrays(value, "policyStatus", ["recent"]));
	}

	policyReleases() {
		return this.request<PolicyReleaseStatus>("/policies/releases", undefined, (value) => expectObjectArrays(value, "policyReleases", ["history"]));
	}

	savePolicyDraft(policy: Config["trafficPolicy"], draftRevision = "") {
		return this.request<{ saved: boolean; draftRevision: string }>("/policies/draft", { method: "PUT", body: JSON.stringify({ policy, draftRevision }) }, (value) => expectObject(value, "policyDraft"));
	}

	publishPolicy(input: { configRevision: string; draftRevision?: string; stage: "shadow" | "canary" | "full"; canaryPercent?: number; policy?: Config["trafficPolicy"] }) {
		return this.request<{ published: boolean; release: PolicyReleaseRecord; configRevision: string }>("/policies/publish", { method: "POST", body: JSON.stringify(input) }, (value) => expectObject(value, "policyPublish"));
	}

	rollbackPolicy(configRevision: string, policyRevision: string) {
		return this.request<{ rolledBack: boolean; release: PolicyReleaseRecord; configRevision: string }>("/policies/rollback", { method: "POST", body: JSON.stringify({ configRevision, policyRevision }) }, (value) => expectObject(value, "policyRollback"));
	}

	simulatePolicy(input: PolicyInput, source: "active" | "draft" = "active") {
		return this.request<PolicyDecision>(`/policies/simulate${source === "draft" ? "?source=draft" : ""}`, { method: "POST", body: JSON.stringify(input) }, (value) => expectObject(value, "policyDecision"));
	}

	replayPolicy(input: { captureId?: string; request?: PolicyInput }) {
		return this.request<{ dryRun: boolean; executed: boolean; containsRawBody: boolean; decision: PolicyDecision }>("/policies/replay", { method: "POST", body: JSON.stringify(input) }, (value) => expectObject(value, "policyReplay"));
	}

	saveConfig(config: Config, confirmAuthentication = false) {
		return this.request<ConfigSaveResult>("/config", {
			method: "PUT",
			body: JSON.stringify(config),
			headers: confirmAuthentication ? { "X-Relay-Lifeline-Confirm": "change-management-auth" } : undefined,
    }, (value) => expectObjectArrays(value, "configSave", ["changedSections", "hotReloadSections", "restartSections"]));
  }

  reloadConfig() {
    return this.request<{ reloaded: boolean }>("/config/reload", { method: "POST" });
  }

  pause() {
    return this.request<{ paused: boolean }>("/control/pause", { method: "POST" });
  }

  resume() {
    return this.request<{ paused: boolean; mode: string }>("/control/resume", { method: "POST" });
  }

	drain() {
		return this.request<{ mode: string; active: number }>("/control/drain", { method: "POST" });
	}

	maintenance() {
		return this.request<{ mode: string; active: number }>("/control/maintenance", { method: "POST" });
	}

  previewUncertainResolution(id: string, action: UncertainResolutionAction) {
    return this.request<UncertainPreview>(`/requests/${encodeURIComponent(id)}/uncertain/preview`, {
      method: "POST", body: JSON.stringify({ action }),
    }, (value) => expectUncertainPreview(value, "uncertainPreview"));
  }

  // 保留与接口路径一致的短方法名，兼容不同调用方。
  previewUncertain(id: string, action: UncertainResolutionAction) {
    return this.previewUncertainResolution(id, action);
  }

  previewUncertainDelivery(id: string, action: UncertainResolutionAction) {
    return this.previewUncertainResolution(id, action);
  }

  resolveUncertain(id: string, input: UncertainResolutionInput): Promise<UncertainResolutionResponse>;
  resolveUncertain(id: string, action: UncertainResolutionAction, confirmationToken: string, reason: string): Promise<UncertainResolutionResponse>;
  resolveUncertain(id: string, inputOrAction: UncertainResolutionInput | UncertainResolutionAction, confirmationToken?: string, reason?: string) {
    const input: UncertainResolutionInput = typeof inputOrAction === "string"
      ? { action: inputOrAction, confirmationToken: confirmationToken || "", reason: reason || "" }
      : inputOrAction;
    return this.request<UncertainResolutionResponse>(`/requests/${encodeURIComponent(id)}/uncertain/resolve`, {
      method: "POST", body: JSON.stringify(input),
    }, (value) => expectObject(value, "uncertainResolution"));
  }

  resolveUncertainDelivery(id: string, input: UncertainResolutionInput) {
    return this.resolveUncertain(id, input);
  }

  retry(id: string, allowUncertain = false) {
    return this.request<{ accepted: boolean }>(`/requests/${encodeURIComponent(id)}/retry`, {
      method: "POST", body: allowUncertain ? JSON.stringify({ allowUncertain: true }) : undefined,
    });
  }

  batchRetry(requestIds: string[], allowUncertain = false) {
    return this.request<BatchActionResponse>("/requests/batch/retry", {
      method: "POST", body: JSON.stringify({ requestIds, allowUncertain }),
    }, (value) => expectObjectArrays(value, "batchRetry", ["results"]));
  }

  setRetryPolicy(id: string, policy: RetryPolicyInput, overwrite = true) {
    return this.request<{ accepted: boolean }>(`/requests/${encodeURIComponent(id)}/retry-policy`, {
      method: "POST", body: JSON.stringify({ ...policy, overwrite }),
    });
  }

  clearRetryPolicy(id: string) {
    return this.request<{ accepted: boolean }>(`/requests/${encodeURIComponent(id)}/retry-policy`, { method: "DELETE" });
  }

  batchRetryPolicy(requestIds: string[], input: {
    policy?: RetryPolicyInput;
    reset?: boolean;
    overwrite?: boolean;
    retryWaitingNow?: boolean;
  }) {
    return this.request<BatchActionResponse>("/requests/batch/retry-policy", {
      method: "POST", body: JSON.stringify({ requestIds, ...input }),
    }, (value) => expectObjectArrays(value, "batchRetryPolicy", ["results"]));
  }

  repeatTasks() {
    return this.request<RepeatTask[]>("/repeat-tasks", undefined, (value) => expectArray(value, "repeatTasks"));
  }

  createRepeatTask(id: string, input: { interval: string; duration: string; idempotency: "preserve" | "regenerate"; confirmForever: boolean; maxExecutions: number; maxFailures: number; failureThreshold: number; maxTokens: number }) {
    return this.request<RepeatTask>(`/requests/${encodeURIComponent(id)}/repeat`, { method: "POST", body: JSON.stringify(input) }, (value) => expectObject(value, "repeatTask"));
  }

  repeatTaskAction(id: string, action: "pause" | "resume" | "run") {
    return this.request<RepeatTask>(`/repeat-tasks/${encodeURIComponent(id)}/${action}`, { method: "POST" }, (value) => expectObject(value, "repeatTask"));
  }

  stopRepeatTask(id: string) {
    return this.request<RepeatTask>(`/repeat-tasks/${encodeURIComponent(id)}`, { method: "DELETE" }, (value) => expectObject(value, "repeatTask"));
  }

  cancel(id: string) {
    return this.request<{ accepted: boolean }>(`/requests/${encodeURIComponent(id)}`, { method: "DELETE" });
  }

}
