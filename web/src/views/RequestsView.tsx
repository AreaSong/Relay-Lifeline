import { ChevronDown, Search, Settings2, TimerReset, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { errorMessage, type ApiClient } from "../api";
import type { ConfirmDialogState } from "../components/ConfirmDialog";
import { RequestsTable } from "../components/RequestsTable";
import { RepeatTaskDialog } from "../components/RepeatTaskDialog";
import { RepeatTasksPanel } from "../components/RepeatTasksPanel";
import { RetryPolicyDialog } from "../components/RetryPolicyDialog";
import { UncertainResolutionDialog } from "../components/UncertainResolutionDialog";
import type { MetricsSnapshot, RepeatTask, RequestInfo, RetryPolicyInput, Status } from "../types";

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;

const quickPolicies: Record<"standard" | "fast" | "conservative", RetryPolicyInput> = {
  standard: {
    durationMilliseconds: HOUR,
    schedule: { mode: "random", minimumIntervalMilliseconds: MINUTE, maximumIntervalMilliseconds: 2 * MINUTE },
    honorRetryAfter: true,
  },
  fast: {
    durationMilliseconds: 15 * MINUTE,
    schedule: { mode: "fixed", intervalMilliseconds: 15_000 },
    maxAdditionalAttempts: 20,
    honorRetryAfter: true,
  },
  conservative: {
    durationMilliseconds: 6 * HOUR,
    schedule: { mode: "exponential", baseIntervalMilliseconds: MINUTE, maximumIntervalMilliseconds: 15 * MINUTE },
    honorRetryAfter: true,
  },
};

function sparkline(values: number[]) {
  const maximum = Math.max(1, ...values);
  return values.map((value, index) => `${values.length <= 1 ? 0 : index / (values.length - 1) * 120},${28 - value / maximum * 24}`).join(" ");
}

function canRetry(request: RequestInfo) {
  return request.state !== "uncertain" && (request.actions?.canRetryNow ?? request.state === "waiting");
}

function canSetPolicy(request: RequestInfo) {
  return request.state !== "uncertain" && (request.actions?.canSetRetryPolicy ?? ["queued", "requesting", "waiting"].includes(request.state));
}

function hasRetryPolicy(request: RequestInfo) {
  if (request.retryPolicy || (request.retryIntervalMilliseconds || 0) > 0) return true;
  if (!request.retryDeadline) return false;
  const deadline = new Date(request.retryDeadline);
  return !Number.isNaN(deadline.getTime()) && deadline.getUTCFullYear() > 1;
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
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [policyTargets, setPolicyTargets] = useState<RequestInfo[] | null>(null);
  const [repeatRequest, setRepeatRequest] = useState<RequestInfo | null>(null);
  const [uncertainRequest, setUncertainRequest] = useState<RequestInfo | null>(null);
  const [busy, setBusy] = useState(false);
  const requests = useMemo(() => status.requests.filter((request) => {
    const matchesState = state === "all" || request.state === state;
    const needle = query.trim().toLowerCase();
    return matchesState && (!needle || [request.id, request.path, request.clientId, request.taskId].some((value) => value?.toLowerCase().includes(needle)));
  }), [query, state, status.requests]);
  const selectedRequests = useMemo(() => status.requests.filter((request) => selected.has(request.id)), [selected, status.requests]);
  const visibleSelectedRequests = useMemo(() => selectedRequests.filter((request) => requests.some((visible) => visible.id === request.id)), [requests, selectedRequests]);
  const scopeRequests = visibleSelectedRequests.length ? visibleSelectedRequests : requests;
  const livePolicyTargets = useMemo(() => policyTargets?.map((target) => status.requests.find((request) => request.id === target.id) || target) || null, [policyTargets, status.requests]);
  const liveRepeatRequest = useMemo(() => repeatRequest ? status.requests.find((request) => request.id === repeatRequest.id) || repeatRequest : null, [repeatRequest, status.requests]);
  const liveUncertainRequest = useMemo(() => {
    if (!uncertainRequest) return null;
    const live = status.requests.find((request) => request.id === uncertainRequest.id);
    return live?.state === "uncertain" ? live : null;
  }, [uncertainRequest, status.requests]);
  const uncertainRequests = useMemo(() => requests.filter((request) => request.state === "uncertain"), [requests]);
  const retryable = scopeRequests.filter(canRetry);
  const policyCapable = scopeRequests.filter(canSetPolicy);
  const pressure = (metrics?.series || []).slice(-30).map((point) => point.active + point.waiting + point.queued);

  useEffect(() => {
    const live = new Set(status.requests.map((request) => request.id));
    setSelected((current) => {
      const next = new Set([...current].filter((id) => live.has(id)));
      return next.size === current.size ? current : next;
    });
  }, [status.requests]);

  function toggle(id: string) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  }

  function toggleAll() {
    setSelected((current) => {
      const next = new Set(current);
      if (requests.every((request) => next.has(request.id))) requests.forEach((request) => next.delete(request.id));
      else requests.forEach((request) => next.add(request.id));
      return next;
    });
  }

  async function retryTargets(targets: RequestInfo[]) {
    const eligible = targets.filter(canRetry);
    if (!eligible.length || busy) return;
    if (eligible.length > 20 && !await confirm({
      title: t("requests:batch.retryConfirmTitle"),
      description: t("requests:batch.retryManyConfirm", { count: eligible.length }),
      confirmLabel: t("requests:batch.retryAction"), tone: "default",
    })) return;
    setBusy(true);
    try {
      let accepted = 0;
      let skipped = 0;
      if (eligible.length === 1) {
        const response = await api.retry(eligible[0].id);
        accepted = response.accepted ? 1 : 0;
        skipped = response.accepted ? 0 : 1;
      }
      else {
        const response = await api.batchRetry(eligible.map((request) => request.id));
        accepted = response.accepted;
        skipped = response.skipped;
      }
      await refresh();
      onSuccess(t("requests:batch.retryResult", { accepted, skipped }));
    } catch (reason) {
      onError(errorMessage(reason));
    } finally {
      setBusy(false);
    }
  }

  async function applyQuickPolicy(name: keyof typeof quickPolicies) {
    if (!policyCapable.length || busy) return;
    if (!await confirm({
      title: t("requests:retryPolicy.quickConfirmTitle"),
      description: t("requests:retryPolicy.quickConfirmDescription", { count: policyCapable.length, preset: t(`requests:retryPolicy.presets.${name}`) }),
      confirmLabel: t("requests:retryPolicy.apply"), tone: "default",
    })) return;
    setBusy(true);
    try {
      let accepted = 0;
      if (policyCapable.length === 1) accepted = (await api.setRetryPolicy(policyCapable[0].id, quickPolicies[name])).accepted ? 1 : 0;
      else accepted = (await api.batchRetryPolicy(policyCapable.map((request) => request.id), { policy: quickPolicies[name], overwrite: true })).accepted;
      await refresh();
      onSuccess(t("requests:retryPolicy.applied", { accepted, total: policyCapable.length }));
    } catch (reason) {
      onError(errorMessage(reason));
    } finally {
      setBusy(false);
    }
  }

  async function resetPolicies() {
    const targets = policyCapable.filter(hasRetryPolicy);
    if (!targets.length || busy) return;
    setBusy(true);
    try {
      let accepted = 0;
      if (targets.length === 1) accepted = (await api.clearRetryPolicy(targets[0].id)).accepted ? 1 : 0;
      else accepted = (await api.batchRetryPolicy(targets.map((request) => request.id), { reset: true })).accepted;
      await refresh();
      onSuccess(t("requests:retryPolicy.reset", { accepted, total: targets.length }));
    } catch (reason) {
      onError(errorMessage(reason));
    } finally {
      setBusy(false);
    }
  }

  async function cancelRequest(request: RequestInfo) {
    if (busy || request.state === "uncertain") return;
    setBusy(true);
    try { await api.cancel(request.id); await refresh(); }
    catch (reason) { onError(errorMessage(reason)); }
    finally { setBusy(false); }
  }

  return <div className="page-stack requests-view">
    <div className="request-summary" aria-label={t("overview:queue.title")}>
      {(["active", "requesting", "waiting", "queued"] as const).map((key) => <div key={key}>
        <span>{t(`requests:summary.${key}`)}</span><strong>{status[key]}</strong>
      </div>)}
      <div className="request-pressure"><span>{t("overview:charts.pressure")}</span><svg viewBox="0 0 120 32" preserveAspectRatio="none" role="img" aria-label={t("overview:charts.pressure")}><title>{t("overview:charts.pressure")}</title><polyline points={sparkline(pressure.length ? pressure : [0, 0])} /></svg></div>
    </div>
    <section className="content-section">
      <div className="section-heading request-heading"><div><h2>{t("overview:queue.title")}</h2><p>{t("overview:queue.description")}</p></div><div className="request-heading-meta"><span>{t("common:requestCount", { count: requests.length })}</span><span className="retryable-count">{t("requests:batch.retryableCount", { count: retryable.length })}</span>{uncertainRequests.length > 0 && <span className="uncertain-count">{t("requests:uncertain.count", { count: uncertainRequests.length })}</span>}</div></div>
      <div className="request-toolbar">
        <div className="filter-controls">
          <label className="search-field"><span className="sr-only">{t("requests:filters.search")}</span><Search size={16} /><input value={query} placeholder={t("requests:filters.search")} onChange={(event) => setQuery(event.target.value)} /></label>
          <label><span className="sr-only">{t("requests:filters.state")}</span><select className="compact-select" value={state} onChange={(event) => setState(event.target.value)}>
            <option value="all">{t("requests:filters.allStates")}</option>
            {["queued", "waiting", "requesting", "uncertain", "buffering", "delivering"].map((value) => <option key={value} value={value}>{t(`common:status.${value}`, { defaultValue: value })}</option>)}
          </select></label>
        </div>
        {canOperate && <div className="batch-controls">
          {selectedRequests.length > 0 && <span className="selection-count">{t("requests:batch.selected", { count: selectedRequests.length })}<button className="icon-button compact" onClick={() => setSelected(new Set())} aria-label={t("requests:batch.clearSelection")}><X size={14} /></button></span>}
          <button className="button compact" disabled={busy || !retryable.length} onClick={() => retryTargets(scopeRequests)}><TimerReset size={15} />{t("requests:batch.retry", { count: retryable.length })}</button>
          <details className="policy-quick-menu"><summary className={`button compact ${!policyCapable.length || busy ? "disabled" : ""}`} aria-disabled={!policyCapable.length || busy}><Settings2 size={15} />{t("requests:batch.policy")}<ChevronDown size={14} /></summary><div className="policy-quick-popover">
            {(["standard", "fast", "conservative"] as const).map((preset) => <button key={preset} disabled={!policyCapable.length || busy} onClick={(event) => { event.currentTarget.closest("details")?.removeAttribute("open"); void applyQuickPolicy(preset); }}>{t(`requests:retryPolicy.presets.${preset}`)}</button>)}
            <button disabled={!policyCapable.length || busy} onClick={(event) => { event.currentTarget.closest("details")?.removeAttribute("open"); setPolicyTargets([...policyCapable]); }}>{t("requests:retryPolicy.presets.custom")}</button>
            <button disabled={!policyCapable.some(hasRetryPolicy) || busy} onClick={(event) => { event.currentTarget.closest("details")?.removeAttribute("open"); void resetPolicies(); }}>{t("requests:retryPolicy.resetAction")}</button>
          </div></details>
        </div>}
      </div>
      <RequestsTable requests={requests} selected={selected} onToggle={toggle} onToggleAll={toggleAll} onOpen={onOpen} canOperate={canOperate} onRetry={(request) => retryTargets([request])} onPolicy={(request) => setPolicyTargets([request])} onRepeat={(request) => { if (request.state !== "uncertain") setRepeatRequest(request); }} onCancel={cancelRequest} onResolveUncertain={(request) => { if (request.state === "uncertain") setUncertainRequest(request); }} />
    </section>
    <RepeatTasksPanel tasks={repeatTasks} api={api} refresh={refresh} onError={onError} canOperate={canOperate} />
    {livePolicyTargets && <RetryPolicyDialog targets={livePolicyTargets} api={api} refresh={refresh} onClose={() => setPolicyTargets(null)} onError={onError} onSuccess={onSuccess} confirm={confirm} />}
    {liveRepeatRequest && <RepeatTaskDialog request={liveRepeatRequest} api={api} refresh={refresh} onClose={() => setRepeatRequest(null)} onError={onError} onSuccess={onSuccess} confirm={confirm} />}
    {liveUncertainRequest && <UncertainResolutionDialog request={liveUncertainRequest} api={api} refresh={refresh} onClose={() => setUncertainRequest(null)} onError={onError} onSuccess={onSuccess} canOperate={canOperate} />}
  </div>;
}
