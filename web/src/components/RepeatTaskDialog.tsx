import { Clock3, Repeat2, ShieldAlert } from "lucide-react";
import { FormEvent, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ApiClient } from "../api";
import { errorMessage } from "../api";
import type { ConfirmDialogState } from "./ConfirmDialog";
import type { RequestInfo } from "../types";

type Mode = "retry" | "repeat";

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
  const [mode, setMode] = useState<Mode>("retry");
  const [interval, setIntervalValue] = useState("60s");
  const [duration, setDuration] = useState("1h");
  const [idempotency, setIdempotency] = useState<"preserve" | "regenerate">("preserve");
  const [forever, setForever] = useState(false);
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
    const risky = mode === "repeat" && (forever || idempotency === "regenerate");
    if (risky && !await confirm({
      title: t("requests:repeat.confirmTitle"), description: t("requests:repeat.confirmDescription"),
      confirmLabel: t("requests:repeat.confirmAction"), tone: "danger",
    })) return;
    setBusy(true);
    try {
      if (mode === "retry") await api.setRetryPolicy(request.id, duration, interval);
      else await api.createRepeatTask(request.id, { interval, duration: forever ? "" : duration, idempotency, confirmForever: forever });
      await refresh();
      onSuccess(t(mode === "retry" ? "requests:repeat.retryCreated" : "requests:repeat.taskCreated"));
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
        <div className="segmented-control repeat-mode" aria-label={t("requests:repeat.mode")}>
          <button type="button" className={mode === "retry" ? "active" : ""} onClick={() => setMode("retry")}><Clock3 size={15} />{t("requests:repeat.retryMode")}</button>
          <button type="button" className={mode === "repeat" ? "active" : ""} onClick={() => setMode("repeat")}><Repeat2 size={15} />{t("requests:repeat.repeatMode")}</button>
        </div>
        <p className="repeat-explanation">{t(mode === "retry" ? "requests:repeat.retryDescription" : "requests:repeat.repeatDescription")}</p>
        <div className="form-grid">
          <label className="field"><span>{t("requests:repeat.interval")}</span><select value={interval} onChange={(event) => setIntervalValue(event.target.value)}>{["5s", "15s", "30s", "60s", "5m", "15m"].map((value) => <option key={value} value={value}>{t(`requests:repeat.values.${value}`)}</option>)}</select></label>
          <label className="field"><span>{t("requests:repeat.duration")}</span><select value={duration} disabled={mode === "repeat" && forever} onChange={(event) => setDuration(event.target.value)}>{["5m", "15m", "30m", "1h", "6h", "24h"].map((value) => <option key={value} value={value}>{t(`requests:repeat.values.${value}`)}</option>)}</select></label>
          {mode === "repeat" && <label className="field wide"><span>{t("requests:repeat.idempotency")}</span><select value={idempotency} onChange={(event) => setIdempotency(event.target.value as "preserve" | "regenerate")}><option value="preserve">{t("requests:repeat.preserve")}</option><option value="regenerate">{t("requests:repeat.regenerate")}</option></select></label>}
        </div>
        {mode === "repeat" && <label className="repeat-checkbox"><input type="checkbox" checked={forever} onChange={(event) => setForever(event.target.checked)} /><span>{t("requests:repeat.forever")}</span></label>}
        {mode === "repeat" && <div className="repeat-warning"><ShieldAlert size={17} /><span>{t("requests:repeat.warning")}</span></div>}
        <footer><button type="button" className="button" onClick={onClose}>{t("common:actions.cancelDialog")}</button><button className="button primary" disabled={busy}>{busy ? t("common:loading") : t("requests:repeat.apply")}</button></footer>
      </form>
    </div>
  </div>;
}
