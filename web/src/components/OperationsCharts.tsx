import { useEffect, useMemo, useRef, useState } from "react";
import type { RefObject } from "react";
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
  waiting: string;
  queued: string;
  duration: string;
}

export interface OperationsChartTheme {
  foreground: string;
  muted: string;
  grid: string;
  success: string;
  warning: string;
  danger: string;
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
}

export function operationsChartTheme(dark: boolean): OperationsChartTheme {
  return dark ? {
    foreground: "#edf2ee", muted: "#8b968e", grid: "#303731", success: "#69dfa0",
    warning: "#efb64d", danger: "#ef7064", accent: "#66b8d0", surface: "transparent",
  } : {
    foreground: "#1b201d", muted: "#727b75", grid: "#d1d7d1", success: "#25845a",
    warning: "#b97816", danger: "#c34f46", accent: "#236d4b", surface: "transparent",
  };
}

type ChartKey = "reliability" | "pressure" | "errors" | "recovery";
type ChartOptions = Record<ChartKey, EChartsCoreOption | null>;
type ChartInstances = Partial<Record<ChartKey, ECharts>>;
type LoadState = "loading" | "ready" | "failed";

const chartKeys: ChartKey[] = ["reliability", "pressure", "errors", "recovery"];

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
    textStyle: { color: theme.foreground },
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
    axisLabel: { color: theme.muted, hideOverlap: true },
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
    axisLabel: { color: theme.muted },
    splitLine: { lineStyle: { color: theme.grid } },
  };
}

function baseCartesian(theme: OperationsChartTheme, animated: boolean) {
  return {
    animation: animated,
    animationDuration: 360,
    backgroundColor: theme.surface,
    textStyle: { color: theme.foreground },
    tooltip: { trigger: "axis", ...tooltipStyle(theme) },
    legend: { top: 0, left: 0, textStyle: { color: theme.muted }, itemWidth: 12, itemHeight: 8 },
    grid: { top: 48, right: 18, bottom: 30, left: 42, containLabel: true },
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
      { name: labels.requests, type: "bar", data: data.map((point) => safeValue(point.requests)), itemStyle: { color: theme.accent, opacity: 0.32 }, barMaxWidth: 18 },
      { name: labels.failedAttempts, type: "line", data: data.map((point) => safeValue(point.failedAttempts)), symbol: "none", lineStyle: { color: theme.danger, width: 2 } },
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
    data: values,
    symbol: "none",
    smooth: 0.22,
    lineStyle: { color, width: 2 },
    areaStyle: { color, opacity: 0.09 },
  });
  return {
    ...baseCartesian(theme, animated),
    xAxis: categoryAxis(data.map((point) => formatTimeLabel(point.time, locale)), theme),
    yAxis: valueAxis(theme, labels.requests),
    series: [
      line(labels.active, theme.accent, data.map((point) => safeValue(point.active))),
      line(labels.waiting, theme.warning, data.map((point) => safeValue(point.waiting))),
      line(labels.queued, theme.muted, data.map((point) => safeValue(point.queued))),
    ],
  };
}

function errorsOption(data: ErrorSlice[], labels: OperationsChartLabels, theme: OperationsChartTheme, animated: boolean): EChartsCoreOption {
  const values = data.filter((slice) => safeValue(slice.count) > 0);
  const colors = [theme.danger, theme.warning, theme.accent, theme.muted, theme.success];
  return {
    ...baseCartesian(theme, animated),
    legend: { show: false },
    grid: { top: 12, right: 22, bottom: 24, left: 18, containLabel: true },
    xAxis: valueAxis(theme, labels.failedAttempts),
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
    grid: { top: 18, right: 18, bottom: 34, left: 42, containLabel: true },
    xAxis: categoryAxis(data.map((point) => point.bucket), theme, labels.duration),
    yAxis: valueAxis(theme, labels.requests),
    series: [{
      name: labels.requests,
      type: "bar",
      data: data.map((point) => safeValue(point.count)),
      itemStyle: { color: theme.accent },
      barMaxWidth: 30,
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
}

function ChartCard({ chartKey, title, empty, loadState, emptyLabel, unavailableLabel, canvasRef }: ChartCardProps) {
  const unavailable = loadState === "failed";
  const classes = [
    "operations-chart",
    `operations-chart--${chartKey}`,
    empty ? "operations-chart--empty" : "",
    unavailable ? "operations-chart--unavailable" : "",
  ].filter(Boolean).join(" ");
  return <article className={classes} aria-busy={loadState === "loading"}>
    <header className="operations-chart__header"><h3>{title}</h3></header>
    <div className="operations-chart__body">
      <div
        className="operations-chart__canvas"
        ref={canvasRef}
        role="img"
        aria-label={title}
        aria-hidden={empty || loadState !== "ready"}
      />
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
}: OperationsChartsProps) {
  const reliabilityRef = useRef<HTMLDivElement>(null);
  const pressureRef = useRef<HTMLDivElement>(null);
  const errorsRef = useRef<HTMLDivElement>(null);
  const recoveryRef = useRef<HTMLDivElement>(null);
  const instancesRef = useRef<ChartInstances>({});
  const optionsRef = useRef<ChartOptions>({ reliability: null, pressure: null, errors: null, recovery: null });
  const [loadState, setLoadState] = useState<LoadState>("loading");
  const reducedMotion = useReducedMotion();
  const empty = {
    reliability: reliability.length === 0 || reliability.every((point) => safeValue(point.requests) === 0 && safeValue(point.successful) === 0 && safeValue(point.failedAttempts) === 0),
    pressure: pressure.length === 0 || pressure.every((point) => safeValue(point.active) === 0 && safeValue(point.waiting) === 0 && safeValue(point.queued) === 0),
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

  return <div className={["operations-charts", className].filter(Boolean).join(" ")}>
    <ChartCard chartKey="reliability" title={labels.reliabilityTitle} empty={empty.reliability} loadState={loadState} emptyLabel={labels.empty} unavailableLabel={labels.unavailable} canvasRef={reliabilityRef} />
    <ChartCard chartKey="pressure" title={labels.pressureTitle} empty={empty.pressure} loadState={loadState} emptyLabel={labels.empty} unavailableLabel={labels.unavailable} canvasRef={pressureRef} />
    <ChartCard chartKey="errors" title={labels.errorsTitle} empty={empty.errors} loadState={loadState} emptyLabel={labels.empty} unavailableLabel={labels.unavailable} canvasRef={errorsRef} />
    <ChartCard chartKey="recovery" title={labels.recoveryTitle} empty={empty.recovery} loadState={loadState} emptyLabel={labels.empty} unavailableLabel={labels.unavailable} canvasRef={recoveryRef} />
  </div>;
}
