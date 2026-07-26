import type { Alert, Config, DiagnosticReport, HistoryRecord, Status } from "./types";
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

export class ApiClient {
  constructor(private token: string, private locale = normalizeLocale(i18n.resolvedLanguage)) {}

  private async request<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await fetch(`/admin/api${path}`, {
      ...init,
      headers: {
        Authorization: `Bearer ${this.token}`,
        "Accept-Language": this.locale,
        "Content-Type": "application/json",
        ...init?.headers,
      },
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) {
      throw new ApiError(payload.code || `HTTP_${response.status}`, payload.error || i18n.t("common:httpError", { status: response.status }), payload.details);
    }
    return payload as T;
  }

  session() {
    return this.request<{ authenticated: boolean }>("/session");
  }

  status() {
    return this.request<Status>("/status");
  }

  config() {
    return this.request<Config>("/config");
  }

  alerts() {
    return this.request<Alert[]>("/alerts");
  }

  history() {
    return this.request<HistoryRecord[]>("/history");
  }

  timeline(id: string) {
    return this.request<HistoryRecord>(`/requests/${encodeURIComponent(id)}/timeline`);
  }

  runDiagnostics() {
    return this.request<DiagnosticReport>("/diagnostics/run", { method: "POST" });
  }

  async downloadDiagnostics() {
    const response = await fetch("/admin/api/diagnostics/export", {
      headers: { Authorization: `Bearer ${this.token}`, "Accept-Language": this.locale },
    });
    if (!response.ok) throw new Error(i18n.t("common:httpError", { status: response.status }));
    const blob = await response.blob();
    const link = document.createElement("a");
    link.href = URL.createObjectURL(blob);
    link.download = "relay-lifeline-diagnostics.json";
    link.click();
    URL.revokeObjectURL(link.href);
  }

  saveConfig(config: Config) {
    return this.request<{ saved: boolean; restartRequired: boolean }>("/config", {
      method: "PUT",
      body: JSON.stringify(config),
    });
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
