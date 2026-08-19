import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ApiClient } from "../api";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { NotificationPopover } from "../components/NotificationPopover";
import { OperationsCharts, operationsChartTheme } from "../components/OperationsCharts";
import type { OperationsChartLabels } from "../components/OperationsCharts";
import type { Config } from "../types";
import { CapturesView } from "../views/CapturesView";
import { LogsView } from "../views/LogsView";

vi.mock("../components/chartRuntime", () => ({
  init: () => ({
    setOption: vi.fn(), clear: vi.fn(), resize: vi.fn(), dispose: vi.fn(), isDisposed: () => false,
  }),
}));

class TestResizeObserver {
  observe() {}
  disconnect() {}
}

vi.stubGlobal("ResizeObserver", TestResizeObserver);

afterEach(() => vi.clearAllMocks());

describe("operations UI behavior", () => {
  it("keeps background log and capture polling stopped while hidden, while manual refresh still works", async () => {
    const user = userEvent.setup();
    const runtimeLogs = vi.fn().mockResolvedValue({ entries: [], nextAfter: 0, oldestAfter: 0, hasMore: false, hasGap: false });
    const logAPI = { runtimeLogs, downloadRuntimeLogs: vi.fn() } as unknown as ApiClient;
    const logs = render(<LogsView api={logAPI} pageVisible={false} onError={vi.fn()} />);
    await Promise.resolve();
    expect(runtimeLogs).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(runtimeLogs).toHaveBeenCalledTimes(1);
    logs.rerender(<LogsView api={logAPI} pageVisible onError={vi.fn()} />);
    await waitFor(() => expect(runtimeLogs).toHaveBeenCalledTimes(2));
    logs.unmount();

    const captureStatus = vi.fn().mockResolvedValue({ available: true, active: false, remainingRequests: 0, storageBytes: 0, maxTotalBytes: 1, captureCount: 0 });
    const captures = vi.fn().mockResolvedValue([]);
    const captureKeyStatus = vi.fn().mockResolvedValue({ activeId: "test", configured: ["test"], recordsById: {}, unresolved: 0 });
    const captureAPI = { captureStatus, captures, captureKeyStatus } as unknown as ApiClient;
    const config = { capture: { defaultRequestLimit: 3, activationTimeout: "10m" } } as Config;
    const captureView = render(<CapturesView api={captureAPI} config={config} pageVisible={false} onError={vi.fn()} onSuccess={vi.fn()} canOperate={false} canSensitive={false} confirm={vi.fn()} />);
    await Promise.resolve();
    expect(captureStatus).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(captureStatus).toHaveBeenCalledTimes(1);
    captureView.rerender(<CapturesView api={captureAPI} config={config} pageVisible onError={vi.fn()} onSuccess={vi.fn()} canOperate={false} canSensitive={false} confirm={vi.fn()} />);
    await waitFor(() => expect(captureStatus).toHaveBeenCalledTimes(2));
  });

  it("moves focus into notifications, routes global alerts to logs, and restores focus", async () => {
    const user = userEvent.setup();
    const onOpenLogs = vi.fn();
    render(<NotificationPopover alerts={[{ id: "a1", type: "queue_pressure", severity: "warning", message: "Queue pressure", createdAt: new Date().toISOString() }]} incidents={[]} onOpenRequest={vi.fn()} onOpenIncident={vi.fn()} onOpenLogs={onOpenLogs} />);
    const trigger = screen.getByRole("button", { name: /notifications/i });
    await user.click(trigger);
    const alert = await screen.findByRole("button", { name: /Queue pressure/i });
    await waitFor(() => expect(alert).toHaveFocus());
    await user.click(alert);
    expect(onOpenLogs).toHaveBeenCalledWith("queue_pressure");
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("lets the top confirmation consume Escape without closing its parent dialog", () => {
    const parentClose = vi.fn();
    const cancel = vi.fn();
    window.addEventListener("keydown", parentClose);
    render(<ConfirmDialog state={{ title: "Confirm", description: "Keep parent open" }} onConfirm={vi.fn()} onCancel={cancel} />);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(parentClose).not.toHaveBeenCalled();
    window.removeEventListener("keydown", parentClose);
  });

  it("exposes chart data as screen-reader tables instead of canvas-only labels", async () => {
    const labels: OperationsChartLabels = {
      reliabilityTitle: "Reliability", pressureTitle: "Pressure", errorsTitle: "Errors", recoveryTitle: "Recovery",
      empty: "Empty", unavailable: "Unavailable", requests: "Requests", successRate: "Success rate", failedAttempts: "Failed attempts",
      active: "Active", requesting: "Requesting", waiting: "Waiting", queued: "Queued", duration: "Time", expand: "Expand", collapse: "Collapse",
    };
    render(<OperationsCharts
      reliability={[{ time: "2026-08-19T12:00:00Z", requests: 10, successful: 9, failedAttempts: 1 }]}
      pressure={[{ time: "2026-08-19T12:00:00Z", active: 4, requesting: 2, waiting: 1, queued: 1 }]}
      errors={[{ category: "timeout", count: 2 }]}
      recovery={[{ bucket: "1-5s", count: 3 }]}
      labels={labels} theme={operationsChartTheme(false)} locale="en-US"
    />);
    expect(await screen.findByRole("table", { name: "Reliability" })).toHaveTextContent("90%");
    expect(screen.getByRole("table", { name: "Pressure" })).toHaveTextContent("Requesting");
    await userEvent.click(screen.getByRole("tab", { name: "Errors" }));
    expect(screen.getByRole("table", { name: "Errors" })).toHaveTextContent("timeout");
    await userEvent.click(screen.getByRole("tab", { name: "Recovery" }));
    expect(screen.getByRole("table", { name: "Recovery" })).toHaveTextContent("1-5s");
  });
});
