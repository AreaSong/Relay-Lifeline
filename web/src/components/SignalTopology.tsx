import { useEffect, useMemo, useRef, useState } from "react";
import type { MutableRefObject } from "react";
import type {
  LineBasicMaterial,
  Mesh,
  MeshBasicMaterial,
  MeshStandardMaterial,
  PerspectiveCamera,
  Scene,
  WebGLRenderer,
} from "three";

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
}

interface TopologySnapshot {
  upstreamState: SignalUpstreamState;
  active: number;
  waiting: number;
  nextRetryAt?: string;
}

interface TopologyObjects {
  relay: Mesh;
  cpaMaterial: MeshStandardMaterial;
  connections: LineBasicMaterial[];
  retryRing: Mesh;
  retryMaterial: MeshBasicMaterial;
  signalMaterial: MeshBasicMaterial;
  signals: Mesh[];
  resources: Array<{ dispose: () => void }>;
}

type ThreeModule = typeof import("three");
type RenderState = "loading" | "ready" | "fallback";

const fallbackTheme: SignalTopologyTheme = {
  foreground: "#e8edf2",
  muted: "#718096",
  relay: "#3aa6a0",
  healthy: "#3fb984",
  degraded: "#e2a93b",
  unknown: "#7e8b99",
  signal: "#d7f1ee",
};

function safeCount(value: number) {
  return Number.isFinite(value) ? Math.max(0, Math.round(value)) : 0;
}

function stateColor(state: SignalUpstreamState, theme: SignalTopologyTheme) {
  if (state === "healthy") return theme.healthy;
  if (state === "degraded") return theme.degraded;
  return theme.unknown;
}

function resolveTheme(host: HTMLElement, supplied?: Partial<SignalTopologyTheme>): SignalTopologyTheme {
  const styles = window.getComputedStyle(host);
  const resolve = (key: keyof SignalTopologyTheme) =>
    supplied?.[key] || styles.getPropertyValue(`--signal-topology-${key}`).trim() || fallbackTheme[key];

  return {
    foreground: resolve("foreground"),
    muted: resolve("muted"),
    relay: resolve("relay"),
    healthy: resolve("healthy"),
    degraded: resolve("degraded"),
    unknown: resolve("unknown"),
    signal: resolve("signal"),
  };
}

function createTopologyObjects(THREE: ThreeModule, scene: Scene, theme: SignalTopologyTheme): TopologyObjects {
  const nodeGeometry = new THREE.IcosahedronGeometry(0.46, 2);
  const relayGeometry = new THREE.OctahedronGeometry(0.62, 1);
  const signalGeometry = new THREE.SphereGeometry(0.055, 10, 8);
  const ringGeometry = new THREE.TorusGeometry(0.82, 0.025, 8, 64);
  const codexMaterial = new THREE.MeshStandardMaterial({ color: theme.foreground, roughness: 0.48 });
  const relayMaterial = new THREE.MeshStandardMaterial({ color: theme.relay, roughness: 0.32, metalness: 0.08 });
  const cpaMaterial = new THREE.MeshStandardMaterial({ color: theme.unknown, roughness: 0.48 });
  const signalMaterial = new THREE.MeshBasicMaterial({ color: theme.signal, transparent: true, opacity: 0.86 });
  const retryMaterial = new THREE.MeshBasicMaterial({ color: theme.degraded, transparent: true, opacity: 0.68 });
  const connectionMaterials = [0, 1].map(() => new THREE.LineBasicMaterial({ color: theme.muted, transparent: true, opacity: 0.42 }));
  const connectionGeometries = [
    new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(-2.7, 0, 0), new THREE.Vector3(-0.55, 0, 0)]),
    new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(0.55, 0, 0), new THREE.Vector3(2.7, 0, 0)]),
  ];
  const codex = new THREE.Mesh(nodeGeometry, codexMaterial);
  const relay = new THREE.Mesh(relayGeometry, relayMaterial);
  const cpa = new THREE.Mesh(nodeGeometry, cpaMaterial);
  codex.position.x = -3.1;
  cpa.position.x = 3.1;
  scene.add(codex, relay, cpa);
  connectionGeometries.forEach((geometry, index) => scene.add(new THREE.Line(geometry, connectionMaterials[index])));
  const signals = Array.from({ length: 12 }, () => {
    const signal = new THREE.Mesh(signalGeometry, signalMaterial);
    scene.add(signal);
    return signal;
  });
  const retryRing = new THREE.Mesh(ringGeometry, retryMaterial);
  scene.add(retryRing);

  return {
    relay,
    cpaMaterial,
    connections: connectionMaterials,
    retryRing,
    retryMaterial,
    signalMaterial,
    signals,
    resources: [nodeGeometry, relayGeometry, signalGeometry, ringGeometry, ...connectionGeometries, codexMaterial,
      relayMaterial, cpaMaterial, signalMaterial, retryMaterial, ...connectionMaterials],
  };
}

