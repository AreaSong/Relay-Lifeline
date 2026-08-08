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
  codex: Mesh;
  codexHalo: Mesh;
  relay: Mesh;
  relayRingOuter: Mesh;
  relayRingInner: Mesh;
  cpa: Mesh;
  cpaHalo: Mesh;
  cpaMaterial: MeshStandardMaterial;
  connections: LineBasicMaterial[];
  retryRing: Mesh;
  retryMaterial: MeshBasicMaterial;
  signalMaterial: MeshBasicMaterial;
  signals: Mesh[];
  ambientParticles: Mesh[];
  lights: Array<{ intensity: number }>;
  resources: Array<{ dispose: () => void }>;
}

type ThreeModule = typeof import("three");

const fallbackTheme: SignalTopologyTheme = {
  foreground: "#e8edf2", muted: "#718096", relay: "#10b981", healthy: "#34d399",
  degraded: "#f59e0b", unknown: "#7e8b99", signal: "#34d399",
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
  // Geometries
  const nodeGeometry = new THREE.IcosahedronGeometry(0.48, 3);
  const relayGeometry = new THREE.OctahedronGeometry(0.68, 2);
  const signalGeometry = new THREE.SphereGeometry(0.065, 12, 12);
  const haloGeometry = new THREE.TorusGeometry(0.72, 0.015, 16, 64);
  const outerHaloGeometry = new THREE.TorusGeometry(0.92, 0.02, 16, 64);

  // Materials with Glow & Metalness
  const codexMaterial = new THREE.MeshStandardMaterial({ color: theme.foreground, roughness: 0.25, metalness: 0.25 });
  const codexHaloMaterial = new THREE.MeshBasicMaterial({ color: theme.foreground, transparent: true, opacity: 0.35 });
  const relayMaterial = new THREE.MeshStandardMaterial({ color: theme.relay, roughness: 0.15, metalness: 0.4, wireframe: false });
  const relayHaloMaterial = new THREE.MeshBasicMaterial({ color: theme.relay, transparent: true, opacity: 0.65 });
  const cpaMaterial = new THREE.MeshStandardMaterial({ color: theme.unknown, roughness: 0.25, metalness: 0.25 });
  const cpaHaloMaterial = new THREE.MeshBasicMaterial({ color: theme.unknown, transparent: true, opacity: 0.45 });
  const signalMaterial = new THREE.MeshBasicMaterial({ color: theme.signal, transparent: true, opacity: 0.95 });
  const retryMaterial = new THREE.MeshBasicMaterial({ color: theme.degraded, transparent: true, opacity: 0.75 });
  
  const connections = [0, 1].map(() => new THREE.LineBasicMaterial({ color: theme.muted, transparent: true, opacity: 0.45 }));
  const connectionGeometries = [
    new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(-2.8, 0, 0), new THREE.Vector3(-0.65, 0, 0)]),
    new THREE.BufferGeometry().setFromPoints([new THREE.Vector3(0.65, 0, 0), new THREE.Vector3(2.8, 0, 0)]),
  ];

  // Lights for realistic 3D specular highlight
  const pointLight1 = new THREE.PointLight("#10b981", 3.5, 10);
  pointLight1.position.set(0, 2, 3);
  const pointLight2 = new THREE.PointLight("#3b82f6", 2.0, 10);
  pointLight2.position.set(-3, -1, 2);
  const ambientLight = new THREE.AmbientLight("#ffffff", 0.65);
  scene.add(pointLight1, pointLight2, ambientLight);

  // Nodes & Halos
  const codex = new THREE.Mesh(nodeGeometry, codexMaterial);
  const codexHalo = new THREE.Mesh(haloGeometry, codexHaloMaterial);
  codex.position.x = -3.2; codexHalo.position.x = -3.2;

  const relay = new THREE.Mesh(relayGeometry, relayMaterial);
  const relayRingInner = new THREE.Mesh(haloGeometry, relayHaloMaterial);
  const relayRingOuter = new THREE.Mesh(outerHaloGeometry, relayHaloMaterial);

  const cpa = new THREE.Mesh(nodeGeometry, cpaMaterial);
  const cpaHalo = new THREE.Mesh(haloGeometry, cpaHaloMaterial);
  cpa.position.x = 3.2; cpaHalo.position.x = 3.2;

  scene.add(codex, codexHalo, relay, relayRingInner, relayRingOuter, cpa, cpaHalo);

  connectionGeometries.forEach((geometry, index) => scene.add(new THREE.Line(geometry, connections[index])));

  // High-Speed Photon Stream Particles
  const signals = Array.from({ length: 16 }, () => {
    const signal = new THREE.Mesh(signalGeometry, signalMaterial);
    scene.add(signal);
    return signal;
  });

  // Ambient Dust Floating Particles
  const dustGeometry = new THREE.SphereGeometry(0.02, 6, 6);
  const dustMaterial = new THREE.MeshBasicMaterial({ color: theme.signal, transparent: true, opacity: 0.25 });
  const ambientParticles = Array.from({ length: 24 }, (_, i) => {
    const particle = new THREE.Mesh(dustGeometry, dustMaterial);
    particle.position.set((Math.random() - 0.5) * 8, (Math.random() - 0.5) * 4, (Math.random() - 0.5) * 3);
    scene.add(particle);
    return particle;
  });

  const retryRing = new THREE.Mesh(outerHaloGeometry, retryMaterial);
  scene.add(retryRing);

  return {
    codex, codexHalo, relay, relayRingInner, relayRingOuter, cpa, cpaHalo, cpaMaterial,
    connections, retryRing, retryMaterial, signalMaterial, signals, ambientParticles,
    lights: [pointLight1, pointLight2],
    resources: [nodeGeometry, relayGeometry, signalGeometry, haloGeometry, outerHaloGeometry, dustGeometry,
      ...connectionGeometries, codexMaterial, codexHaloMaterial, relayMaterial, relayHaloMaterial, cpaMaterial,
      cpaHaloMaterial, signalMaterial, retryMaterial, dustMaterial, ...connections],
  };
}

