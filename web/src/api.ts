import type { Config, Status } from "./types";

export class ApiClient {
  constructor(private token: string) {}

  private async request<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await fetch(`/admin/api${path}`, {
      ...init,
      headers: {
        Authorization: `Bearer ${this.token}`,
        "Content-Type": "application/json",
        ...init?.headers,
      },
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) {
      throw new Error(payload.error || `HTTP ${response.status}`);
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
