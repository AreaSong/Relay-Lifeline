import { CircleCheck, CircleX, Download, Play, Route, Stethoscope, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import { formatSeconds } from "../format";
import type { DiagnosticCheck, DiagnosticReport } from "../types";

function CheckIcon({ status }: { status: DiagnosticCheck["status"] }) {
  if (status === "pass") return <CircleCheck />;
  if (status === "warn") return <TriangleAlert />;
  return <CircleX />;
}

export function DiagnosticsView({ report, busy, run, download, canOperate = true }: {
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
