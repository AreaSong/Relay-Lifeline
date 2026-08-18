import { Clock3, RotateCcw, Settings2, ShieldAlert } from "lucide-react";
import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { errorMessage, type ApiClient } from "../api";
import type { ConfirmDialogState } from "./ConfirmDialog";
import type { RequestInfo, RetryPolicyInfo, RetryPolicyInput, RetryScheduleMode } from "../types";

const SECOND = 1_000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

type Preset = "standard" | "fast" | "conservative" | "custom";
type Choice = string;

interface RetryPolicyDialogProps {
  targets: RequestInfo[];
  api: ApiClient;
  refresh: () => Promise<void>;
  onClose: () => void;
  onError: (message: string) => void;
  onSuccess: (message: string) => void;
  confirm: (state: ConfirmDialogState) => Promise<boolean>;
}

interface FormState {
  preset: Preset;
  durationChoice: Choice;
  customDurationValue: string;
  customDurationUnit: Choice;
  mode: RetryScheduleMode;
  fixedChoice: Choice;
  customFixedValue: string;
  customFixedUnit: Choice;
  randomChoice: Choice;
  customRandomMin: string;
  customRandomMax: string;
  customRandomUnit: Choice;
  exponentialChoice: Choice;
  customExponentialBase: string;
  customExponentialMax: string;
  customExponentialUnit: Choice;
  maxAttemptsChoice: Choice;
  customMaxAttempts: string;
  honorRetryAfter: boolean;
  overwrite: boolean;
  retryWaitingNow: boolean;
}

const fixedChoices = [
  ["5000", "5s"], ["15000", "15s"], ["30000", "30s"], ["60000", "1m"], ["300000", "5m"], ["900000", "15m"],
] as const;
const randomChoices = [
  ["5000:15000", "5s–15s"], ["15000:60000", "15s–60s"], ["60000:120000", "1m–2m"], ["60000:300000", "1m–5m"],
] as const;
const exponentialChoices = [
  ["5000:60000", "5s → 1m"], ["15000:300000", "15s → 5m"], ["60000:900000", "1m → 15m"],
] as const;

function durationValue(value: string, unit: string) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return 0;
  return Math.round(parsed * Number(unit));
}

function fromMilliseconds(milliseconds: number, fallbackUnit = String(MINUTE)) {
  const units = [String(HOUR), String(MINUTE), String(SECOND)];
  const unit = units.find((candidate) => milliseconds % Number(candidate) === 0) || fallbackUnit;
  return { value: String(milliseconds / Number(unit)), unit };
}

function choiceFor(value: number, choices: readonly (readonly [string, string])[]) {
  return choices.some(([candidate]) => Number(candidate) === value) ? String(value) : "custom";
}

function splitChoice(value: string) {
  const [minimum, maximum] = value.split(":");
  return { minimum, maximum };
}

function rangeFromMilliseconds(minimum: number, maximum: number) {
  const units = [HOUR, MINUTE, SECOND];
  const unit = units.find((candidate) => minimum % candidate === 0 && maximum % candidate === 0) || SECOND;
  return { minimum: String(minimum / unit), maximum: String(maximum / unit), unit: String(unit) };
}

function policyDefaults(policy?: RetryPolicyInfo): FormState {
  const schedule = policy?.schedule;
  const duration = policy?.durationMilliseconds || HOUR;
  const durationCustom = fromMilliseconds(duration);
  const mode = schedule?.mode || "random";
  const fixed = schedule?.intervalMilliseconds || 15000;
  const randomMin = schedule?.minimumIntervalMilliseconds || MINUTE;
  const randomMax = schedule?.maximumIntervalMilliseconds || 2 * MINUTE;
  const exponentialBase = schedule?.baseIntervalMilliseconds || MINUTE;
  const exponentialMax = schedule?.maximumIntervalMilliseconds || 15 * MINUTE;
  const randomValue = `${randomMin}:${randomMax}`;
  const exponentialValue = `${exponentialBase}:${exponentialMax}`;
  const randomCustom = rangeFromMilliseconds(randomMin, randomMax);
  const exponentialCustom = rangeFromMilliseconds(exponentialBase, exponentialMax);
  const attempts = policy?.maxAdditionalAttempts || 0;
  return {
    preset: policy ? "custom" : "standard",
    durationChoice: [300000, 900000, 1800000, HOUR, 6 * HOUR, DAY].includes(duration) ? String(duration) : "custom",
    customDurationValue: durationCustom.value,
    customDurationUnit: durationCustom.unit,
    mode,
    fixedChoice: choiceFor(fixed, fixedChoices),
    customFixedValue: fromMilliseconds(fixed, String(SECOND)).value,
    customFixedUnit: fromMilliseconds(fixed, String(SECOND)).unit,
    randomChoice: randomChoices.some(([candidate]) => candidate === randomValue) ? randomValue : "custom",
    customRandomMin: randomCustom.minimum,
    customRandomMax: randomCustom.maximum,
    customRandomUnit: randomCustom.unit,
    exponentialChoice: exponentialChoices.some(([candidate]) => candidate === exponentialValue) ? exponentialValue : "custom",
    customExponentialBase: exponentialCustom.minimum,
    customExponentialMax: exponentialCustom.maximum,
    customExponentialUnit: exponentialCustom.unit,
    maxAttemptsChoice: [3, 5, 10, 20].includes(attempts) ? String(attempts) : attempts > 0 ? "custom" : "0",
    customMaxAttempts: String(attempts || 3),
    honorRetryAfter: policy?.honorRetryAfter ?? true,
    overwrite: true,
    retryWaitingNow: false,
  };
}

