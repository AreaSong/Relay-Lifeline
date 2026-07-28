import type { Alert, CaptureKeyRewrapResult, CaptureKeyStatus, CapturePreview, CaptureRecord, CaptureStatus, Config, ConfigChangePlan, ConfigSaveResult, DiagnosticReport, HistoryRecord, Incident, MetricsErrors, MetricsSnapshot, MetricsWindow, MonitoringEvents, RealtimeSnapshot, RuntimeInfo, RuntimeLogPage, SessionInfo, Status } from "./types";
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
    private onUnauthorized?: () => void,
  ) {}

  private notifyUnauthorized(status: number) {
    if (status !== 401 || this.unauthorizedNotified) return;
    this.unauthorizedNotified = true;
    this.onUnauthorized?.();
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
      this.notifyUnauthorized(response.status);
      throw new ApiError(payload.code || `HTTP_${response.status}`, payload.error || i18n.t("common:httpError", { status: response.status }), payload.details);
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
      if (config.schemaVersion !== 2) throw new ApiError("UNSUPPORTED_CONFIG_SCHEMA", `Unsupported config schema ${config.schemaVersion}`);
      return config;
    });
  }

  alerts() {
    return this.request<Alert[]>("/alerts", undefined, (value) => expectArray(value, "alerts"));
  }

  incidents() {
    return this.request<Incident[]>("/incidents", undefined, (value) => expectArray(value, "incidents"));
  }

  subscribe(onSnapshot: (snapshot: RealtimeSnapshot) => void, onError: () => void) {
    const source = new EventSource(`/admin/api/stream?locale=${encodeURIComponent(this.locale)}`);
    source.addEventListener("snapshot", (event) => {
      try { onSnapshot(expectObject<RealtimeSnapshot>(JSON.parse((event as MessageEvent).data), "streamSnapshot")); }
      catch { onError(); }
    });
    source.onerror = onError;
    return () => source.close();
  }

  history() {
    return this.request<HistoryRecord[]>("/history", undefined, (value) => expectArray(value, "history"));
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
    this.notifyUnauthorized(response.status);
    if (!response.ok) throw new Error(i18n.t("common:httpError", { status: response.status }));
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
    this.notifyUnauthorized(response.status);
    if (!response.ok) throw new Error(i18n.t("common:httpError", { status: response.status }));
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

  retry(id: string) {
    return this.request<{ accepted: boolean }>(`/requests/${encodeURIComponent(id)}/retry`, { method: "POST" });
  }

  cancel(id: string) {
    return this.request<{ accepted: boolean }>(`/requests/${encodeURIComponent(id)}`, { method: "DELETE" });
  }
}