function updateScene(objects: TopologyObjects, snapshot: TopologySnapshot, theme: SignalTopologyTheme, elapsed: number, motionAllowed: boolean) {
  const activeSignals = Math.min(objects.signals.length, safeCount(snapshot.active));
  const speed = motionAllowed ? elapsed * (0.28 + Math.min(snapshot.active, 24) * 0.015) : 0.4;
  
  // Update Laser Photon Stream Particles
  objects.signals.forEach((signal, index) => {
    signal.visible = index < activeSignals || (index === 0 && snapshot.active === 0);
    const progress = (speed + index / Math.max(1, activeSignals)) % 1;
    const waveY = Math.sin(progress * Math.PI * 2) * 0.08;
    signal.position.set(-2.8 + progress * 5.6, waveY, 0.1);
    const scale = 0.7 + Math.sin(progress * Math.PI) * 0.5;
    signal.scale.setScalar(scale);
  });

  // Smooth Rotation for Holographic Halos
  if (motionAllowed) {
    objects.relay.rotation.y = elapsed * 0.65;
    objects.relay.rotation.x = elapsed * 0.35;
    objects.relayRingInner.rotation.x = elapsed * 0.8;
    objects.relayRingInner.rotation.y = elapsed * 0.4;
    objects.relayRingOuter.rotation.z = -elapsed * 0.6;
    objects.relayRingOuter.rotation.x = elapsed * 0.2;
    objects.codexHalo.rotation.y = elapsed * 0.4;
    objects.cpaHalo.rotation.y = -elapsed * 0.4;

    // Ambient dust particles slow drift
    objects.ambientParticles.forEach((particle, i) => {
      particle.position.y += Math.sin(elapsed + i) * 0.0015;
    });
  }

  const retrying = snapshot.waiting > 0 || Boolean(snapshot.nextRetryAt);
  const pulse = motionAllowed ? 1 + Math.sin(elapsed * 4.0) * 0.05 : 1;
  objects.relay.scale.setScalar(retrying ? pulse * 1.08 : pulse);
  objects.retryRing.visible = retrying;
  objects.retryRing.rotation.z = motionAllowed ? elapsed * 0.8 : 0;
  
  // Dynamic Theme State Color Mapping
  const currentStateColor = stateColor(snapshot.upstreamState, theme);
  objects.cpaMaterial.color.set(currentStateColor);
  objects.cpaHalo.children;
  objects.connections[1].color.set(currentStateColor);
  objects.connections[1].opacity = snapshot.upstreamState === "healthy" ? 0.85 : 0.45;
  objects.signalMaterial.opacity = snapshot.upstreamState === "degraded" ? 0.65 : 0.95;
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