function retryProgress(nextRetryAt?: string) {
  if (!nextRetryAt) return 0;
  const remaining = new Date(nextRetryAt).getTime() - Date.now();
  if (!Number.isFinite(remaining)) return 0;
  return 1 - Math.min(1, Math.max(0, remaining / 30_000));
}

function updateScene(
  objects: TopologyObjects,
  snapshot: TopologySnapshot,
  theme: SignalTopologyTheme,
  elapsed: number,
  motionAllowed: boolean,
) {
  const activeSignals = Math.min(objects.signals.length, safeCount(snapshot.active));
  const speed = 0.11 + Math.min(snapshot.active, 24) * 0.006;
  const phase = motionAllowed ? elapsed * speed : 0.34;
  objects.signals.forEach((signal, index) => {
    signal.visible = index < activeSignals;
    const progress = (phase + index / Math.max(1, activeSignals)) % 1;
    signal.position.set(-2.68 + progress * 5.36, Math.sin(progress * Math.PI) * 0.14, 0.08);
  });
  const retrying = snapshot.waiting > 0 || Boolean(snapshot.nextRetryAt);
  const pulse = motionAllowed ? 1 + Math.sin(elapsed * 3.2) * 0.035 : 1;
  objects.relay.scale.setScalar(retrying ? pulse + retryProgress(snapshot.nextRetryAt) * 0.06 : 1);
  objects.retryRing.visible = retrying;
  objects.retryRing.rotation.z = motionAllowed ? elapsed * 0.45 : 0;
  objects.retryMaterial.opacity = 0.38 + Math.min(snapshot.waiting, 10) * 0.035;
  objects.cpaMaterial.color.set(stateColor(snapshot.upstreamState, theme));
  objects.connections[1].color.set(stateColor(snapshot.upstreamState, theme));
  objects.connections[1].opacity = snapshot.upstreamState === "healthy" ? 0.72 : 0.46;
  objects.signalMaterial.opacity = snapshot.upstreamState === "degraded" ? 0.58 : 0.86;
}

function resizeRenderer(host: HTMLElement, renderer: WebGLRenderer, camera: PerspectiveCamera) {
  const width = Math.max(1, host.clientWidth);
  const height = Math.max(1, host.clientHeight);
  renderer.setSize(width, height, false);
  camera.aspect = width / height;
  camera.position.z = Math.max(9.2, 10.8 / camera.aspect);
  camera.updateProjectionMatrix();
}

