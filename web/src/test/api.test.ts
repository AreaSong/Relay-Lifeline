import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError } from "../api";
import type { Config, RealtimeEvent } from "../types";

function jsonResponse(status: number, payload: unknown) {
  return new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
}

class FakeEventSource {
  static latest?: FakeEventSource;
  readonly listeners = new Map<string, EventListener>();
  onerror: ((event: Event) => void) | null = null;
  closed = false;

  constructor(readonly url: string) { FakeEventSource.latest = this; }
  addEventListener(type: string, listener: EventListener) { this.listeners.set(type, listener); }
  close() { this.closed = true; }
  emit(type: string, payload: unknown) {
    const event = new MessageEvent(type, { data: JSON.stringify(payload) });
    this.listeners.get(type)?.(event);
  }
}

describe("ApiClient", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("notifies once when a management session expires", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(401, { code: "SESSION_EXPIRED", error: "expired" }));
    vi.stubGlobal("fetch", fetchMock);
    const unauthorized = vi.fn();
    const api = new ApiClient("en-US", unauthorized);

		const reason = await api.status().catch((error: unknown) => error);
		expect(reason).toBeInstanceOf(ApiError);
		expect((reason as ApiError).code).toBe("SESSION_EXPIRED");
    await expect(api.status()).rejects.toBeInstanceOf(ApiError);
    expect(unauthorized).toHaveBeenCalledTimes(1);
  });

  it("parses versioned sync and incremental realtime events", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const api = new ApiClient("en-US");
    const received = vi.fn<(event: RealtimeEvent) => void>();
    const failed = vi.fn();
    const unsubscribe = api.subscribe(received, failed);
    const source = FakeEventSource.latest!;
    const event: RealtimeEvent = { version: 1, sequence: 7, type: "status", generatedAt: new Date().toISOString(), data: { active: 1 } };
    source.emit("update", event);

    expect(source.url).toContain("locale=en-US");
    expect(received).toHaveBeenCalledWith(event);
    expect(failed).not.toHaveBeenCalled();
    unsubscribe();
    expect(source.closed).toBe(true);
  });

	it("normalizes nullable YAML collections before exposing configuration", async () => {
		const raw = {
			schemaVersion: 5,
			upstreams: { targets: null },
			egress: { allowedHosts: null },
			notifications: { eventTypes: null },
			governance: { budgets: null, prices: null },
			managementSecurity: { oidc: { scopes: null, signingAlgorithms: null, viewerValues: null, operatorValues: null, sensitiveValues: null } },
			trafficPolicy: { rules: null },
		} as unknown as Config;
		const fetchMock = vi.fn()
			.mockResolvedValueOnce(jsonResponse(200, raw))
			.mockResolvedValueOnce(jsonResponse(200, { ...raw, schemaVersion: 4 }));
		vi.stubGlobal("fetch", fetchMock);
		const api = new ApiClient("en-US");

		const config = await api.config();
		expect(config.upstreams.targets).toEqual([]);
		expect(config.egress.allowedHosts).toEqual([]);
		expect(config.notifications.eventTypes).toEqual([]);
		expect(config.governance.budgets).toEqual([]);
		expect(config.governance.prices).toEqual([]);
		expect(config.managementSecurity.oidc).toMatchObject({
			scopes: [], signingAlgorithms: [], viewerValues: [], operatorValues: [], sensitiveValues: [],
		});
		expect(config.trafficPolicy.rules).toEqual([]);
		await expect(api.config()).rejects.toMatchObject({ code: "UNSUPPORTED_CONFIG_SCHEMA" });
	});

	it("validates management collection contracts across read endpoints", async () => {
		const payload = (path: string): unknown => {
			if (path === "/admin/api/status") return { requests: [] };
			if (path === "/admin/api/health/summary") return { components: [] };
			if (path === "/admin/api/alerts" || path === "/admin/api/notifications/deliveries" || path === "/admin/api/captures") return [];
			if (path === "/admin/api/incidents") return { items: [] };
			if (path.startsWith("/admin/api/incidents/")) return { requests: [], timeline: [] };
			if (path === "/admin/api/history") return { items: [] };
			if (path === "/admin/api/metrics") return { series: [] };
			if (path === "/admin/api/metrics/errors") return { categories: [] };
			if (path === "/admin/api/events") return { events: [] };
			if (path === "/admin/api/runtime-logs") return { entries: [] };
			if (path === "/admin/api/capture/keys") return { configured: [] };
			if (path === "/admin/api/config/backups") return { items: [] };
			if (path === "/admin/api/upstreams/status") return { targets: [] };
			if (path === "/admin/api/governance/status") return { entries: [] };
			if (path === "/admin/api/policies/status") return { recent: [] };
			if (path === "/admin/api/policies/releases") return { history: [] };
			return {};
		};
		const fetchMock = vi.fn().mockImplementation(async (input: RequestInfo | URL) => {
			const url = new URL(String(input), "http://relay.test");
			return jsonResponse(200, payload(url.pathname));
		});
		vi.stubGlobal("fetch", fetchMock);
		const api = new ApiClient("en-US");

		await Promise.all([
			api.runtimeInfo(), api.status(), api.healthSummary(), api.slo(), api.loginOptions(), api.alerts(),
			api.notificationStatus(), api.notificationDeliveries(), api.incidents({ limit: 10 }), api.incident("incident/1"),
			api.history({ state: "waiting" }), api.metrics(), api.metricErrors(), api.events(), api.runtimeLogs({ tail: true }),
			api.captureStatus(), api.captureKeyStatus(), api.rewrapCaptureKeys(), api.captures(), api.configState(), api.configVersions(),
			api.upstreamStatus(), api.governanceStatus(), api.telemetryStatus(), api.policyStatus(), api.policyReleases(),
		]);
		expect(fetchMock).toHaveBeenCalledTimes(26);
	});

	it("binds policy and uncertain mutations to the authenticated CSRF session", async () => {
		const preview = {
			confirmationToken: "token-1", expiresAt: new Date(Date.now() + 60_000).toISOString(),
			evidence: { state: "uncertain", attempt: 1, startedAt: new Date().toISOString(), uncertainSince: new Date().toISOString(), attempts: [] },
		};
		const fetchMock = vi.fn().mockImplementation(async (input: RequestInfo | URL) => {
			const path = new URL(String(input), "http://relay.test").pathname;
			if (path === "/admin/api/session/login") return jsonResponse(200, { authenticated: true, capabilities: ["operate"], csrfToken: "csrf-1" });
			if (path.endsWith("/uncertain/preview")) return jsonResponse(200, preview);
			if (path === "/admin/api/requests/batch/retry") return jsonResponse(200, { results: [] });
			return jsonResponse(200, {});
		});
		vi.stubGlobal("fetch", fetchMock);
		const api = new ApiClient("en-US");
		await api.login("operator-key");

		await api.savePolicyDraft({} as Config["trafficPolicy"]);
		await api.publishPolicy({ configRevision: "config-1", stage: "canary", canaryPercent: 10 });
		await api.rollbackPolicy("config-2", "policy-1");
		await api.simulatePolicy({ method: "POST", path: "/v1/responses", model: "test", principal: "operator" });
		await api.replayPolicy({ captureId: "capture/1" });
		const receivedPreview = await api.previewUncertainResolution("request/1", "confirm_success");
		await api.resolveUncertain("request/1", "confirm_success", receivedPreview.confirmationToken, "provider receipt verified");
		await api.retry("request/1", true);
		await api.batchRetry(["request/1"], true);

		const mutations = fetchMock.mock.calls.slice(1).map(([, init]) => init as RequestInit);
		expect(mutations).toHaveLength(9);
		for (const init of mutations) {
			expect(init.headers).toMatchObject({ "X-CSRF-Token": "csrf-1" });
		}
		expect(JSON.parse(String(mutations[6].body))).toEqual({
			action: "confirm_success", confirmationToken: "token-1", reason: "provider receipt verified",
		});
	});
});
