import type { MutableRefObject } from "react";
import type {
  LineBasicMaterial, Mesh, MeshBasicMaterial, MeshStandardMaterial, PerspectiveCamera, Scene, WebGLRenderer,
} from "three";
import type { SignalTopologyTheme, SignalUpstreamState } from "./SignalTopology";

export interface TopologySnapshot {
  upstreamState: SignalUpstreamState;
  active: number;
  waiting: number;
  nextRetryAt?: string;
}

export interface SignalSceneControl {
  render: () => void;
  dispose: () => void;
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

const fallbackTheme: SignalTopologyTheme = {
  foreground: "#e8edf2", muted: "#718096", relay: "#3aa6a0", healthy: "#3fb984",
  degraded: "#e2a93b", unknown: "#7e8b99", signal: "#d7f1ee",
};

export function safeCount(value: number) {
  return Number.isFinite(value) ? Math.max(0, Math.round(value)) : 0;
}

function stateColor(state: SignalUpstreamState, theme: SignalTopologyTheme) {
  if (state === "healthy") return theme.healthy;
  if (state === "degraded") return theme.degraded;
  return theme.unknown;
}

function resolveTheme(host: HTMLElement, supplied?: Partial<SignalTopologyTheme>): SignalTopologyTheme {
  const styles = window.getComputedStyle(host);
  const resolve = (key: keyof SignalTopologyTheme) => supplied?.[key]
    || styles.getPropertyValue(`--signal-topology-${key}`).trim() || fallbackTheme[key];
  return {
    foreground: resolve("foreground"), muted: resolve("muted"), relay: resolve("relay"),
    healthy: resolve("healthy"), degraded: resolve("degraded"), unknown: resolve("unknown"), signal: resolve("signal"),
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
  const connections = [0, 1].map(() => new THREE.LineBasicMaterial({ color: theme.muted, transparent: true, opacity: 0.42 }));
  const connectionGeometries = [
    new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(-2.7, 0, 0), new THREE.Vector3(-0.55, 0, 0)]),
    new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(0.55, 0, 0), new THREE.Vector3(2.7, 0, 0)]),
  ];
  const codex = new THREE.Mesh(nodeGeometry, codexMaterial);
  const relay = new THREE.Mesh(relayGeometry, relayMaterial);
  const cpa = new THREE.Mesh(nodeGeometry, cpaMaterial);
  codex.position.x = -3.1; cpa.position.x = 3.1;
  scene.add(codex, relay, cpa);
  connectionGeometries.forEach((geometry, index) => scene.add(new THREE.Line(geometry, connections[index])));
  const signals = Array.from({ length: 12 }, () => { const signal = new THREE.Mesh(signalGeometry, signalMaterial); scene.add(signal); return signal; });
  const retryRing = new THREE.Mesh(ringGeometry, retryMaterial);
  scene.add(retryRing);
  return {
    relay, cpaMaterial, connections, retryRing, retryMaterial, signalMaterial, signals,
    resources: [nodeGeometry, relayGeometry, signalGeometry, ringGeometry, ...connectionGeometries, codexMaterial,
      relayMaterial, cpaMaterial, signalMaterial, retryMaterial, ...connections],
  };
}

function updateScene(objects: TopologyObjects, snapshot: TopologySnapshot, theme: SignalTopologyTheme, elapsed: number, motionAllowed: boolean) {
  const activeSignals = Math.min(objects.signals.length, safeCount(snapshot.active));
  const phase = motionAllowed ? elapsed * (0.11 + Math.min(snapshot.active, 24) * 0.006) : 0.34;
  objects.signals.forEach((signal, index) => {
    signal.visible = index < activeSignals;
    const progress = (phase + index / Math.max(1, activeSignals)) % 1;
    signal.position.set(-2.68 + progress * 5.36, Math.sin(progress * Math.PI) * 0.14, 0.08);
  });
  const retrying = snapshot.waiting > 0 || Boolean(snapshot.nextRetryAt);
  const retryAt = snapshot.nextRetryAt ? new Date(snapshot.nextRetryAt).getTime() - Date.now() : 0;
  const retryProgress = Number.isFinite(retryAt) ? 1 - Math.min(1, Math.max(0, retryAt / 30_000)) : 0;
  const pulse = motionAllowed ? 1 + Math.sin(elapsed * 3.2) * 0.035 : 1;
  objects.relay.scale.setScalar(retrying ? pulse + retryProgress * 0.06 : 1);
  objects.retryRing.visible = retrying;
  objects.retryRing.rotation.z = motionAllowed ? elapsed * 0.45 : 0;
  objects.retryMaterial.opacity = 0.38 + Math.min(snapshot.waiting, 10) * 0.035;
  objects.cpaMaterial.color.set(stateColor(snapshot.upstreamState, theme));
  objects.connections[1].color.set(stateColor(snapshot.upstreamState, theme));
  objects.connections[1].opacity = snapshot.upstreamState === "healthy" ? 0.72 : 0.46;
  objects.signalMaterial.opacity = snapshot.upstreamState === "degraded" ? 0.58 : 0.86;
}

