import { expect, test, type Page } from "@playwright/test";

const config = {
  schemaVersion: 3,
  server: { listen: "127.0.0.1:8318", adminEnabled: true, configBackupDir: "", readHeaderTimeout: "10s", shutdownTimeout: "30s", maxRequestBody: "64MiB" },
  upstream: { baseUrl: "http://127.0.0.1:8317", connectTimeout: "10s", responseHeaderTimeout: "30s", responseBodyIdleTimeout: "90s" },
  retry: { enabled: true, mode: "all-errors", minInterval: "5s", maxInterval: "1m", maxAttempts: 0, honorRetryAfter: true },
  stream: { heartbeatInterval: "15s", memoryLimit: "8MiB", maxResponseBody: "512MiB", maxTotalCache: "2GiB", tempDir: "" },
  queue: { maxActive: 10, maxWaiting: 100, recoverySpacing: "100ms" },
  history: { maxItems: 500, retention: "24h" },
  observability: { errorDetails: "safe", maxErrorDetail: "2KiB" },
  capture: { enabled: false, storageDir: "/tmp/relay-captures", retention: "24h", defaultRequestLimit: 3, activationTimeout: "10m", maxBodySize: "64MiB", maxTotalSize: "1GiB", maxAttemptsPerRequest: 20, minimumFreeDisk: "1GiB", logMaxItems: 2000, logRetention: "1h" },
  localization: { defaultLocale: "en-US", fallbackLocale: "en-US" },
  risk: { warningAfter: "15m", warningAttempts: 10, authErrorAttempts: 3, queueWarningPercent: 80, minimumFreeDisk: "512MiB" },
  notifications: { stalledAfter: "10m", notifyOnRecovery: true, webhookUrl: "https://example.test/hook", deliveryAttempts: 3, deliveryBackoff: "5s", eventTypes: ["stalled"], locale: "en-US" },
  logging: { level: "info", locale: "en-US", logRequestBody: false, logResponseBody: false, logAuthorization: false },
  persistence: { enabled: false, directory: "/tmp/relay-events", retention: "168h", syncWrites: true },
  incidents: { enabled: true, correlationWindow: "5m", recoveryStableWindow: "1m", retention: "168h", maxItems: 100 },
  lifecycle: { trackUncertainDelivery: true, preserveIdempotencyKey: true, generateIdempotencyKey: true, maxRequestDuration: "24h", clientDisconnectPolicy: "cancel" },
  managementSecurity: { loginFailuresPerMinute: 5, loginCooldown: "1m", sessionIdleTimeout: "30m" },
  metricsExport: { enabled: false, path: "/metrics" },
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
		if (path === "/meta") return response({ version: "2.3.0", revision: "test", builtAt: new Date().toISOString(), goVersion: "go1.25", platform: "linux/amd64", startedAt: new Date().toISOString(), uptimeSeconds: 60, adminApiVersion: "3", configSchemaVersion: 3, process: {} });
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
