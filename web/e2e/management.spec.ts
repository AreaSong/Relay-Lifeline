import { expect, test, type Page } from "@playwright/test";

const config = {
	schemaVersion: 5,
	server: { listen: "127.0.0.1:8318", adminEnabled: true, configBackupDir: "", readHeaderTimeout: "10s", readBodyTimeout: "60s", idleTimeout: "90s", downstreamWriteIdleTimeout: "30s", shutdownTimeout: "30s", maxHeaderBytes: 1048576, maxRequestBody: "64MiB" },
	upstream: { baseUrl: "http://127.0.0.1:8317", connectTimeout: "10s", responseHeaderTimeout: "30s", responseBodyIdleTimeout: "90s" },
	upstreams: { strategy: "primary-only", targets: [], health: { mode: "passive" }, circuit: { enabled: false, minimumRequests: 0, failurePercent: 0, openDuration: "30s", halfOpenMax: 1 } },
	retry: { enabled: true, mode: "all-errors", minInterval: "5s", maxInterval: "1m", maxAttempts: 0, maxElapsed: "0s", retryAfterCap: "10m", honorRetryAfter: true },
  stream: { heartbeatInterval: "15s", memoryLimit: "8MiB", maxResponseBody: "512MiB", maxTotalCache: "2GiB", tempDir: "" },
  queue: { maxActive: 10, maxWaiting: 100, recoverySpacing: "100ms" },
  history: { maxItems: 500, retention: "24h" },
  observability: { errorDetails: "safe", maxErrorDetail: "2KiB", telemetry: { enabled: false, protocol: "grpc", endpoint: "localhost:4317", insecure: false, sampleRatio: 1, serviceName: "relay-lifeline", environment: "test", exportTimeout: "10s", metricInterval: "60s" } },
  capture: { enabled: false, storageDir: "/tmp/relay-captures", retention: "24h", defaultRequestLimit: 3, activationTimeout: "10m", maxBodySize: "64MiB", maxTotalSize: "1GiB", maxAttemptsPerRequest: 20, minimumFreeDisk: "1GiB", logMaxItems: 2000, logRetention: "1h" },
  localization: { defaultLocale: "en-US", fallbackLocale: "en-US" },
  risk: { warningAfter: "15m", warningAttempts: 10, authErrorAttempts: 3, queueWarningPercent: 80, minimumFreeDisk: "512MiB" },
  notifications: { stalledAfter: "10m", notifyOnRecovery: true, webhookUrl: "https://example.test/hook", deliveryAttempts: 3, deliveryBackoff: "5s", eventTypes: ["stalled"], locale: "en-US" },
  logging: { level: "info", locale: "en-US", logRequestBody: false, logResponseBody: false, logAuthorization: false },
  persistence: { enabled: false, directory: "/tmp/relay-events", retention: "168h", syncWrites: true },
  incidents: { enabled: true, correlationWindow: "5m", recoveryStableWindow: "1m", retention: "168h", maxItems: 100 },
	lifecycle: { trackUncertainDelivery: true, preserveIdempotencyKey: true, generateIdempotencyKey: true, allowUncertainRetry: false, allowCrossDomainFailover: false, uncertainResolutionTarget: "2h", maxRequestDuration: "24h", clientDisconnectPolicy: "cancel" },
	managementSecurity: { localAccessEnabled: true, loginFailuresPerMinute: 5, loginCooldown: "1m", sessionIdleTimeout: "30m", sessionMaxLifetime: "8h", oidc: { enabled: false, issuerUrl: "", clientId: "", redirectUrl: "", scopes: [], signingAlgorithms: ["RS256"], roleClaim: "groups", viewerValues: [], operatorValues: [], sensitiveValues: [] } },
	metricsExport: { enabled: false, path: "/metrics" },
	egress: { denyPrivateNetworks: true, allowedHosts: [] },
	governance: { mode: "observe", unknownUsagePolicy: "observe", maxConcurrent: 0, requestsPerMinute: 0, tokenLimit: 0, costLimitMicros: 0, tokenReservation: 0, costReservationMicros: 0, reservationMinTokens: 0, reservationMaxTokens: 0, reservationMinCostMicros: 0, reservationMaxCostMicros: 0, softThresholdPercent: 80, forecastWindow: "1h", budgets: [], prices: [] },
	slo: { enabled: true, availabilityTarget: 0.99, recoveryLatencyTarget: "5m", window: "1h" },
	trafficPolicy: { enabled: false, mode: "observe", releaseStage: "draft", canaryPercent: 10, revision: "", rules: [], shadow: { enabled: false, targetId: "primary", samplePercent: 0, maxConcurrent: 1, maxRequestBody: "1MiB", requestBudgetPerHour: 0, costBudgetMicrosPerHour: 0, costReservationMicros: 0, requireIdempotency: true }, adaptive: { enabled: false, errorBudgetFloor: 0, minimumObservations: 0, maximumLatencyMilliseconds: 0, latencyWeight: 1, errorRateWeight: 1, costWeight: 0, capabilityWeight: 0, switchCooldown: "5m", fallbackTargetId: "primary", autoStopBurnRate: 0, autoStopFailureRate: 0 } },
};