function actionFor(request: RequestInfo, action: "retry" | "policy") {
  if (request.actions) return action === "retry" ? request.actions.canRetryNow : request.actions.canSetRetryPolicy;
  if (action === "retry") return request.state === "waiting" || request.state === "uncertain";
  return ["queued", "requesting", "waiting", "uncertain"].includes(request.state);
}

function hasRetryPolicy(request: RequestInfo) {
  if (request.retryPolicy || (request.retryIntervalMilliseconds || 0) > 0) return true;
  if (!request.retryDeadline) return false;
  const deadline = new Date(request.retryDeadline);
  return !Number.isNaN(deadline.getTime()) && deadline.getUTCFullYear() > 1;
}

function buildPolicy(form: FormState): { policy?: RetryPolicyInput; error?: string } {
  const duration = form.durationChoice === "custom" ? durationValue(form.customDurationValue, form.customDurationUnit) : Number(form.durationChoice);
  if (duration < 5 * SECOND || duration > DAY) return { error: "duration" };
  let schedule: RetryPolicyInput["schedule"];
  if (form.mode === "inherit" || form.mode === "immediate") {
    schedule = { mode: form.mode };
  } else if (form.mode === "fixed") {
    const interval = form.fixedChoice === "custom" ? durationValue(form.customFixedValue, form.customFixedUnit) : Number(form.fixedChoice);
    if (interval < 5 * SECOND || interval >= duration || interval > DAY) return { error: "interval" };
    schedule = { mode: form.mode, intervalMilliseconds: interval };
  } else if (form.mode === "random") {
    const random = form.randomChoice === "custom" ? {
      minimum: durationValue(form.customRandomMin, form.customRandomUnit),
      maximum: durationValue(form.customRandomMax, form.customRandomUnit),
    } : splitChoice(form.randomChoice);
    const minimum = Number(random.minimum);
    const maximum = Number(random.maximum);
    if (minimum < 5 * SECOND || maximum < minimum || maximum >= duration || maximum > DAY) return { error: "interval" };
    schedule = { mode: form.mode, minimumIntervalMilliseconds: minimum, maximumIntervalMilliseconds: maximum };
  } else {
    const exponential = form.exponentialChoice === "custom" ? {
      base: durationValue(form.customExponentialBase, form.customExponentialUnit),
      maximum: durationValue(form.customExponentialMax, form.customExponentialUnit),
    } : (() => { const pair = splitChoice(form.exponentialChoice); return { base: pair.minimum, maximum: pair.maximum }; })();
    const base = Number(exponential.base);
    const maximum = Number(exponential.maximum);
    if (base < 5 * SECOND || maximum < base || maximum >= duration || maximum > DAY) return { error: "interval" };
    schedule = { mode: form.mode, baseIntervalMilliseconds: base, maximumIntervalMilliseconds: maximum };
  }
  const maxAttempts = form.maxAttemptsChoice === "custom" ? Number(form.customMaxAttempts) : Number(form.maxAttemptsChoice);
  if (!Number.isInteger(maxAttempts) || maxAttempts < 0 || maxAttempts > 1000) return { error: "attempts" };
  if (form.mode === "immediate" && (maxAttempts < 1 || maxAttempts > 20)) return { error: "attempts" };
  return { policy: { durationMilliseconds: duration, schedule, maxAdditionalAttempts: maxAttempts || undefined, honorRetryAfter: form.honorRetryAfter } };
}

