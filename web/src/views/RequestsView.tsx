import { Search } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ApiClient } from "../api";
import { RequestsTable } from "../components/RequestsTable";
import { RepeatTaskDialog } from "../components/RepeatTaskDialog";
import { RepeatTasksPanel } from "../components/RepeatTasksPanel";
import type { ConfirmDialogState } from "../components/ConfirmDialog";
import type { MetricsSnapshot, RepeatTask, RequestInfo, Status } from "../types";

function sparkline(values: number[]) {
  const maximum = Math.max(1, ...values);
  return values.map((value, index) => `${values.length <= 1 ? 0 : index / (values.length - 1) * 120},${28 - value / maximum * 24}`).join(" ");
}

export function RequestsView({ status, metrics, repeatTasks, api, refresh, onOpen, onError, onSuccess, canOperate, confirm }: {
  status: Status;
  metrics: MetricsSnapshot | null;
  repeatTasks: RepeatTask[];
  api: ApiClient;
  refresh: () => Promise<void>;
  onOpen: (id: string) => void;
  onError: (message: string) => void;
  onSuccess: (message: string) => void;
  canOperate: boolean;
  confirm: (state: ConfirmDialogState) => Promise<boolean>;
}) {
  const { t } = useTranslation(["common", "overview", "requests"]);
  const [query, setQuery] = useState("");
  const [state, setState] = useState("all");
  const [policyRequest, setPolicyRequest] = useState<RequestInfo | null>(null);
  const requests = useMemo(() => status.requests.filter((request) => {
    const matchesState = state === "all" || request.state === state;
    const needle = query.trim().toLowerCase();
    return matchesState && (!needle || [request.id, request.path, request.clientId, request.taskId].some((value) => value?.toLowerCase().includes(needle)));
  }), [query, state, status.requests]);
  const pressure = (metrics?.series || []).slice(-30).map((point) => point.active + point.waiting + point.queued);

  return <div className="page-stack requests-view">
    <div className="request-summary" aria-label={t("overview:queue.title")}>
      {(["active", "requesting", "waiting", "queued"] as const).map((key) => <div key={key}>
        <span>{t(`requests:summary.${key}`)}</span><strong>{status[key]}</strong>
      </div>)}
      <div className="request-pressure"><span>{t("overview:charts.pressure")}</span><svg viewBox="0 0 120 32" preserveAspectRatio="none" role="img" aria-label={t("overview:charts.pressure")}><title>{t("overview:charts.pressure")}</title><polyline points={sparkline(pressure.length ? pressure : [0, 0])} /></svg></div>
    </div>
    <section className="content-section">
      <div className="section-heading"><div><h2>{t("overview:queue.title")}</h2><p>{t("overview:queue.description")}</p></div><span>{t("common:requestCount", { count: requests.length })}</span></div>
      <div className="request-toolbar">
        <div className="filter-controls">
          <label className="search-field"><span className="sr-only">{t("requests:filters.search")}</span><Search size={16} /><input value={query} placeholder={t("requests:filters.search")} onChange={(event) => setQuery(event.target.value)} /></label>
          <label><span className="sr-only">{t("requests:filters.state")}</span><select className="compact-select" value={state} onChange={(event) => setState(event.target.value)}>
            <option value="all">{t("requests:filters.allStates")}</option>
            {["queued", "waiting", "requesting"].map((value) => <option key={value} value={value}>{t(`common:status.${value}`)}</option>)}
          </select></label>
        </div>
      </div>
      <RequestsTable requests={requests} api={api} refresh={refresh} onOpen={onOpen} onError={onError} canOperate={canOperate} onPolicy={setPolicyRequest} />
    </section>
    <RepeatTasksPanel tasks={repeatTasks} api={api} refresh={refresh} onError={onError} canOperate={canOperate} />
    {policyRequest && <RepeatTaskDialog request={policyRequest} api={api} refresh={refresh} onClose={() => setPolicyRequest(null)} onError={onError} onSuccess={onSuccess} confirm={confirm} />}
  </div>;
}
