import type { Alert, BatchActionResponse, CaptureKeyRewrapResult, CaptureKeyStatus, CapturePreview, CaptureRecord, CaptureStatus, Config, ConfigChangePlan, ConfigSaveResult, DiagnosticReport, HistoryPage, HistoryRecord, IncidentDetail, IncidentPage, MetricsErrors, MetricsSnapshot, MetricsWindow, MonitoringEvents, NotificationDelivery, NotificationStatus, RealtimeEvent, RepeatTask, RetryPolicyInput, RuntimeInfo, RuntimeLogPage, SessionInfo, Status } from "./types";
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

function expectCaptureRecord(value: unknown, label: string): CaptureRecord {
  const record = expectObject<Record<string, unknown>>(value, label);
  expectArray(record.attempts, `${label}.attempts`);
  return record as unknown as CaptureRecord;
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

  config() {
    return this.request<Config>("/config", undefined, (value) => {
      const config = expectObject<Config>(value, "config");
      if (config.schemaVersion !== 3) throw new ApiError("UNSUPPORTED_CONFIG_SCHEMA", `Unsupported config schema ${config.schemaVersion}`);
      return config;
    });
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
		return this.request<IncidentDetail>(`/incidents/${encodeURIComponent(id)}`, undefined, (value) => expectObjectArrays(value, "incidentDetail", ["requests"]));
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

  runtimeLogs(filters: { after?: number; limit?: number; tail?: boolean; level?: string; event?: string; requestId?: string } = {}) {
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

  saveConfig(config: Config) {
    return this.request<ConfigSaveResult>("/config", {
      method: "PUT",
      body: JSON.stringify(config),
    }, (value) => expectObjectArrays(value, "configSave", ["changedSections", "hotReloadSections", "restartSections"]));
  }

  reloadConfig() {
    return this.request<{ reloaded: boolean }>("/config/reload", { method: "POST" });
  }

  pause() {
    return this.request<{ paused: boolean }>("/control/pause", { method: "POST" });
  }

  resume() {
    return this.request<{ paused: boolean }>("/control/resume", { method: "POST" });
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
