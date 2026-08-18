import type { HistoryRecord } from "./types";

export function mergeHistoryPage(current: HistoryRecord[], fresh: HistoryRecord[]): HistoryRecord[] {
  const freshIds = new Set(fresh.map((item) => item.id));
  return [...fresh, ...current.filter((item) => !freshIds.has(item.id))];
}
