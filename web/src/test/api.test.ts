import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiError } from "../api";
import type { RealtimeEvent } from "../types";

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
});
