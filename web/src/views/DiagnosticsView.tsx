import { CircleCheck, CircleX, Cpu, Download, MemoryStick, Play, Route, Stethoscope, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import { formatBytes, formatSeconds, formatTime } from "../format";
import type { DiagnosticCheck, DiagnosticReport, RuntimeInfo } from "../types";

function CheckIcon({ status }: { status: DiagnosticCheck["status"] }) {
  if (status === "pass") return <CircleCheck />;
  if (status === "warn") return <TriangleAlert />;
  return <CircleX />;
}

export function DiagnosticsView({ runtimeInfo, report, busy, run, download, canOperate = true }: {
  runtimeInfo: RuntimeInfo | null;
  report: DiagnosticReport | null;
  busy: boolean;
  run: () => void;
  download: () => void;
  canOperate?: boolean;
}) {
  const { t } = useTranslation("diagnostics");
  return <section className="content-section"><div className="section-heading diagnostics-heading"><div><h2>{t("title")}</h2><p>{t("description")}</p></div><div className="header-actions">
    <button className="button" onClick={download}><Download size={17} />{t("export")}</button>
    {canOperate && <button className="button primary" disabled={busy} onClick={run}><Play size={17} />{busy ? t("running") : t("start")}</button>}
  </div></div>
    {runtimeInfo?.process && <section className="process-snapshot" aria-labelledby="process-snapshot-title">
      <div className="section-heading"><div><h2 id="process-snapshot-title">{t("process.title")}</h2><p>{t("process.description")}</p></div><span className="process-sampled">{t("process.sampledAt", { time: formatTime(runtimeInfo.process.sampledAt) })}</span></div>
      <div className="process-grid">
        <div><Cpu size={18} /><span>{t("process.pid")}</span><strong>{runtimeInfo.process.pid}</strong><small>{t("process.parentPid", { pid: runtimeInfo.process.parentPid })}</small></div>
        <div><Cpu size={18} /><span>{t("process.scheduler")}</span><strong>{runtimeInfo.process.goroutines}</strong><small>{t("process.schedulerDetail", { cpu: runtimeInfo.process.cpuCount, gomaxprocs: runtimeInfo.process.gomaxprocs })}</small></div>
        <div><MemoryStick size={18} /><span>{t("process.heap")}</span><strong>{formatBytes(runtimeInfo.process.heapAllocBytes)}</strong><small>{t("process.heapInuse", { value: formatBytes(runtimeInfo.process.heapInuseBytes) })}</small></div>
        <div><MemoryStick size={18} /><span>{t("process.systemMemory")}</span><strong>{formatBytes(runtimeInfo.process.systemMemoryBytes)}</strong><small>{t("process.gcCycles", { count: runtimeInfo.process.gcCycles })}</small></div>
      </div>
    </section>}
    {!report ? <div className="empty-state"><Stethoscope size={25} /><span>{t("empty")}</span></div> : <>
      <div className={`diagnostic-summary ${report.healthy ? "healthy" : "unhealthy"}`}><strong>{report.healthy ? t("healthy") : t("unhealthy")}</strong><span>{t("summary", { version: report.version, uptime: formatSeconds(report.uptimeSeconds) })}</span></div>
      <div className="section-heading"><div><h2>{t("pathTitle")}</h2><p>{t("pathDescription")}</p></div><Route size={18} /></div>
      <div className="diagnostic-path" role="list">{report.checks.map((check) => <div className={`diagnostic-node ${check.status}`} role="listitem" key={`path-${check.id}`}><CheckIcon status={check.status} /><strong>{check.name}</strong></div>)}</div>
      <div className="check-list">{report.checks.map((check) => <div className={`check-row ${check.status}`} key={check.id}>
        <span><CheckIcon status={check.status} /></span><div><strong>{check.name}</strong><p>{check.message}</p></div>
      </div>)}</div>
    </>}
  </section>;
}
