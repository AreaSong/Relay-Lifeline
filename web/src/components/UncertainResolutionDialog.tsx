import { ArrowLeft, CheckCircle2, ShieldAlert, X } from "lucide-react";
import { FormEvent, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { errorMessage, type ApiClient } from "../api";
import { formatBytes, formatOptionalTime } from "../format";
import type { RequestInfo, UncertainPreview, UncertainResolutionAction } from "../types";

const actions: UncertainResolutionAction[] = ["confirm_success", "abandon", "request_compensation"];

function valueOrFallback(value: string | number | undefined, fallback: string) {
  return value === undefined || value === "" ? fallback : String(value);
}

function formatRequestBytes(value: number | undefined, fallback: string) {
  return value === undefined ? fallback : formatBytes(Math.max(0, value));
}

function formatLatency(value: number | undefined, fallback: string) {
  return value === undefined ? fallback : `${Math.max(0, value)} ms`;
}

export function UncertainResolutionDialog({ request, api, refresh, onClose, onError, onSuccess, canOperate = true }: {
  request: RequestInfo;
  api: ApiClient;
  refresh: () => Promise<void>;
  onClose: () => void;
  onError: (message: string) => void;
  onSuccess: (message: string) => void;
  canOperate?: boolean;
}) {
  const { t, i18n } = useTranslation(["common", "requests"]);
  const dialog = useRef<HTMLDivElement>(null);
  const [stage, setStage] = useState<"action" | "confirm">("action");
  const [action, setAction] = useState<UncertainResolutionAction>("confirm_success");
  const [preview, setPreview] = useState<UncertainPreview | null>(null);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const fallback = t("common:notAvailable");

  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.current?.querySelector<HTMLElement>("button, textarea")?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") { event.preventDefault(); onClose(); return; }
      if (event.key !== "Tab" || !dialog.current) return;
      const focusable = Array.from(dialog.current.querySelectorAll<HTMLElement>('button, textarea, [tabindex]:not([tabindex="-1"])')).filter((element) => !element.hasAttribute("disabled"));
      if (!focusable.length) return;
      if (event.shiftKey && document.activeElement === focusable[0]) { event.preventDefault(); focusable[focusable.length - 1].focus(); }
      else if (!event.shiftKey && document.activeElement === focusable[focusable.length - 1]) { event.preventDefault(); focusable[0].focus(); }
    };
    document.addEventListener("keydown", keydown, true);
    return () => { document.removeEventListener("keydown", keydown, true); previous?.focus(); };
  }, [onClose, stage]);

  async function previewAction(event: FormEvent) {
    event.preventDefault();
    if (!canOperate || busy) return;
    setBusy(true);
    try {
      const previewFn = (api as Partial<ApiClient>).previewUncertainResolution || (api as Partial<ApiClient>).previewUncertain || (api as Partial<ApiClient>).previewUncertainDelivery;
      if (!previewFn) throw new Error(t("requests:uncertain.unavailable"));
      const next = await previewFn.call(api, request.id, action);
      setPreview(next);
      setStage("confirm");
    } catch (error) {
      onError(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }

  async function resolve(event: FormEvent) {
    event.preventDefault();
    const trimmed = reason.trim();
    if (!preview || !trimmed || !canOperate || busy) {
      if (!trimmed) onError(t("requests:uncertain.reasonRequired"));
      return;
    }
    setBusy(true);
    try {
      const resolveFn = ((api as Partial<ApiClient>).resolveUncertain || (api as Partial<ApiClient>).resolveUncertainDelivery) as ((id: string, input: { action: UncertainResolutionAction; confirmationToken: string; reason: string }) => Promise<unknown>) | undefined;
      if (!resolveFn) throw new Error(t("requests:uncertain.unavailable"));
      await resolveFn.call(api, request.id, { action, confirmationToken: preview.confirmationToken, reason: trimmed });
      await refresh();
      onSuccess(t(`requests:uncertain.success.${action}`));
      onClose();
    } catch (error) {
      onError(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }

  const evidence = preview?.evidence;
  return <div className="confirm-backdrop" role="presentation" onMouseDown={onClose}>
    <div ref={dialog} className="repeat-dialog uncertain-dialog" role="dialog" aria-modal="true" aria-labelledby="uncertain-dialog-title" onMouseDown={(event) => event.stopPropagation()}>
      <header>
        <span className="confirm-icon"><ShieldAlert size={20} /></span>
        <div><h2 id="uncertain-dialog-title">{t("requests:uncertain.title")}</h2><p>{request.method} {request.path} · {request.id}</p></div>
        <button className="icon-button" type="button" aria-label={t("common:actions.close")} data-tooltip={t("common:actions.close")} onClick={onClose}><X size={17} /></button>
      </header>
      {!canOperate ? <div className="uncertain-readonly"><strong>{t("requests:uncertain.readOnlyTitle")}</strong><p>{t("requests:uncertain.readOnlyDescription")}</p></div> : stage === "action" ? <form onSubmit={previewAction}>
        <p className="repeat-explanation">{t("requests:uncertain.description")}</p>
        <div className="uncertain-action-picker" role="group" aria-label={t("requests:uncertain.actionLabel")}>
          {actions.map((candidate) => <button type="button" key={candidate} className={action === candidate ? "active" : ""} aria-pressed={action === candidate} onClick={() => setAction(candidate)}><strong>{t(`requests:uncertain.actions.${candidate}.label`)}</strong><span>{t(`requests:uncertain.actions.${candidate}.description`)}</span></button>)}
        </div>
        <div className="uncertain-warning"><ShieldAlert size={17} /><span>{t("requests:uncertain.previewWarning")}</span></div>
        <footer><button type="button" className="button" onClick={onClose}>{t("common:actions.cancelDialog")}</button><button className="button primary" disabled={busy}>{busy ? t("common:loading") : t("requests:uncertain.previewAction")}</button></footer>
      </form> : <form onSubmit={resolve}>
        <div className="uncertain-stage-heading"><button type="button" className="link-button" onClick={() => setStage("action")}><ArrowLeft size={14} />{t("requests:uncertain.changeAction")}</button><span>{t(`requests:uncertain.actions.${action}.label`)}</span></div>
        {preview && <EvidencePanel preview={preview} fallback={fallback} locale={i18n.resolvedLanguage || "en-US"} t={t} />}
        <label className="field uncertain-reason"><span>{t("requests:uncertain.reason")}</span><textarea value={reason} maxLength={500} required rows={3} placeholder={t("requests:uncertain.reasonPlaceholder")} onChange={(event) => setReason(event.target.value)} /></label>
        <p className="uncertain-confirm-note"><CheckCircle2 size={16} />{t("requests:uncertain.confirmNote")}</p>
        <footer><button type="button" className="button" onClick={onClose}>{t("common:actions.cancelDialog")}</button><button className={`button ${action === "confirm_success" ? "primary" : "danger"}`} disabled={busy || !reason.trim()}>{busy ? t("common:loading") : t(`requests:uncertain.actions.${action}.confirm`)}</button></footer>
      </form>}
    </div>
  </div>;
}

function EvidencePanel({ preview, fallback, locale, t }: {
  preview: UncertainPreview;
  fallback: string;
  locale: string;
  t: (key: string, options?: Record<string, unknown>) => string;
}) {
  const evidence = preview.evidence;
  const field = (name: string) => t(`requests:uncertain.fields.${name}`);
  return <section className="uncertain-evidence" aria-label={t("requests:uncertain.evidenceTitle")}>
    <div className="uncertain-evidence-meta"><dl>
      <div><dt>{field("state")}</dt><dd>{evidence.state}</dd></div>
      <div><dt>{field("attempt")}</dt><dd>{evidence.attempt}</dd></div>
      <div><dt>{field("started")}</dt><dd>{formatOptionalTime(evidence.startedAt, locale, fallback)}</dd></div>
      <div><dt>{field("uncertainSince")}</dt><dd>{formatOptionalTime(evidence.uncertainSince, locale, fallback)}</dd></div>
    </dl><small>{t("requests:uncertain.expires", { time: formatOptionalTime(preview.expiresAt, locale, fallback) })}</small></div>
    <div className="uncertain-attempts"><div className="uncertain-attempts-heading"><strong>{t("requests:uncertain.attemptEvidence")}</strong><span>{t("requests:uncertain.hashNotice")}</span></div>
      {evidence.attempts.length ? <EvidenceAttemptTable attempts={evidence.attempts} fallback={fallback} t={t} /> : <p className="uncertain-empty-evidence">{t("requests:uncertain.noAttemptEvidence")}</p>}
    </div>
  </section>;
}

function EvidenceAttemptTable({ attempts, fallback, t }: {
  attempts: UncertainPreview["evidence"]["attempts"];
  fallback: string;
  t: (key: string, options?: Record<string, unknown>) => string;
}) {
  const columns = ["attempt", "target", "domain", "wrote", "hash", "bytes", "latency", "statusCode", "phase", "category", "upstreamRequestId"];
  return <div className="table-wrap"><table><thead><tr>{columns.map((column) => <th key={column}>{t(`requests:uncertain.fields.${column}`)}</th>)}</tr></thead><tbody>{attempts.map((attempt, index) => <EvidenceAttemptRow key={`${attempt.attempt}-${index}`} attempt={attempt} fallback={fallback} t={t} />)}</tbody></table></div>;
}

function EvidenceAttemptRow({ attempt, fallback, t }: {
  attempt: UncertainPreview["evidence"]["attempts"][number];
  fallback: string;
  t: (key: string, options?: Record<string, unknown>) => string;
}) {
  const label = (name: string) => t(`requests:uncertain.fields.${name}`);
  const text = (name: string, value: string | number | undefined) => <td data-label={label(name)}>{valueOrFallback(value, fallback)}</td>;
  const code = (name: string, value: string | number | undefined) => <td data-label={label(name)}><code>{valueOrFallback(value, fallback)}</code></td>;
  return <tr>
    {text("attempt", attempt.attempt)}
    {code("target", attempt.targetId)}
    {code("domain", attempt.targetDomain)}
    <td data-label={label("wrote")}>{attempt.wroteRequest ? t("requests:uncertain.yes") : t("requests:uncertain.no")}</td>
    {code("hash", attempt.idempotencyKeyHash)}
    <td data-label={label("bytes")}>{formatRequestBytes(attempt.requestBytes, fallback)}</td>
    <td data-label={label("latency")}>{formatLatency(attempt.latencyMilliseconds, fallback)}</td>
    {text("statusCode", attempt.statusCode)}
    {code("phase", attempt.attemptPhase)}
    {code("category", attempt.category)}
    {code("upstreamRequestId", attempt.upstreamRequestId)}
  </tr>;
}
