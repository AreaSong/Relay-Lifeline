import { Maximize2, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode, RefObject } from "react";
import type { ECharts, EChartsCoreOption } from "echarts/core";

export interface TimeSeriesPoint {
  time: string;
  requests: number;
  successful: number;
  failedAttempts: number;
}

export interface PressurePoint {
  time: string;
  active: number;
  requesting?: number;
  waiting: number;
  queued: number;
}

export interface ErrorSlice {
  category: string;
  count: number;
}

export interface RecoveryPoint {
  bucket: string;
  count: number;
}

export interface OperationsChartLabels {
  reliabilityTitle: string;
  pressureTitle: string;
  errorsTitle: string;
  recoveryTitle: string;
  empty: string;
  unavailable: string;
  requests: string;
  successRate: string;
  failedAttempts: string;
  active: string;
  requesting: string;
  waiting: string;
  queued: string;
  duration: string;
  expand: string;
  collapse: string;
}

export interface OperationsChartTheme {
  foreground: string;
  muted: string;
  grid: string;
  success: string;
  warning: string;
  accent: string;
  surface: string;
}

export interface OperationsChartsProps {
  reliability: TimeSeriesPoint[];
  pressure: PressurePoint[];
  errors: ErrorSlice[];
  recovery: RecoveryPoint[];
  labels: OperationsChartLabels;
  theme: OperationsChartTheme;
  locale?: string;
  className?: string;
  preferredSecondary?: Exclude<ChartKey, "reliability">;
}

export function operationsChartTheme(dark: boolean): OperationsChartTheme {
  return dark ? {
    foreground: "#f2f6f3", muted: "#87938b", grid: "#2b342f", success: "#68e0a0",
    warning: "#eab55a", accent: "#68bde7", surface: "#141917",
  } : {
    foreground: "#151b17", muted: "#6f7a73", grid: "#d9dfda", success: "#187b4c",
    warning: "#a96b12", accent: "#176e9b", surface: "#ffffff",
  };
}

type ChartKey = "reliability" | "pressure" | "errors" | "recovery";
type ChartOptions = Record<ChartKey, EChartsCoreOption | null>;
type ChartInstances = Partial<Record<ChartKey, ECharts>>;
type LoadState = "loading" | "ready" | "failed";

const chartKeys: ChartKey[] = ["reliability", "pressure", "errors", "recovery"];
const secondaryKeys: Exclude<ChartKey, "reliability">[] = ["pressure", "errors", "recovery"];

function safeValue(value: number) {
  return Number.isFinite(value) ? Math.max(0, value) : 0;
}

function formatTimeLabel(value: string, locale?: string) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(date);
}

function tooltipStyle(theme: OperationsChartTheme) {
  return {
    backgroundColor: theme.surface,
    borderColor: theme.grid,
    borderWidth: 1,
    padding: [9, 11],
    confine: true,
    textStyle: { color: theme.foreground, fontSize: 11, fontFamily: '"Source Sans 3", "Source Han Sans SC", sans-serif' },
    extraCssText: "box-shadow: 0 16px 48px rgba(0,0,0,.22); border-radius: 6px;",
  };
}

function categoryAxis(data: string[], theme: OperationsChartTheme, name?: string) {
  return {
    type: "category" as const,
    data,
    name,
    nameTextStyle: { color: theme.muted },
    axisLine: { lineStyle: { color: theme.grid } },
    axisTick: { show: false },
    axisLabel: { color: theme.muted, hideOverlap: true, fontSize: 11 },
  };
}

function valueAxis(theme: OperationsChartTheme, name?: string) {
  return {
    type: "value" as const,
    name,
    minInterval: 1,
    nameTextStyle: { color: theme.muted },
    axisLine: { show: false },
    axisTick: { show: false },
    axisLabel: { color: theme.muted, fontSize: 11 },
    splitLine: { lineStyle: { color: theme.grid, type: "dashed", opacity: 0.72 } },
  };
}