function resize(host: HTMLElement, renderer: WebGLRenderer, camera: PerspectiveCamera) {
  const width = Math.max(1, host.clientWidth);
  const height = Math.max(1, host.clientHeight);
  renderer.setSize(width, height, false);
  camera.aspect = width / height;
  camera.position.z = Math.max(9.2, 10.8 / camera.aspect);
  camera.updateProjectionMatrix();
}

export async function mountThreeScene(host: HTMLElement, snapshotRef: MutableRefObject<TopologySnapshot>, suppliedTheme: Partial<SignalTopologyTheme> | undefined, onReady: () => void, onFallback: () => void): Promise<SignalSceneControl> {
  const THREE = await import("three");
  const theme = resolveTheme(host, suppliedTheme);
  const renderer = new THREE.WebGLRenderer({ alpha: true, antialias: true, powerPreference: "low-power" });
  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(38, 1, 0.1, 30);
  const objects = createTopologyObjects(THREE, scene, theme);
  const media = window.matchMedia("(prefers-reduced-motion: reduce)");
  let disposed = false;
  let available = true;
  let frame = 0;
  let lastRender = 0;
  let motionAllowed = !media.matches;
  let cleaned = false;
  let observer: ResizeObserver | undefined;

  const draw = (timestamp: number) => {
    if (disposed || !available) return;
    if (motionAllowed && timestamp - lastRender < 32) {
      frame = requestAnimationFrame(draw);
      return;
    }
    lastRender = timestamp;
    updateScene(objects, snapshotRef.current, theme, timestamp / 1000, motionAllowed);
    renderer.render(scene, camera);
    const snapshot = snapshotRef.current;
    if (motionAllowed && !document.hidden && (snapshot.active > 0 || snapshot.waiting > 0)) frame = requestAnimationFrame(draw);
  };
  const start = () => { cancelAnimationFrame(frame); if (available) draw(motionAllowed ? performance.now() : 0); };
  const onVisibility = () => { if (document.hidden) cancelAnimationFrame(frame); else start(); };
  const onMotion = (event: MediaQueryListEvent) => { motionAllowed = !event.matches; start(); };
  function cleanup() {
    if (cleaned) return;
    cleaned = true; disposed = true; cancelAnimationFrame(frame); observer?.disconnect();
    document.removeEventListener("visibilitychange", onVisibility); media.removeEventListener("change", onMotion);
    renderer.domElement.removeEventListener("webglcontextlost", onContextLost);
    objects.resources.forEach((resource) => resource.dispose()); renderer.dispose(); renderer.domElement.remove();
  }
  function onContextLost(event: Event) { event.preventDefault(); if (!cleaned) { available = false; cleanup(); onFallback(); } }

  try {
    camera.position.set(0, 0.25, 9.2);
    scene.add(new THREE.AmbientLight(theme.foreground, 1.7));
    const keyLight = new THREE.DirectionalLight(theme.signal, 2.2); keyLight.position.set(-1, 4, 5); scene.add(keyLight);
    renderer.setPixelRatio(Math.min(devicePixelRatio || 1, innerWidth <= 820 ? 1.25 : 2));
    renderer.outputColorSpace = THREE.SRGBColorSpace;
    renderer.domElement.className = "signal-topology__canvas";
    host.replaceChildren(renderer.domElement);
    observer = new ResizeObserver(() => { resize(host, renderer, camera); start(); }); observer.observe(host);
    document.addEventListener("visibilitychange", onVisibility); media.addEventListener("change", onMotion);
    renderer.domElement.addEventListener("webglcontextlost", onContextLost);
    resize(host, renderer, camera); start(); onReady();
    return { render: start, dispose: cleanup };
  } catch (error) { cleanup(); throw error; }
}
