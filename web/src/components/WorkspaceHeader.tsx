import { CirclePause, CirclePlay, CircleStop, HeartPulse, Menu, RefreshCw, Wrench } from "lucide-react";
import { useTranslation } from "react-i18next";
import type {
  Alert, Config, HealthSummary, HistoryRecord, Incident, MetricsWindow, RequestInfo, SessionInfo, Status,
} from "../types";
import type { ApiClient } from "../api";
import type { View } from "./AppNavigation";
import { GlobalSearch } from "./GlobalSearch";
import type { SearchTarget } from "./GlobalSearch";
import { NotificationPopover } from "./NotificationPopover";

const windows: MetricsWindow[] = ["15m", "1h", "6h", "24h"];

export function WorkspaceHeader({
  api, config, view, status, healthSummary, session, requests, history, incidents, alerts, metricsWindow,
  canOperate, mobileToolsOpen, onWindowChange, onOpen, onNavigate, onRefresh, onPauseToggle, onDrain, onMaintenance, onMobileTools,
}: {
  api: ApiClient;
  config: Config;
  view: View;
  status: Status;
  healthSummary: HealthSummary | null;
  session: SessionInfo;
  requests: RequestInfo[];
  history: HistoryRecord[];
  incidents: Incident[];
  alerts: Alert[];
  metricsWindow: MetricsWindow;
  canOperate: boolean;
  mobileToolsOpen: boolean;
  onWindowChange: (window: MetricsWindow) => void;
  onOpen: (id: string) => void;
  onNavigate: (view: View, target?: SearchTarget) => void;
  onRefresh: () => void;
  onPauseToggle: () => void;
  onDrain: () => void;
  onMaintenance: () => void;
  onMobileTools: () => void;
}) {
  const { t } = useTranslation(["common", "overview"]);
  const upstreamLabel = status.upstream.state === "healthy" ? "upstreamHealthy" : status.upstream.state === "degraded" ? "upstreamDegraded" : "upstreamUnknown";
  const showWindow = view === "overview" || view === "history";

  return <header className="workspace-header">
    <div className="mobile-topbar"><span className="rail-brand"><HeartPulse size={17} /></span><div><strong>Relay-Lifeline</strong><span>{t(`common:nav.${view}`)}</span></div></div>
    <div className="desktop-heading">
      <span className="workspace-eyebrow">Relay-Lifeline / {t(`common:nav.${view}`)}</span>
      <div className="workspace-title-row"><h1>{t(`common:title.${view}`)}</h1><div className="health-row">
        <span className="connection"><i />{t("common:status.gatewayOnline")}</span>
        <span className={`connection upstream-${status.upstream.state}`}><i />{t(`common:status.${upstreamLabel}`)}</span>
        {healthSummary && <span className={`connection health-${healthSummary.overall}`}><i />{t(`common:health.${healthSummary.overall}`, { defaultValue: healthSummary.overall })}</span>}
        <span className="mode">{t(`common:roles.${session.role}`)}</span>
      </div></div>
    </div>

    <GlobalSearch api={api} upstream={config.upstream.baseUrl} canOperate={canOperate} requests={requests} history={history} incidents={incidents} alerts={alerts} onOpen={onOpen} onNavigate={onNavigate} />

    <div className="header-actions">
      {showWindow && <div className="header-window segmented-control" role="group" aria-label={t("overview:charts.window")}>
        {windows.map((value) => <button key={value} className={metricsWindow === value ? "active" : ""} aria-pressed={metricsWindow === value} onClick={() => onWindowChange(value)}>{value}</button>)}
      </div>}
      <NotificationPopover alerts={alerts} incidents={incidents} onOpenRequest={onOpen} onOpenIncident={(id) => onNavigate("incidents", { kind: "incident", id })} onOpenLogs={(event) => onNavigate("logs", { kind: "log", id: "", detail: event })} />
      <button id="mobile-tools-trigger" className="icon-button mobile-tools-toggle" aria-label={t("common:tools")} aria-expanded={mobileToolsOpen} aria-controls="mobile-tools-panel" data-tooltip={t("common:tools")} onClick={onMobileTools}><Menu size={17} /></button>
      <button className="icon-button header-refresh" aria-label={t("common:actions.refresh")} data-tooltip={t("common:actions.refresh")} onClick={onRefresh}><RefreshCw size={17} /></button>
      {canOperate && (status.mode === "draining" || status.mode === "maintenance" ? <button className="button primary pause-action" onClick={onPauseToggle}><CirclePlay size={17} /><span>{t("common:actions.resume")}</span></button> : <><button className={`button pause-action ${status.paused ? "primary" : ""}`} aria-label={status.paused ? t("common:actions.resume") : t("common:actions.pause")} onClick={onPauseToggle}>{status.paused ? <CirclePlay size={17} /> : <CirclePause size={17} />}<span>{status.paused ? t("common:actions.resume") : t("common:actions.pause")}</span></button><button className="button control-mode-action" onClick={onDrain}><CircleStop size={16} /><span>{t("common:actions.drain")}</span></button><button className="icon-button control-mode-action" aria-label={t("common:actions.maintenance")} data-tooltip={t("common:actions.maintenance")} onClick={onMaintenance}><Wrench size={16} /></button></>)}
    </div>
  </header>;
}