function baseCartesian(theme: OperationsChartTheme, animated: boolean) {
  return {
    animation: animated,
    animationDuration: 360,
    backgroundColor: theme.surface,
    textStyle: { color: theme.foreground, fontFamily: '"Source Sans 3", "Source Han Sans SC", sans-serif', fontSize: 11 },
    tooltip: { trigger: "axis", axisPointer: { type: "cross", lineStyle: { color: theme.muted, opacity: 0.5 }, crossStyle: { color: theme.muted, opacity: 0.5 } }, ...tooltipStyle(theme) },
    legend: { top: 0, left: 0, textStyle: { color: theme.muted, fontSize: 11 }, itemWidth: 12, itemHeight: 7, itemGap: 16 },
    grid: { top: 44, right: 16, bottom: 22, left: 34, containLabel: true },
  };
}

function reliabilityOption(
  data: TimeSeriesPoint[],
  labels: OperationsChartLabels,
  theme: OperationsChartTheme,
  locale: string | undefined,
  animated: boolean,
): EChartsCoreOption {
  const successRates = data.map((point) => {
    const requests = safeValue(point.requests);
    return requests > 0 ? Math.min(100, safeValue(point.successful) / requests * 100) : 0;
  });
  return {
    ...baseCartesian(theme, animated),
    xAxis: categoryAxis(data.map((point) => formatTimeLabel(point.time, locale)), theme),
    yAxis: [
      valueAxis(theme),
      { ...valueAxis(theme), min: 0, max: 100, minInterval: undefined, axisLabel: { color: theme.muted, formatter: "{value}%" } },
    ],
    series: [
      { name: labels.requests, type: "bar", data: data.map((point) => safeValue(point.requests)), itemStyle: { color: theme.accent, opacity: 0.28 }, barMaxWidth: 14 },
      { name: labels.failedAttempts, type: "line", data: data.map((point) => safeValue(point.failedAttempts)), symbol: "none", lineStyle: { color: theme.warning, width: 2 } },
      { name: labels.successRate, type: "line", yAxisIndex: 1, data: successRates, symbol: "none", smooth: 0.24, lineStyle: { color: theme.success, width: 2.5 } },
    ],
  };
}

function pressureOption(
  data: PressurePoint[],
  labels: OperationsChartLabels,
  theme: OperationsChartTheme,
  locale: string | undefined,
  animated: boolean,
): EChartsCoreOption {
  const line = (name: string, color: string, values: number[]) => ({
    name,
    type: "line",
    stack: "load",
    data: values,
    symbol: "none",
    smooth: 0.22,
    lineStyle: { color, width: 1.6 },
    areaStyle: { color, opacity: 0.16 },
  });
  return {
    ...baseCartesian(theme, animated),
    xAxis: categoryAxis(data.map((point) => formatTimeLabel(point.time, locale)), theme),
    yAxis: valueAxis(theme, labels.requests),
    series: [
      line(labels.requesting, theme.accent, data.map((point) => safeValue(point.requesting ?? point.active))),
      line(labels.waiting, theme.warning, data.map((point) => safeValue(point.waiting))),
      line(labels.queued, theme.muted, data.map((point) => safeValue(point.queued))),
    ],
  };
}

function errorsOption(data: ErrorSlice[], labels: OperationsChartLabels, theme: OperationsChartTheme, animated: boolean): EChartsCoreOption {
  const values = data.filter((slice) => safeValue(slice.count) > 0).sort((left, right) => right.count - left.count);
  const colors = [theme.warning, theme.accent, theme.muted, theme.success];
  return {
    ...baseCartesian(theme, animated),
    legend: { show: false },
    grid: { top: 12, right: 22, bottom: 24, left: 18, containLabel: true },
    xAxis: valueAxis(theme),
    yAxis: { ...categoryAxis(values.map((slice) => slice.category), theme), inverse: true },
    series: [{
      name: labels.failedAttempts,
      type: "bar",
      barMaxWidth: 18,
      label: { show: true, position: "right", color: theme.muted },
      data: values.map((slice, index) => ({ value: safeValue(slice.count), itemStyle: { color: colors[index % colors.length] } })),
    }],
  };
}

function recoveryOption(data: RecoveryPoint[], labels: OperationsChartLabels, theme: OperationsChartTheme, animated: boolean): EChartsCoreOption {
  return {
    ...baseCartesian(theme, animated),
    legend: { show: false },
    grid: { top: 14, right: 24, bottom: 22, left: 18, containLabel: true },
    xAxis: valueAxis(theme, labels.requests),
    yAxis: { ...categoryAxis(data.map((point) => point.bucket), theme, labels.duration), inverse: true },
    series: [{
      name: labels.requests,
      type: "bar",
      data: data.map((point) => safeValue(point.count)),
      itemStyle: { color: theme.accent },
      label: { show: true, position: "right", color: theme.muted },
      barMaxWidth: 18,
    }],
  };
}

