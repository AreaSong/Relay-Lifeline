import { describe, expect, it, vi } from "vitest";
import { mergeMonitoringEvents, readMonitoringEvents } from "../eventFeed";
import type { MonitoringEvent, MonitoringEvents } from "../types";

function event(id: number): MonitoringEvent {
  return { id, time: new Date(id * 1000).toISOString(), code: `event.${id}` };
}

describe("monitoring event feed", () => {
  it("follows every page and resumes from the returned cursor", async () => {
    const fetchPage = vi.fn<(after: number, limit: number) => Promise<MonitoringEvents>>()
      .mockResolvedValueOnce({ events: [event(1), event(2)], nextAfter: 2, hasMore: true })
      .mockResolvedValueOnce({ events: [event(3)], nextAfter: 3, hasMore: false });

    const batch = await readMonitoringEvents(fetchPage, 0, 2);

    expect(fetchPage.mock.calls).toEqual([[0, 2], [2, 2]]);
    expect(batch.events.map((item) => item.id)).toEqual([1, 2, 3]);
    expect(batch.nextAfter).toBe(3);
    expect(batch.reset).toBe(false);
  });

  it("resets after a ring-buffer gap and keeps a bounded de-duplicated tail", async () => {
    const fetchPage = vi.fn().mockResolvedValue({ events: [event(101), event(102)], nextAfter: 102, oldestAfter: 100, hasMore: false, hasGap: true });
    const batch = await readMonitoringEvents(fetchPage, 4);
    const merged = mergeMonitoringEvents([event(1), event(101)], batch.events, batch.reset, 2);

    expect(batch.reset).toBe(true);
    expect(merged.map((item) => item.id)).toEqual([101, 102]);
  });

  it("rejects a paginated response whose cursor cannot advance", async () => {
    const fetchPage = vi.fn().mockResolvedValue({ events: [], nextAfter: 5, hasMore: true });
    await expect(readMonitoringEvents(fetchPage, 5)).rejects.toThrow(/cursor did not advance/);
  });
});
