import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "../api";
import { RepeatTaskDialog } from "../components/RepeatTaskDialog";
import { AppNavigation } from "../components/AppNavigation";
import type { Config, RequestInfo, SessionInfo } from "../types";
import { SettingsView } from "../views/SettingsView";

const repeatRequest = {
  id: "request-1", method: "POST", path: "/v1/responses", state: "waiting", attempt: 1,
  startedAt: new Date().toISOString(), updatedAt: new Date().toISOString(),
} as RequestInfo;

function notificationConfig() {
	const emptySection = new Proxy({}, { get: () => "" });
	return new Proxy({
		schemaVersion: 3,
		notifications: {
			stalledAfter: "10m", notifyOnRecovery: true, webhookUrl: "https://example.test/hook",
			deliveryAttempts: 3, deliveryBackoff: "5s", eventTypes: ["stalled"], locale: "en-US",
		},
	}, { get: (target, key: string | symbol) => key in target ? target[key as keyof typeof target] : emptySection }) as unknown as Config;
}

describe("critical management flows", () => {
	it("hides operator settings from viewer sessions", () => {
		const config = notificationConfig();
		const common = { config, runtimeInfo: null, themeMode: "system" as const, onThemeChange: vi.fn(), onSelect: vi.fn(), onCollapse: vi.fn(), onLogout: vi.fn() };
		const viewer = { authenticated: true, role: "viewer", capabilities: ["view"] } as SessionInfo;
		const operator = { authenticated: true, role: "operator", capabilities: ["view", "operate"] } as SessionInfo;
		const { rerender } = render(<AppNavigation {...common} view="overview" collapsed={false} session={viewer} />);
		expect(screen.queryByRole("button", { name: /Settings/i })).not.toBeInTheDocument();
		rerender(<AppNavigation {...common} view="overview" collapsed={false} session={operator} />);
		expect(screen.getByRole("button", { name: /Settings/i })).toBeInTheDocument();
	});

  it("requires confirmation and sends repeat-task safety limits", async () => {
    const user = userEvent.setup();
    const createRepeatTask = vi.fn().mockResolvedValue({});
    const refresh = vi.fn().mockResolvedValue(undefined);
    const confirm = vi.fn().mockResolvedValue(true);
    render(<RepeatTaskDialog request={repeatRequest} api={{ createRepeatTask } as unknown as ApiClient} refresh={refresh} onClose={vi.fn()} onError={vi.fn()} onSuccess={vi.fn()} confirm={confirm} />);

    await user.click(screen.getByRole("button", { name: /Create task/i }));
    await waitFor(() => expect(createRepeatTask).toHaveBeenCalled());
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(createRepeatTask).toHaveBeenCalledWith("request-1", expect.objectContaining({ maxExecutions: 100, maxFailures: 20, failureThreshold: 5, maxTokens: 100000 }));
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("saves settings and exposes webhook operations", async () => {
    const user = userEvent.setup();
    const save = vi.fn().mockResolvedValue(undefined);
    const testNotification = vi.fn().mockResolvedValue({ queued: true });
    const api = {
				notificationStatus: vi.fn().mockResolvedValue({ configured: true, signingConfigured: true, signingKeyId: "primary-2026", queueDepth: 0, queueCapacity: 100, enqueued: 1, delivered: 1, failed: 0, dropped: 0 }),
      notificationDeliveries: vi.fn().mockResolvedValue([]),
      testNotification,
    } as unknown as ApiClient;
    const config = notificationConfig();
    render(<SettingsView api={api} config={config} baseline={config} setConfig={vi.fn()} save={save} reload={vi.fn()} discard={vi.fn()} dirty busy={false} runtimeInfo={null} themeMode="system" setThemeMode={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: /Save/i }));
    expect(save).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("tab", { name: /Notifications/i }));
    const testButton = await screen.findByRole("button", { name: /Send test notification/i });
    await waitFor(() => expect(testButton).toBeEnabled());
    await user.click(testButton);
    expect(testNotification).toHaveBeenCalledTimes(1);
  });
});