function useReducedMotion() {
  const [reduced, setReduced] = useState(() =>
    typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches,
  );
  useEffect(() => {
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = (event: MediaQueryListEvent) => setReduced(event.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  return reduced;
}

function applyOptions(instances: ChartInstances, options: ChartOptions) {
  chartKeys.forEach((key) => {
    const chart = instances[key];
    if (!chart || chart.isDisposed()) return;
    const option = options[key];
    if (option) chart.setOption(option, { notMerge: true, lazyUpdate: true });
    else chart.clear();
  });
}

interface ChartCardProps {
  chartKey: ChartKey;
  title: string;
  empty: boolean;
  loadState: LoadState;
  emptyLabel: string;
  unavailableLabel: string;
  canvasRef: RefObject<HTMLDivElement | null>;
  active?: boolean;
  expanded?: boolean;
  expandLabel?: string;
  collapseLabel?: string;
  onToggleExpand?: () => void;
  showHeader?: boolean;
  modalRef?: RefObject<HTMLElement | null>;
  accessibleContent: ReactNode;
}

function ChartCard({ chartKey, title, empty, loadState, emptyLabel, unavailableLabel, canvasRef, active = true, expanded = false, expandLabel, collapseLabel, onToggleExpand, showHeader = true, modalRef, accessibleContent }: ChartCardProps) {
  const unavailable = loadState === "failed";
  const classes = [
    "operations-chart",
    `operations-chart--${chartKey}`,
    empty ? "operations-chart--empty" : "",
    unavailable ? "operations-chart--unavailable" : "",
    active ? "is-active" : "is-inactive",
    expanded ? "is-expanded" : "",
    showHeader ? "" : "operations-chart--headerless",
  ].filter(Boolean).join(" ");
  return <article ref={modalRef} className={classes} aria-busy={loadState === "loading"} aria-hidden={!active} role={expanded ? "dialog" : undefined} aria-modal={expanded || undefined} aria-label={expanded ? title : undefined} tabIndex={expanded ? -1 : undefined}>
    {showHeader && <header className="operations-chart__header"><h3>{title}</h3>{onToggleExpand && <button className="chart-expand" aria-label={expanded ? collapseLabel : expandLabel} onClick={onToggleExpand}>{expanded ? <X size={16} /> : <Maximize2 size={15} />}</button>}</header>}
    <div className="operations-chart__body">
      <div
        className="operations-chart__canvas"
        ref={canvasRef}
        aria-hidden="true"
      />
	  {!empty && loadState === "ready" && <div className="sr-only">{accessibleContent}</div>}
      {unavailable
        ? <p className="operations-chart__message operations-chart__message--unavailable">{unavailableLabel}</p>
        : empty && <p className="operations-chart__message operations-chart__message--empty">{emptyLabel}</p>}
    </div>
  </article>;
}

export function OperationsCharts({
  reliability,
  pressure,
  errors,
  recovery,
  labels,
  theme,
  locale,
  className = "",
  preferredSecondary = "pressure",
}: OperationsChartsProps) {
  const reliabilityRef = useRef<HTMLDivElement>(null);
  const pressureRef = useRef<HTMLDivElement>(null);
  const errorsRef = useRef<HTMLDivElement>(null);
  const recoveryRef = useRef<HTMLDivElement>(null);
  const instancesRef = useRef<ChartInstances>({});
  const optionsRef = useRef<ChartOptions>({ reliability: null, pressure: null, errors: null, recovery: null });
  const secondaryManuallySelected = useRef(false);
  const reliabilityModalRef = useRef<HTMLElement>(null);
  const secondaryModalRef = useRef<HTMLElement>(null);
  const [loadState, setLoadState] = useState<LoadState>("loading");
  const [secondary, setSecondary] = useState<Exclude<ChartKey, "reliability">>(preferredSecondary);
  const [expanded, setExpanded] = useState<"reliability" | "secondary" | null>(null);
  const reducedMotion = useReducedMotion();
  const empty = {
    reliability: reliability.length === 0 || reliability.every((point) => safeValue(point.requests) === 0 && safeValue(point.successful) === 0 && safeValue(point.failedAttempts) === 0),
    pressure: pressure.length === 0 || pressure.every((point) => safeValue(point.requesting ?? point.active) === 0 && safeValue(point.waiting) === 0 && safeValue(point.queued) === 0),
    errors: errors.length === 0 || errors.every((slice) => safeValue(slice.count) === 0),
    recovery: recovery.length === 0 || recovery.every((point) => safeValue(point.count) === 0),
  };
  const options = useMemo<ChartOptions>(() => ({
    reliability: empty.reliability ? null : reliabilityOption(reliability, labels, theme, locale, !reducedMotion),
    pressure: empty.pressure ? null : pressureOption(pressure, labels, theme, locale, !reducedMotion),
    errors: empty.errors ? null : errorsOption(errors, labels, theme, !reducedMotion),
    recovery: empty.recovery ? null : recoveryOption(recovery, labels, theme, !reducedMotion),
  }), [empty.errors, empty.pressure, empty.recovery, empty.reliability, errors, labels, locale, pressure, recovery, reducedMotion, reliability, theme]);
  optionsRef.current = options;

  useEffect(() => {
    let cancelled = false;
    let resizeFrame = 0;
    let observer: ResizeObserver | undefined;
    const created: ECharts[] = [];
    const nodes: Record<ChartKey, HTMLDivElement | null> = {
      reliability: reliabilityRef.current,
      pressure: pressureRef.current,
      errors: errorsRef.current,
      recovery: recoveryRef.current,
    };
    const resize = () => {
      window.cancelAnimationFrame(resizeFrame);
      resizeFrame = window.requestAnimationFrame(() => {
        Object.values(instancesRef.current).forEach((chart) => { if (chart && !chart.isDisposed()) chart.resize(); });
      });
    };
    void import("./chartRuntime").then((core) => {
      if (cancelled) return;
      const instances: ChartInstances = {};
      chartKeys.forEach((key) => {
        const node = nodes[key];
        if (!node) return;
        const chart = core.init(node, undefined, { renderer: "canvas", devicePixelRatio: Math.min(window.devicePixelRatio || 1, window.innerWidth <= 820 ? 1.25 : 2) });
        instances[key] = chart;
        created.push(chart);
      });
      instancesRef.current = instances;
      applyOptions(instances, optionsRef.current);
      observer = new ResizeObserver(resize);
      Object.values(nodes).forEach((node) => { if (node) observer?.observe(node); });
      window.addEventListener("resize", resize);
      setLoadState("ready");
    }).catch(() => {
      created.forEach((chart) => chart.dispose());
      if (!cancelled) setLoadState("failed");
    });
    return () => {
      cancelled = true;
      window.cancelAnimationFrame(resizeFrame);
      observer?.disconnect();
      window.removeEventListener("resize", resize);
      created.forEach((chart) => { if (!chart.isDisposed()) chart.dispose(); });
      instancesRef.current = {};
    };
  }, []);

  useEffect(() => { applyOptions(instancesRef.current, options); }, [options]);

  useEffect(() => {
    if (!secondaryManuallySelected.current) setSecondary(preferredSecondary);
  }, [preferredSecondary]);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      Object.values(instancesRef.current).forEach((chart) => { if (chart && !chart.isDisposed()) chart.resize(); });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [expanded, secondary]);

  useEffect(() => {
    if (!expanded) return;
    const modal = expanded === "reliability" ? reliabilityModalRef.current : secondaryModalRef.current;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    modal?.querySelector<HTMLElement>("button")?.focus();
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") { setExpanded(null); return; }
      if (event.key !== "Tab" || !modal) return;
      const focusable = Array.from(modal.querySelectorAll<HTMLElement>('button, [href], [tabindex]:not([tabindex="-1"])')).filter((element) => !element.hasAttribute("disabled"));
      if (!focusable.length) { event.preventDefault(); modal.focus(); return; }
      const first = focusable[0]; const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", handleKey);
    return () => { window.removeEventListener("keydown", handleKey); previous?.focus(); };
  }, [expanded]);

  const titles = { pressure: labels.pressureTitle, errors: labels.errorsTitle, recovery: labels.recoveryTitle };
  const refs = { pressure: pressureRef, errors: errorsRef, recovery: recoveryRef };
  const percent = new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 1 });
  const accessible: Record<ChartKey, ReactNode> = {
    reliability: <table><caption>{labels.reliabilityTitle}</caption><thead><tr><th>{labels.duration}</th><th>{labels.requests}</th><th>{labels.successRate}</th><th>{labels.failedAttempts}</th></tr></thead><tbody>{reliability.map((point) => <tr key={point.time}><th>{formatTimeLabel(point.time, locale)}</th><td>{safeValue(point.requests)}</td><td>{percent.format(point.requests > 0 ? safeValue(point.successful) / safeValue(point.requests) : 0)}</td><td>{safeValue(point.failedAttempts)}</td></tr>)}</tbody></table>,
    pressure: <table><caption>{labels.pressureTitle}</caption><thead><tr><th>{labels.duration}</th><th>{labels.requesting}</th><th>{labels.waiting}</th><th>{labels.queued}</th></tr></thead><tbody>{pressure.map((point) => <tr key={point.time}><th>{formatTimeLabel(point.time, locale)}</th><td>{safeValue(point.requesting ?? point.active)}</td><td>{safeValue(point.waiting)}</td><td>{safeValue(point.queued)}</td></tr>)}</tbody></table>,
    errors: <table><caption>{labels.errorsTitle}</caption><thead><tr><th>{labels.errorsTitle}</th><th>{labels.failedAttempts}</th></tr></thead><tbody>{errors.filter((slice) => safeValue(slice.count) > 0).map((slice) => <tr key={slice.category}><th>{slice.category}</th><td>{safeValue(slice.count)}</td></tr>)}</tbody></table>,
    recovery: <table><caption>{labels.recoveryTitle}</caption><thead><tr><th>{labels.duration}</th><th>{labels.requests}</th></tr></thead><tbody>{recovery.map((point) => <tr key={point.bucket}><th>{point.bucket}</th><td>{safeValue(point.count)}</td></tr>)}</tbody></table>,
  };

  return <>
    <div className={["operations-charts", className, expanded ? "has-expanded-chart" : ""].filter(Boolean).join(" ")}>
      <ChartCard chartKey="reliability" title={labels.reliabilityTitle} empty={empty.reliability} loadState={loadState} emptyLabel={labels.empty} unavailableLabel={labels.unavailable} canvasRef={reliabilityRef} expanded={expanded === "reliability"} expandLabel={labels.expand} collapseLabel={labels.collapse} onToggleExpand={() => setExpanded((value) => value === "reliability" ? null : "reliability")} modalRef={reliabilityModalRef} accessibleContent={accessible.reliability} />
      <article ref={secondaryModalRef} className={`operations-chart-deck${expanded === "secondary" ? " is-expanded" : ""}`} role={expanded === "secondary" ? "dialog" : undefined} aria-modal={expanded === "secondary" || undefined} aria-label={expanded === "secondary" ? titles[secondary] : undefined} tabIndex={expanded === "secondary" ? -1 : undefined}>
        <header className="operations-chart-deck__header"><div className="chart-tabs" role="tablist" aria-label={labels.pressureTitle}>
          {secondaryKeys.map((key) => <button key={key} role="tab" aria-selected={secondary === key} className={secondary === key ? "active" : ""} onClick={() => { secondaryManuallySelected.current = true; setSecondary(key); }}>{titles[key]}</button>)}
        </div><button className="chart-expand" aria-label={expanded === "secondary" ? labels.collapse : labels.expand} onClick={() => setExpanded((value) => value === "secondary" ? null : "secondary")}>{expanded === "secondary" ? <X size={16} /> : <Maximize2 size={15} />}</button></header>
        <div className="operations-chart-deck__body">
          {secondaryKeys.map((key) => <ChartCard key={key} chartKey={key} title={titles[key]} empty={empty[key]} loadState={loadState} emptyLabel={labels.empty} unavailableLabel={labels.unavailable} canvasRef={refs[key]} active={secondary === key} showHeader={false} accessibleContent={accessible[key]} />)}
        </div>
      </article>
    </div>
    {expanded && <button className="chart-focus-backdrop" aria-label={labels.collapse} onClick={() => setExpanded(null)} />}
  </>;
}
