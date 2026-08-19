import type { MonitoringEvent, MonitoringEvents } from "./types";

export const monitoringEventCapacity = 1000;

export interface MonitoringEventBatch {
  events: MonitoringEvent[];
  nextAfter: number;
  reset: boolean;
}

export async function readMonitoringEvents(
  fetchPage: (after: number, limit: number) => Promise<MonitoringEvents>,
  after: number,
  limit = 200,
): Promise<MonitoringEventBatch> {
  let cursor = after;
  let reset = false;
  const events: MonitoringEvent[] = [];
  const seen = new Set<number>();

  for (;;) {
    const page = await fetchPage(cursor, limit);
    if (page.hasGap) {
      reset = true;
      events.length = 0;
      seen.clear();
    }
    for (const event of page.events) {
      if (!seen.has(event.id)) {
        seen.add(event.id);
        events.push(event);
      }
    }
    if (page.nextAfter <= cursor && page.hasMore) {
      throw new Error("monitoring event cursor did not advance");
    }
    cursor = page.nextAfter;
    if (!page.hasMore) break;
  }

  return { events, nextAfter: cursor, reset };
}

export function mergeMonitoringEvents(
  current: MonitoringEvent[],
  incoming: MonitoringEvent[],
  reset = false,
  capacity = monitoringEventCapacity,
) {
  const byID = new Map<number, MonitoringEvent>();
  if (!reset) current.forEach((event) => byID.set(event.id, event));
  incoming.forEach((event) => byID.set(event.id, event));
  return Array.from(byID.values()).sort((left, right) => left.id - right.id).slice(-capacity);
}