const status = { paused: false, active: 0, queued: 0, waiting: 0, requesting: 0, totalRequests: 0, successful: 0, failedAttempts: 0, upstream: { state: "healthy" }, requests: [] };
const metrics = { generatedAt: new Date().toISOString(), dataSince: new Date().toISOString(), complete: true, window: "1h", from: new Date().toISOString(), to: new Date().toISOString(), totals: { requests: 0, successful: 0, failed: 0, canceled: 0, rejected: 0, attempts: 0, failedAttempts: 0, recovered: 0, successRate: 0, averageRecoveryMilliseconds: 0 }, load: { active: 0, queued: 0, waiting: 0, requesting: 0 }, series: [], recovery: { durationBuckets: [], attemptBuckets: [] } };

async function mockManagementAPI(page: Page) {
  await page.route("**/admin/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/admin/api", "");
    const response = (body: unknown, statusCode = 200) => route.fulfill({ status: statusCode, contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/session") return response({ authenticated: true, role: "operator", capabilities: ["view", "operate"], csrfToken: "test-csrf" });
    if (path === "/status") return response(status);
    if (path === "/config") return response(config);
		if (path === "/meta") return response({ version: "2.3.0", revision: "test", builtAt: new Date().toISOString(), goVersion: "go1.25", platform: "linux/amd64", startedAt: new Date().toISOString(), uptimeSeconds: 60, adminApiVersion: "3", configSchemaVersion: 5, process: {} });
		if (path === "/config/state") return response({ active: config, desired: config, activeRevision: "active-test", desiredRevision: "active-test", pendingRestart: { schemaVersion: 5, changedSections: [], hotReloadSections: [], restartSections: [], restartRequired: false, fields: [] } });
		if (path === "/health/summary") return response({ generatedAt: new Date().toISOString(), overall: "healthy", components: [{ name: "gateway", state: "healthy", healthy: true, details: {} }, { name: "uncertain-delivery", state: "healthy", healthy: true, details: { open: 0, targetSeconds: 7200 } }, { name: "governance", state: "healthy", healthy: true, details: {} }], actions: [] });
		if (path === "/upstreams/status") return response({ strategy: "primary-only", targets: [] });
		if (path === "/governance/status") return response({ mode: "observe", unknownUsagePolicy: "observe", principals: 0, reservations: 0, entries: [], counters: { admitted: 0, rejected: {}, settlements: 0, knownSettlements: 0, unknownSettlements: 0, reconciled: 0, persistenceFailures: 0 }, ledger: { enabled: true, healthy: true } });
		if (path === "/policies/status") return response({ enabled: false, mode: "observe", rules: 0, decisions: 0, denied: 0, routed: 0, adaptive: 0, shadowPlanned: 0, shadowActive: 0, shadowSent: 0, shadowSkipped: 0, shadowFailed: 0, shadowReservedCostMicros: 0, adaptiveStopped: false, adaptiveSwitches: 0, recent: [] });
		if (path === "/policies/releases") return response({ currentRevision: "", currentStage: "draft", history: [] });
		if (path === "/telemetry/status") return response({ enabled: false, healthy: true, traceHealthy: true, metricHealthy: true, traceExportFailures: 0, metricExportFailures: 0 });
    if (path === "/alerts" || path === "/repeat-tasks") return response([]);
    if (path === "/incidents") return response({ items: [], hasMore: false });
    if (path === "/history") return response({ items: [], hasMore: false });
    if (path === "/metrics") return response(metrics);
    if (path === "/metrics/errors") return response({ ...metrics, categories: [] });
    if (path === "/events") return response({ events: [], nextAfter: 0, hasMore: false });
    if (path === "/notifications/status") return response({ configured: true, signingConfigured: true, signingKeyId: "primary-test", queueDepth: 0, queueCapacity: 100, enqueued: 0, delivered: 0, failed: 0, dropped: 0 });
    if (path === "/notifications/deliveries") return response([]);
    if (path === "/notifications/test") return response({ queued: true }, 202);
    if (path === "/stream") return route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: "" });
    return response({ code: "ENDPOINT_NOT_FOUND", error: path }, 404);
  });
}

test("operator can inspect and test webhook operations", async ({ page }) => {
  await mockManagementAPI(page);
  await page.goto("#/settings");
  await expect(page.getByRole("heading", { name: "Service and upstream" })).toBeVisible();
  await page.getByRole("tab", { name: "Notifications" }).click();
  await expect(page.getByRole("button", { name: "Send test notification" })).toBeEnabled();
  const request = page.waitForRequest((candidate) => candidate.url().endsWith("/admin/api/notifications/test") && candidate.method() === "POST");
  await page.getByRole("button", { name: "Send test notification" }).click();
  await request;
  await expect(page.getByText("0 / 100")).toBeVisible();
  await expect(page.getByText("primary-test")).toBeVisible();
});