async function mountThreeScene(
  host: HTMLElement,
  snapshotRef: MutableRefObject<TopologySnapshot>,
  suppliedTheme: Partial<SignalTopologyTheme> | undefined,
  onReady: () => void,
  onFallback: () => void,
) {
  const THREE = await import("three");
  const theme = resolveTheme(host, suppliedTheme);
  const renderer = new THREE.WebGLRenderer({ alpha: true, antialias: true, powerPreference: "low-power" });
  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(38, 1, 0.1, 30);
  const objects = createTopologyObjects(THREE, scene, theme);
  const media = window.matchMedia("(prefers-reduced-motion: reduce)");
  let disposed = false;
  let sceneAvailable = true;
  let frame = 0;
  let motionAllowed = !media.matches;
  let cleaned = false;
  let observer: ResizeObserver | undefined;

  const draw = (timestamp: number) => {
    if (disposed || !sceneAvailable) return;
    updateScene(objects, snapshotRef.current, theme, timestamp / 1000, motionAllowed);
    renderer.render(scene, camera);
    if (motionAllowed && !document.hidden) frame = window.requestAnimationFrame(draw);
  };
  const start = () => {
    window.cancelAnimationFrame(frame);
    if (!sceneAvailable) return;
    draw(motionAllowed ? performance.now() : 0);
  };
  const onVisibility = () => { if (!document.hidden) start(); else window.cancelAnimationFrame(frame); };
  const onMotion = (event: MediaQueryListEvent) => { motionAllowed = !event.matches; start(); };
  function cleanup() {
    if (cleaned) return;
    cleaned = true;
    disposed = true;
    window.cancelAnimationFrame(frame);
    observer?.disconnect();
    document.removeEventListener("visibilitychange", onVisibility);
    media.removeEventListener("change", onMotion);
    renderer.domElement.removeEventListener("webglcontextlost", onContextLost);
    objects.resources.forEach((resource) => resource.dispose());
    renderer.dispose();
    renderer.domElement.remove();
  }
  function onContextLost(event: Event) {
    event.preventDefault();
    if (cleaned) return;
    sceneAvailable = false;
    cleanup();
    onFallback();
  }
  const resize = () => { resizeRenderer(host, renderer, camera); start(); };
  try {
    camera.position.set(0, 0.25, 9.2);
    scene.add(new THREE.AmbientLight(theme.foreground, 1.7));
    const keyLight = new THREE.DirectionalLight(theme.signal, 2.2);
    keyLight.position.set(-1, 4, 5);
    scene.add(keyLight);
    renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, window.innerWidth <= 820 ? 1.25 : 2));
    renderer.outputColorSpace = THREE.SRGBColorSpace;
    renderer.domElement.className = "signal-topology__canvas";
    host.replaceChildren(renderer.domElement);
    observer = new ResizeObserver(resize);
    observer.observe(host);
    document.addEventListener("visibilitychange", onVisibility);
    media.addEventListener("change", onMotion);
    renderer.domElement.addEventListener("webglcontextlost", onContextLost);
    resize();
    onReady();
    return cleanup;
  } catch (error) {
    cleanup();
    throw error;
  }
}

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
}: SignalTopologyProps) {
  const sceneHostRef = useRef<HTMLDivElement>(null);
  const snapshotRef = useRef<TopologySnapshot>({ upstreamState, active, waiting, nextRetryAt });
  const [renderState, setRenderState] = useState<RenderState>("loading");
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

  useEffect(() => {
    const host = sceneHostRef.current;
    if (!host) return;
    let cancelled = false;
    let dispose: (() => void) | undefined;
    void mountThreeScene(
      host,
      snapshotRef,
      sceneTheme,
      () => { if (!cancelled) setRenderState("ready"); },
      () => { if (!cancelled) setRenderState("fallback"); },
    ).then((cleanup) => {
      if (cancelled) cleanup();
      else dispose = cleanup;
    }).catch(() => { if (!cancelled) setRenderState("fallback"); });
    return () => { cancelled = true; dispose?.(); };
  }, [sceneTheme]);

  const stateLabel = upstreamState === "healthy" ? labels.healthy
    : upstreamState === "degraded" ? labels.degraded : labels.unknown;
  const classes = [
    "signal-topology",
    `signal-topology--${upstreamState}`,
    active > 0 ? "signal-topology--active" : "",
    waiting > 0 ? "signal-topology--waiting" : "",
    nextRetryAt ? "signal-topology--retry-scheduled" : "",
    renderState === "fallback" ? "signal-topology--static" : "",
    className,
  ].filter(Boolean).join(" ");

  return <section className={classes} aria-label={labels.ariaLabel}>
    <div className="signal-topology__scene" ref={sceneHostRef} aria-hidden="true" />
    <ol className="signal-topology__nodes">
      <li className="signal-topology__node signal-topology__node--codex"><strong>{labels.codex}</strong></li>
      <li className="signal-topology__node signal-topology__node--relay"><strong>{labels.relay}</strong></li>
      <li className="signal-topology__node signal-topology__node--cpa"><strong>{labels.cpa}</strong></li>
    </ol>
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
