import { Repeat2, ShieldAlert } from "lucide-react";
import { FormEvent, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ApiClient } from "../api";
import { errorMessage } from "../api";
import type { ConfirmDialogState } from "./ConfirmDialog";
import type { RequestInfo } from "../types";

export function RepeatTaskDialog({ request, api, refresh, onClose, onError, onSuccess, confirm }: {
  request: RequestInfo;
  api: ApiClient;
  refresh: () => Promise<void>;
  onClose: () => void;
  onError: (message: string) => void;
  onSuccess: (message: string) => void;
  confirm: (state: ConfirmDialogState) => Promise<boolean>;
}) {
  const { t } = useTranslation(["common", "requests"]);
  const dialog = useRef<HTMLDivElement>(null);
  const [interval, setIntervalValue] = useState("60s");
  const [duration, setDuration] = useState("1h");
  const [idempotency, setIdempotency] = useState<"preserve" | "regenerate">("preserve");
  const [forever, setForever] = useState(false);
	const [maxExecutions, setMaxExecutions] = useState(100);
	const [maxFailures, setMaxFailures] = useState(20);
	const [failureThreshold, setFailureThreshold] = useState(5);
	const [maxTokens, setMaxTokens] = useState(100000);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.current?.querySelector<HTMLButtonElement>("button")?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
      if (event.key !== "Tab" || !dialog.current) return;
      const focusable = Array.from(dialog.current.querySelectorAll<HTMLElement>('button, select, input, [tabindex]:not([tabindex="-1"])')).filter((element) => !element.hasAttribute("disabled"));
      if (!focusable.length) return;
      if (event.shiftKey && document.activeElement === focusable[0]) { event.preventDefault(); focusable[focusable.length - 1].focus(); }
      else if (!event.shiftKey && document.activeElement === focusable[focusable.length - 1]) { event.preventDefault(); focusable[0].focus(); }
    };
    window.addEventListener("keydown", keydown);
    return () => { window.removeEventListener("keydown", keydown); previous?.focus(); };
  }, [onClose]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!await confirm({
      title: t("requests:repeat.confirmTitle"), description: t("requests:repeat.confirmDescription"),
      confirmLabel: t("requests:repeat.confirmAction"), tone: forever || idempotency === "regenerate" ? "danger" : "default",
    })) return;
    setBusy(true);
    try {
		await api.createRepeatTask(request.id, {
			interval, duration: forever ? "" : duration, idempotency, confirmForever: forever,
			maxExecutions, maxFailures, failureThreshold, maxTokens,
		});
      await refresh();
      onSuccess(t("requests:repeat.taskCreated"));
      onClose();
    } catch (reason) {
      onError(errorMessage(reason));
    } finally {
      setBusy(false);
    }
  }

  return <div className="confirm-backdrop" role="presentation" onMouseDown={onClose}>
    <div ref={dialog} className="repeat-dialog" role="dialog" aria-modal="true" aria-labelledby="repeat-dialog-title" onMouseDown={(event) => event.stopPropagation()}>
      <header><span className="confirm-icon"><Repeat2 size={20} /></span><div><h2 id="repeat-dialog-title">{t("requests:repeat.title")}</h2><p>{request.method} {request.path} · {request.id}</p></div></header>
      <form onSubmit={submit}>
        <p className="repeat-explanation">{t("requests:repeat.repeatDescription")}</p>
        <div className="form-grid">
          <label className="field"><span>{t("requests:repeat.interval")}</span><select value={interval} onChange={(event) => setIntervalValue(event.target.value)}>{["5s", "15s", "30s", "60s", "5m", "15m"].map((value) => <option key={value} value={value}>{t(`requests:repeat.values.${value}`)}</option>)}</select></label>
          <label className="field"><span>{t("requests:repeat.duration")}</span><select value={duration} disabled={forever} onChange={(event) => setDuration(event.target.value)}>{["5m", "15m", "30m", "1h", "6h", "24h"].map((value) => <option key={value} value={value}>{t(`requests:repeat.values.${value}`)}</option>)}</select></label>
          <label className="field wide"><span>{t("requests:repeat.idempotency")}</span><select value={idempotency} onChange={(event) => setIdempotency(event.target.value as "preserve" | "regenerate")}><option value="preserve">{t("requests:repeat.preserve")}</option><option value="regenerate">{t("requests:repeat.regenerate")}</option></select></label>
			<label className="field"><span>{t("requests:repeat.maxExecutions")}</span><input type="number" min="1" max="100000" value={maxExecutions} onChange={(event) => setMaxExecutions(Number(event.target.value))} required /></label>
			<label className="field"><span>{t("requests:repeat.maxFailures")}</span><input type="number" min="1" max="100000" value={maxFailures} onChange={(event) => setMaxFailures(Number(event.target.value))} required /></label>
			<label className="field wide"><span>{t("requests:repeat.failureThreshold")}</span><input type="number" min="1" max={maxFailures} value={failureThreshold} onChange={(event) => setFailureThreshold(Number(event.target.value))} required /></label>
			<label className="field wide"><span>{t("requests:repeat.maxTokens")}</span><input type="number" min="1" max="1000000000000" value={maxTokens} onChange={(event) => setMaxTokens(Number(event.target.value))} required /> <small className="field-hint">{t("requests:repeat.maxTokensHint")}</small></label>
        </div>
        <label className="repeat-checkbox"><input type="checkbox" checked={forever} onChange={(event) => setForever(event.target.checked)} /><span>{t("requests:repeat.forever")}</span></label>
        <div className="repeat-warning"><ShieldAlert size={17} /><span>{t("requests:repeat.warning")}</span></div>
        <footer><button type="button" className="button" onClick={onClose}>{t("common:actions.cancelDialog")}</button><button className="button primary" disabled={busy}>{busy ? t("common:loading") : t("requests:repeat.apply")}</button></footer>
      </form>
    </div>
  </div>;
}
