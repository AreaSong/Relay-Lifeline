import { describe, expect, it } from "vitest";
import { mergeHistoryPage } from "../historyPagination";
import type { HistoryRecord } from "../types";

const record = (id: string, state: HistoryRecord["state"] = "successful"): HistoryRecord => ({
  id,
  method: "POST",
  path: "/v1/responses",
  state,
  attempt: 1,
  startedAt: "2026-08-19T00:00:00Z",
  completedAt: "2026-08-19T00:00:01Z",
  events: [],
});

describe("mergeHistoryPage", () => {
  it("keeps loaded older records while replacing refreshed records", () => {
    const current = [record("latest"), record("older"), record("oldest")];
    const fresh = [record("latest", "failed")];

    expect(mergeHistoryPage(current, fresh).map((item) => item.id)).toEqual(["latest", "older", "oldest"]);
    expect(mergeHistoryPage(current, fresh)[0].state).toBe("failed");
  });
});
