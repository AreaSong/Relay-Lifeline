import { useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties } from "react";
import type { RequestInfo } from "../types";
import { mountThreeScene, safeCount } from "./signalTopologyScene";
import type { SignalSceneControl, TopologySnapshot } from "./signalTopologyScene";

export type SignalUpstreamState = "unknown" | "healthy" | "degraded";

export interface SignalTopologyLabels {
  ariaLabel: string;
  codex: string;
  relay: string;
  cpa: string;
  healthy: string;
  degraded: string;
  unknown: string;
  active: string;
  waiting: string;
  nextRetry: string;
  retryNow: string;
  staticFallback: string;
  stateLabels?: Record<string, string>;
}

export interface SignalTopologyTheme {
  foreground: string;
  muted: string;
  relay: string;
  healthy: string;
  degraded: string;
  unknown: string;
  signal: string;
}

export interface SignalTopologyProps {
  upstreamState: SignalUpstreamState;
  active: number;
  waiting: number;
  nextRetryAt?: string;
  labels: SignalTopologyLabels;
  locale?: string;
  theme?: Partial<SignalTopologyTheme>;
  className?: string;
  requests?: RequestInfo[];
  selectedRequestId?: string;
  onSelect?: (id: string) => void;
}

type RenderState = "loading" | "ready" | "fallback" | "compact";

function formatRetry(nextRetryAt: string, locale: string | undefined, retryNow: string) {
  const date = new Date(nextRetryAt);
  if (!Number.isFinite(date.getTime())) return nextRetryAt;
  if (date.getTime() <= Date.now()) return retryNow;
  return new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(date);
}

export function SignalTopology({
  upstreamState,
  active,
  waiting,
  nextRetryAt,
  labels,
  locale,
  theme,
  className = "",
  requests = [],
  selectedRequestId,
  onSelect,
}: SignalTopologyProps) {
  const sceneHostRef = useRef<HTMLDivElement>(null);
  const sceneControlRef = useRef<SignalSceneControl>(null);
  const snapshotRef = useRef<TopologySnapshot>({ upstreamState, active, waiting, nextRetryAt });
  const [renderState, setRenderState] = useState<RenderState>("loading");
  const [compact, setCompact] = useState(() => window.matchMedia("(max-width: 820px)").matches);
  const [themeRevision, setThemeRevision] = useState(0);
  snapshotRef.current = { upstreamState, active, waiting, nextRetryAt };
  const sceneTheme = useMemo(() => theme ? {
    foreground: theme.foreground,
    muted: theme.muted,
    relay: theme.relay,
    healthy: theme.healthy,
    degraded: theme.degraded,
    unknown: theme.unknown,
    signal: theme.signal,
  } : undefined, [theme?.degraded, theme?.foreground, theme?.healthy, theme?.muted, theme?.relay, theme?.signal, theme?.unknown]);
  const retryValue = useMemo(
    () => nextRetryAt ? formatRetry(nextRetryAt, locale, labels.retryNow) : "",
    [labels.retryNow, locale, nextRetryAt],
  );
  const visibleRequests = useMemo(() => requests.slice(0, 7), [requests]);
  const primaryRequestId = visibleRequests.find((request) => request.state === "requesting")?.id;
  const overflow = Math.max(0, requests.length - visibleRequests.length);

  useEffect(() => {
    const media = window.matchMedia("(max-width: 820px)");
    const changed = (event: MediaQueryListEvent) => setCompact(event.matches);
    media.addEventListener("change", changed);
    return () => media.removeEventListener("change", changed);
  }, []);

  useEffect(() => {
    const observer = new MutationObserver(() => setThemeRevision((value) => value + 1));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const host = sceneHostRef.current;
    if (!host) return;
    if (compact) { host.replaceChildren(); setRenderState("compact"); return; }
    setRenderState("loading");
    let cancelled = false;
    let control: SignalSceneControl | undefined;
    void mountThreeScene(
      host,
      snapshotRef,
      sceneTheme,
      () => { if (!cancelled) setRenderState("ready"); },
      () => { if (!cancelled) setRenderState("fallback"); },
    ).then((nextControl) => {
      if (cancelled) nextControl.dispose();
      else { control = nextControl; sceneControlRef.current = nextControl; }
    }).catch(() => { if (!cancelled) setRenderState("fallback"); });
    return () => { cancelled = true; sceneControlRef.current = null; control?.dispose(); };
  }, [compact, sceneTheme, themeRevision]);

  useEffect(() => {
    sceneControlRef.current?.render();
  }, [active, nextRetryAt, upstreamState, waiting]);

  const stateLabel = upstreamState === "healthy" ? labels.healthy
    : upstreamState === "degraded" ? labels.degraded : labels.unknown;
  const classes = [
    "signal-topology",
    `signal-topology--${upstreamState}`,
    active > 0 ? "signal-topology--active" : "",
    waiting > 0 ? "signal-topology--waiting" : "",
    nextRetryAt ? "signal-topology--retry-scheduled" : "",
    renderState === "fallback" || renderState === "compact" ? "signal-topology--static" : "",
    selectedRequestId ? "signal-topology--focused" : "",
    className,
  ].filter(Boolean).join(" ");

  return <section className={classes} aria-label={labels.ariaLabel}>
    <div className="signal-topology__scene" ref={sceneHostRef} aria-hidden="true" />
    <ol className="signal-topology__nodes">
      <li className="signal-topology__node signal-topology__node--codex"><strong>{labels.codex}</strong></li>
      <li className="signal-topology__node signal-topology__node--relay"><strong>{labels.relay}</strong></li>
      <li className="signal-topology__node signal-topology__node--cpa"><strong>{labels.cpa}</strong></li>
    </ol>
    {visibleRequests.length > 0 && <div className="signal-topology__request-stack" aria-label={labels.active}>
      {visibleRequests.map((request, index) => <button
        key={request.id}
        type="button"
        className={`request-flow-card state-${request.state}${selectedRequestId === request.id ? " selected" : ""}${!selectedRequestId && primaryRequestId === request.id ? " primary" : ""}`}
        style={{ "--request-index": index } as CSSProperties}
        aria-label={`${request.method} ${request.path} · ${labels.stateLabels?.[request.state] || request.state}`}
        onClick={() => onSelect?.(request.id)}
      >
        <span><code>{request.id.slice(0, 12)}</code><i /></span>
        <strong>{request.method} {request.path}</strong>
        <small>{labels.stateLabels?.[request.state] || request.state} · #{request.attempt}</small>
      </button>)}
      {overflow > 0 && <span className="request-flow-overflow">+{overflow}</span>}
    </div>}
    <div className="signal-topology__summary">
      <span className={`signal-topology__state signal-topology__state--${upstreamState}`} role="status">{stateLabel}</span>
      <dl className="signal-topology__metrics">
        <div><dt>{labels.active}</dt><dd>{safeCount(active)}</dd></div>
        <div><dt>{labels.waiting}</dt><dd>{safeCount(waiting)}</dd></div>
        {nextRetryAt && <div><dt>{labels.nextRetry}</dt><dd><time dateTime={nextRetryAt}>{retryValue}</time></dd></div>}
      </dl>
    </div>
    {renderState === "fallback" && <p className="signal-topology__fallback">{labels.staticFallback}</p>}
  </section>;
}