function estimateAttempts(policy: RetryPolicyInput) {
  if (policy.maxAdditionalAttempts) return policy.maxAdditionalAttempts;
  if (policy.schedule.mode === "fixed" && policy.schedule.intervalMilliseconds) return Math.floor(policy.durationMilliseconds / policy.schedule.intervalMilliseconds);
  if (policy.schedule.mode === "random" && policy.schedule.minimumIntervalMilliseconds) return Math.floor(policy.durationMilliseconds / policy.schedule.minimumIntervalMilliseconds);
  if (policy.schedule.mode === "exponential" && policy.schedule.baseIntervalMilliseconds) return Math.floor(policy.durationMilliseconds / policy.schedule.baseIntervalMilliseconds);
  return 0;
}

export function RetryPolicyDialog({ targets, api, refresh, onClose, onError, onSuccess, confirm }: RetryPolicyDialogProps) {
  const { t } = useTranslation(["common", "requests"]);
  const dialog = useRef<HTMLDivElement>(null);
  const [form, setForm] = useState<FormState>(() => policyDefaults(targets[0]?.retryPolicy));
  const [busy, setBusy] = useState(false);
  const eligibleTargets = useMemo(() => targets.filter((request) => actionFor(request, "policy")), [targets]);
  const waitingTargets = useMemo(() => eligibleTargets.filter((request) => request.state === "waiting"), [eligibleTargets]);

  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.current?.querySelector<HTMLElement>("button, input, select")?.focus();
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

  function update(changes: Partial<FormState>) {
    setForm((current) => ({ ...current, ...changes, preset: "custom" }));
  }

  function applyPreset(preset: Exclude<Preset, "custom">) {
    if (preset === "standard") setForm((current) => ({ ...current, preset, durationChoice: String(HOUR), mode: "random", randomChoice: "60000:120000", maxAttemptsChoice: "0", honorRetryAfter: true }));
    if (preset === "fast") setForm((current) => ({ ...current, preset, durationChoice: String(15 * MINUTE), mode: "fixed", fixedChoice: "15000", maxAttemptsChoice: "20", honorRetryAfter: true }));
    if (preset === "conservative") setForm((current) => ({ ...current, preset, durationChoice: String(6 * HOUR), mode: "exponential", exponentialChoice: "60000:900000", maxAttemptsChoice: "0", honorRetryAfter: true }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const built = buildPolicy(form);
    if (!built.policy) {
      onError(t(`requests:retryPolicy.validation.${built.error || "duration"}`));
      return;
    }
    if (!eligibleTargets.length) {
      onError(t("requests:retryPolicy.noEligible"));
      return;
    }
    const estimate = estimateAttempts(built.policy) * eligibleTargets.length;
    if ((built.policy.schedule.mode === "immediate" || estimate > 100) && !await confirm({
      title: t("requests:retryPolicy.confirmTitle"),
      description: t("requests:retryPolicy.confirmDescription", { count: eligibleTargets.length, estimate: estimate || t("requests:retryPolicy.durationOnly") }),
      confirmLabel: t("requests:retryPolicy.confirmAction"), tone: built.policy.schedule.mode === "immediate" ? "danger" : "default",
    })) return;
    setBusy(true);
    try {
      const ids = eligibleTargets.map((request) => request.id);
      let accepted = 1;
      if (ids.length === 1) await api.setRetryPolicy(ids[0], built.policy, form.overwrite);
      else accepted = (await api.batchRetryPolicy(ids, { policy: built.policy, overwrite: form.overwrite, retryWaitingNow: form.retryWaitingNow })).accepted;
      await refresh();
      onSuccess(t("requests:retryPolicy.applied", { accepted, total: ids.length }));
      onClose();
    } catch (reason) {
      onError(errorMessage(reason));
    } finally {
      setBusy(false);
    }
  }

  async function reset() {
    if (!eligibleTargets.length) return;
    setBusy(true);
    try {
      const ids = eligibleTargets.map((request) => request.id);
      let accepted = 1;
      if (ids.length === 1) await api.clearRetryPolicy(ids[0]);
      else accepted = (await api.batchRetryPolicy(ids, { reset: true })).accepted;
      await refresh();
      onSuccess(t("requests:retryPolicy.reset", { accepted, total: ids.length }));
      onClose();
    } catch (reason) {
      onError(errorMessage(reason));
    } finally {
      setBusy(false);
    }
  }

  return <div className="confirm-backdrop" role="presentation" onMouseDown={onClose}>
    <div ref={dialog} className="repeat-dialog retry-policy-dialog" role="dialog" aria-modal="true" aria-labelledby="retry-policy-title" onMouseDown={(event) => event.stopPropagation()}>
      <header><span className="confirm-icon"><Settings2 size={20} /></span><div><h2 id="retry-policy-title">{t("requests:retryPolicy.title")}</h2><p>{t("requests:retryPolicy.targets", { count: targets.length, eligible: eligibleTargets.length, waiting: waitingTargets.length })}</p></div></header>
      <form onSubmit={submit}>
        <div className="policy-presets" aria-label={t("requests:retryPolicy.presetsLabel")}>
          {(["standard", "fast", "conservative"] as const).map((preset) => <button type="button" key={preset} className={form.preset === preset ? "active" : ""} onClick={() => applyPreset(preset)}>{t(`requests:retryPolicy.presets.${preset}`)}</button>)}
          <button type="button" className={form.preset === "custom" ? "active" : ""} onClick={() => update({})}>{t("requests:retryPolicy.presets.custom")}</button>
        </div>
        <div className="form-grid">
          <label className="field"><span>{t("requests:retryPolicy.duration")}</span><select value={form.durationChoice} onChange={(event) => update({ durationChoice: event.target.value })}>{[[300000, "5m"], [900000, "15m"], [1800000, "30m"], [HOUR, "1h"], [6 * HOUR, "6h"], [DAY, "24h"]].map(([value, label]) => <option key={value} value={value}>{t(`requests:retryPolicy.values.${label}`)}</option>)}<option value="custom">{t("requests:retryPolicy.custom")}</option></select></label>
          {form.durationChoice === "custom" && <div className="compound-field"><input type="number" min="0.01" step="any" value={form.customDurationValue} onChange={(event) => update({ customDurationValue: event.target.value })} aria-label={t("requests:retryPolicy.customDuration")} /><select value={form.customDurationUnit} onChange={(event) => update({ customDurationUnit: event.target.value })}><option value={SECOND}>{t("requests:retryPolicy.units.seconds")}</option><option value={MINUTE}>{t("requests:retryPolicy.units.minutes")}</option><option value={HOUR}>{t("requests:retryPolicy.units.hours")}</option></select></div>}
          <label className="field"><span>{t("requests:retryPolicy.mode")}</span><select value={form.mode} onChange={(event) => { const mode = event.target.value as RetryScheduleMode; update({ mode, ...(mode === "immediate" && form.maxAttemptsChoice === "0" ? { maxAttemptsChoice: "3" } : {}) }); }}>{(["inherit", "immediate", "fixed", "random", "exponential"] as const).map((mode) => <option key={mode} value={mode}>{t(`requests:retryPolicy.modes.${mode}`)}</option>)}</select></label>
          {form.mode === "fixed" && <label className="field"><span>{t("requests:retryPolicy.interval")}</span><select value={form.fixedChoice} onChange={(event) => update({ fixedChoice: event.target.value })}>{fixedChoices.map(([value, label]) => <option key={value} value={value}>{t(`requests:retryPolicy.values.${label}`)}</option>)}<option value="custom">{t("requests:retryPolicy.custom")}</option></select></label>}
          {form.mode === "fixed" && form.fixedChoice === "custom" && <div className="compound-field"><input type="number" min="0.01" step="any" value={form.customFixedValue} onChange={(event) => update({ customFixedValue: event.target.value })} aria-label={t("requests:retryPolicy.customInterval")} /><select value={form.customFixedUnit} onChange={(event) => update({ customFixedUnit: event.target.value })}><option value={SECOND}>{t("requests:retryPolicy.units.seconds")}</option><option value={MINUTE}>{t("requests:retryPolicy.units.minutes")}</option><option value={HOUR}>{t("requests:retryPolicy.units.hours")}</option></select></div>}
          {form.mode === "random" && <label className="field"><span>{t("requests:retryPolicy.randomRange")}</span><select value={form.randomChoice} onChange={(event) => update({ randomChoice: event.target.value })}>{randomChoices.map(([value, label]) => <option key={value} value={value}>{label}</option>)}<option value="custom">{t("requests:retryPolicy.custom")}</option></select></label>}
          {form.mode === "random" && form.randomChoice === "custom" && <div className="compound-field range-field"><input type="number" min="0.01" step="any" value={form.customRandomMin} onChange={(event) => update({ customRandomMin: event.target.value })} aria-label={t("requests:retryPolicy.randomMinimum")} /><span>–</span><input type="number" min="0.01" step="any" value={form.customRandomMax} onChange={(event) => update({ customRandomMax: event.target.value })} aria-label={t("requests:retryPolicy.randomMaximum")} /><select value={form.customRandomUnit} onChange={(event) => update({ customRandomUnit: event.target.value })}><option value={SECOND}>{t("requests:retryPolicy.units.seconds")}</option><option value={MINUTE}>{t("requests:retryPolicy.units.minutes")}</option><option value={HOUR}>{t("requests:retryPolicy.units.hours")}</option></select></div>}
          {form.mode === "exponential" && <label className="field"><span>{t("requests:retryPolicy.exponentialRange")}</span><select value={form.exponentialChoice} onChange={(event) => update({ exponentialChoice: event.target.value })}>{exponentialChoices.map(([value, label]) => <option key={value} value={value}>{label}</option>)}<option value="custom">{t("requests:retryPolicy.custom")}</option></select></label>}
          {form.mode === "exponential" && form.exponentialChoice === "custom" && <div className="compound-field range-field"><input type="number" min="0.01" step="any" value={form.customExponentialBase} onChange={(event) => update({ customExponentialBase: event.target.value })} aria-label={t("requests:retryPolicy.exponentialBase")} /><span>→</span><input type="number" min="0.01" step="any" value={form.customExponentialMax} onChange={(event) => update({ customExponentialMax: event.target.value })} aria-label={t("requests:retryPolicy.exponentialMaximum")} /><select value={form.customExponentialUnit} onChange={(event) => update({ customExponentialUnit: event.target.value })}><option value={SECOND}>{t("requests:retryPolicy.units.seconds")}</option><option value={MINUTE}>{t("requests:retryPolicy.units.minutes")}</option><option value={HOUR}>{t("requests:retryPolicy.units.hours")}</option></select></div>}
          <label className="field"><span>{t("requests:retryPolicy.maxAttempts")}</span><select value={form.maxAttemptsChoice} onChange={(event) => update({ maxAttemptsChoice: event.target.value })}><option value="0">{t("requests:retryPolicy.durationOnly")}</option>{[3, 5, 10, 20].map((value) => <option key={value} value={value}>{t("requests:retryPolicy.additionalAttempts", { count: value })}</option>)}<option value="custom">{t("requests:retryPolicy.custom")}</option></select></label>
          {form.maxAttemptsChoice === "custom" && <div className="compound-field"><input type="number" min="1" max="1000" step="1" value={form.customMaxAttempts} onChange={(event) => update({ customMaxAttempts: event.target.value })} aria-label={t("requests:retryPolicy.customAttempts")} /><span>{t("requests:retryPolicy.attemptsUnit")}</span></div>}
        </div>
        <label className="repeat-checkbox"><input type="checkbox" checked={form.honorRetryAfter} onChange={(event) => update({ honorRetryAfter: event.target.checked })} /><span>{t("requests:retryPolicy.honorRetryAfter")}</span></label>
        {targets.length > 1 && <>
          <label className="repeat-checkbox"><input type="checkbox" checked={form.overwrite} onChange={(event) => update({ overwrite: event.target.checked })} /><span>{t("requests:retryPolicy.overwrite")}</span></label>
          <label className="repeat-checkbox"><input type="checkbox" checked={form.retryWaitingNow} onChange={(event) => update({ retryWaitingNow: event.target.checked })} /><span>{t("requests:retryPolicy.retryWaitingNow")}</span></label>
        </>}
        {form.mode === "immediate" && <div className="repeat-warning"><ShieldAlert size={17} /><span>{t("requests:retryPolicy.immediateWarning")}</span></div>}
        <p className="retry-policy-note"><Clock3 size={14} />{t("requests:retryPolicy.globalSpacing")}</p>
        <footer><button type="button" className="button" onClick={onClose}>{t("common:actions.cancelDialog")}</button>{eligibleTargets.some(hasRetryPolicy) && <button type="button" className="button" disabled={busy} onClick={reset}><RotateCcw size={15} />{t("requests:retryPolicy.resetAction")}</button>}<button className="button primary" disabled={busy || !eligibleTargets.length}>{busy ? t("common:loading") : t("requests:retryPolicy.apply")}</button></footer>
      </form>
    </div>
  </div>;
}
