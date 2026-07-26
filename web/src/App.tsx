import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import {
  Activity, CirclePause, CirclePlay, Clock3, HeartPulse, LogOut,
  RefreshCw, RotateCcw, Save, Settings2, ShieldCheck, Square, TimerReset,
} from "lucide-react";
import { ApiClient } from "./api";
import type { Config, RequestInfo, Status } from "./types";

type View = "overview" | "requests" | "settings";

function formatAge(value: string) {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

function Login({ onLogin }: { onLogin: (token: string) => Promise<void> }) {
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try { await onLogin(token); } catch (reason) { setError(reason instanceof Error ? reason.message : "连接失败"); }
    finally { setBusy(false); }
  }
  return <main className="login-shell">
    <form className="login-panel" onSubmit={submit}>
      <div className="brand-mark"><HeartPulse size={24} /></div>
      <h1>Relay Lifeline</h1>
      <p>中转生命线控制台</p>
      <label className="field"><span>管理密钥</span><input type="password" value={token} onChange={(event) => setToken(event.target.value)} autoFocus required /></label>
      {error && <div className="error-banner">{error}</div>}
      <button className="button primary" disabled={busy || !token}><ShieldCheck size={17} />{busy ? "验证中" : "进入控制台"}</button>
    </form>
  </main>;
}

function Stat({ icon, label, value }: { icon: React.ReactNode; label: string; value: number | string }) {
  return <div className="stat"><span className="stat-icon">{icon}</span><div><span>{label}</span><strong>{value}</strong></div></div>;
}

function RequestsTable({ requests, api, refresh, onError }: { requests: RequestInfo[]; api: ApiClient; refresh: () => Promise<void>; onError: (message: string) => void }) {
  async function act(action: () => Promise<unknown>) {
    try { await action(); await refresh(); } catch (reason) { onError(reason instanceof Error ? reason.message : "操作失败"); }
  }
  if (!requests.length) return <div className="empty-state"><Activity size={24} /><span>当前没有活动请求</span></div>;
  return <div className="table-wrap"><table><thead><tr><th>请求</th><th>状态</th><th>尝试</th><th>持续时间</th><th>下次重试</th><th aria-label="操作" /></tr></thead>
    <tbody>{requests.map((request) => <tr key={request.id}>
      <td><strong>{request.method} {request.path}</strong><span className="subtle">{request.id}</span></td>
      <td><span className={`status ${request.state}`}>{request.state}</span>{request.lastError && <span className="subtle">{request.lastError}</span>}</td>
      <td>{request.attempt}</td><td>{formatAge(request.startedAt)}</td>
      <td>{request.nextRetryAt ? new Date(request.nextRetryAt).toLocaleTimeString() : "-"}</td>
      <td><div className="row-actions">
        <button className="icon-button" data-tooltip="立即重试" aria-label="立即重试" onClick={() => act(() => api.retry(request.id))}><TimerReset size={17} /></button>
        <button className="icon-button danger" data-tooltip="取消请求" aria-label="取消请求" onClick={() => act(() => api.cancel(request.id))}><Square size={16} /></button>
      </div></td>
    </tr>)}</tbody></table></div>;
}

function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (value: boolean) => void; label: string }) {
  return <label className="toggle"><input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} /><span className="toggle-track" /><span>{label}</span></label>;
}

function Settings({ config, setConfig, save, reload }: { config: Config; setConfig: (value: Config) => void; save: () => Promise<void>; reload: () => Promise<void> }) {
  const setServer = (patch: Partial<Config["server"]>) => setConfig({ ...config, server: { ...config.server, ...patch } });
  const setUpstream = (patch: Partial<Config["upstream"]>) => setConfig({ ...config, upstream: { ...config.upstream, ...patch } });
  const setRetry = (patch: Partial<Config["retry"]>) => setConfig({ ...config, retry: { ...config.retry, ...patch } });
  const setStream = (patch: Partial<Config["stream"]>) => setConfig({ ...config, stream: { ...config.stream, ...patch } });
  const setQueue = (patch: Partial<Config["queue"]>) => setConfig({ ...config, queue: { ...config.queue, ...patch } });
  const setNotifications = (patch: Partial<Config["notifications"]>) => setConfig({ ...config, notifications: { ...config.notifications, ...patch } });
  const setLogging = (patch: Partial<Config["logging"]>) => setConfig({ ...config, logging: { ...config.logging, ...patch } });
  return <div className="settings-stack">
    <section><div className="section-heading"><div><h2>服务与上游</h2><p>监听地址及目标中转站连接</p></div></div><div className="form-grid">
      <label className="field"><span>监听地址</span><input value={config.server.listen} onChange={(event) => setServer({ listen: event.target.value })} /></label>
      <label className="field"><span>请求体上限</span><input value={config.server.maxRequestBody} onChange={(event) => setServer({ maxRequestBody: event.target.value })} /></label>
      <label className="field wide"><span>上游 Base URL</span><input type="url" value={config.upstream.baseUrl} onChange={(event) => setUpstream({ baseUrl: event.target.value })} /></label>
      <label className="field"><span>连接超时</span><input value={config.upstream.connectTimeout} onChange={(event) => setUpstream({ connectTimeout: event.target.value })} /></label>
      <label className="field"><span>响应头超时</span><input value={config.upstream.responseHeaderTimeout} onChange={(event) => setUpstream({ responseHeaderTimeout: event.target.value })} /></label>
    </div></section>
    <section><div className="section-heading"><div><h2>重试策略</h2><p>失败请求的等待与恢复规则</p></div><Toggle label="启用重试" checked={config.retry.enabled} onChange={(enabled) => setRetry({ enabled })} /></div>
      <div className="form-grid">
        <label className="field"><span>错误范围</span><select value={config.retry.mode} onChange={(event) => setRetry({ mode: event.target.value as Config["retry"]["mode"] })}><option value="all-errors">所有错误</option><option value="transient-errors">仅临时错误</option></select></label>
        <label className="field"><span>最大尝试次数</span><input type="number" min="0" value={config.retry.maxAttempts} onChange={(event) => setRetry({ maxAttempts: Number(event.target.value) })} /></label>
        <label className="field"><span>最小等待</span><input value={config.retry.minInterval} onChange={(event) => setRetry({ minInterval: event.target.value })} /></label>
        <label className="field"><span>最大等待</span><input value={config.retry.maxInterval} onChange={(event) => setRetry({ maxInterval: event.target.value })} /></label>
      </div><Toggle label="遵循 Retry-After" checked={config.retry.honorRetryAfter} onChange={(honorRetryAfter) => setRetry({ honorRetryAfter })} />
    </section>
    <section><div className="section-heading"><div><h2>流式与缓存</h2><p>SSE 保活及完整响应交付</p></div></div><div className="form-grid">
      <label className="field"><span>心跳间隔</span><input value={config.stream.heartbeatInterval} onChange={(event) => setStream({ heartbeatInterval: event.target.value })} /></label>
      <label className="field"><span>内存缓存上限</span><input value={config.stream.memoryLimit} onChange={(event) => setStream({ memoryLimit: event.target.value })} /></label>
      <label className="field wide"><span>临时目录</span><input value={config.stream.tempDir} onChange={(event) => setStream({ tempDir: event.target.value })} placeholder="系统默认" /></label>
    </div></section>
    <section><div className="section-heading"><div><h2>并发与通知</h2><p>队列容量、恢复速度和故障通知</p></div></div><div className="form-grid">
      <label className="field"><span>最大活动请求</span><input type="number" min="1" value={config.queue.maxActive} onChange={(event) => setQueue({ maxActive: Number(event.target.value) })} /></label>
      <label className="field"><span>最大等待请求</span><input type="number" min="0" value={config.queue.maxWaiting} onChange={(event) => setQueue({ maxWaiting: Number(event.target.value) })} /></label>
      <label className="field"><span>恢复放行间隔</span><input value={config.queue.recoverySpacing} onChange={(event) => setQueue({ recoverySpacing: event.target.value })} /></label>
      <label className="field"><span>故障通知阈值</span><input value={config.notifications.stalledAfter} onChange={(event) => setNotifications({ stalledAfter: event.target.value })} /></label>
      <label className="field wide"><span>通知 Webhook</span><input type="url" value={config.notifications.webhookUrl} onChange={(event) => setNotifications({ webhookUrl: event.target.value })} placeholder="https://" /></label>
    </div><Toggle label="恢复后通知" checked={config.notifications.notifyOnRecovery} onChange={(notifyOnRecovery) => setNotifications({ notifyOnRecovery })} /></section>
    <section><div className="section-heading"><div><h2>日志</h2><p>结构化运行日志级别</p></div></div><div className="form-grid">
      <label className="field"><span>日志级别</span><select value={config.logging.level} onChange={(event) => setLogging({ level: event.target.value })}><option value="debug">Debug</option><option value="info">Info</option><option value="warn">Warn</option><option value="error">Error</option></select></label>
    </div></section>
    <div className="settings-actions"><button className="button" onClick={reload}><RotateCcw size={17} />重新载入</button><button className="button primary" onClick={save}><Save size={17} />保存设置</button></div>
  </div>;
}

export function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem("relay-lifeline-token") || "");
  const [view, setView] = useState<View>("overview");
  const [status, setStatus] = useState<Status | null>(null);
  const [config, setConfig] = useState<Config | null>(null);
  const [message, setMessage] = useState("");
  const [messageKind, setMessageKind] = useState<"success" | "error">("success");
  const api = useMemo(() => new ApiClient(token), [token]);
  const refresh = useCallback(async () => { setStatus(await api.status()); }, [api]);

  async function login(value: string) { await new ApiClient(value).session(); sessionStorage.setItem("relay-lifeline-token", value); setToken(value); }
  function logout() { sessionStorage.removeItem("relay-lifeline-token"); setToken(""); setStatus(null); setConfig(null); }
  useEffect(() => { if (!token) return; Promise.all([refresh(), api.config().then(setConfig)]).catch(logout); const timer = window.setInterval(() => refresh().catch(() => undefined), 2000); return () => clearInterval(timer); }, [token, api, refresh]);
  if (!token) return <Login onLogin={login} />;
  if (!status || !config) return <div className="loading"><HeartPulse size={26} />正在连接 Relay Lifeline</div>;

  function showMessage(value: string, kind: "success" | "error" = "success") { setMessage(value); setMessageKind(kind); window.setTimeout(() => setMessage(""), 4000); }
  async function togglePause() { try { status!.paused ? await api.resume() : await api.pause(); await refresh(); } catch (reason) { showMessage(reason instanceof Error ? reason.message : "操作失败", "error"); } }
  async function save() { try { const result = await api.saveConfig(config!); showMessage(result.restartRequired ? "设置已保存，部分配置重启后生效" : "设置已保存并生效"); } catch (reason) { showMessage(reason instanceof Error ? reason.message : "保存失败", "error"); } }
  async function reload() { try { await api.reloadConfig(); setConfig(await api.config()); showMessage("已从配置文件重新载入"); } catch (reason) { showMessage(reason instanceof Error ? reason.message : "载入失败", "error"); } }

  return <div className="app-shell">
    <aside><div className="brand"><span className="brand-mark"><HeartPulse size={21} /></span><div><strong>Relay Lifeline</strong><span>中转生命线</span></div></div>
      <nav><button className={view === "overview" ? "active" : ""} onClick={() => setView("overview")}><Activity size={18} />概览</button><button className={view === "requests" ? "active" : ""} onClick={() => setView("requests")}><Clock3 size={18} />请求</button><button className={view === "settings" ? "active" : ""} onClick={() => setView("settings")}><Settings2 size={18} />设置</button></nav>
      <button className="logout" onClick={logout}><LogOut size={17} />退出</button>
    </aside>
    <main className="workspace"><header><div><h1>{view === "overview" ? "运行概览" : view === "requests" ? "活动请求" : "网关设置"}</h1><div className="health-row"><span className="connection"><i />Gateway 在线</span><span className={`connection upstream-${status.upstream.state}`}><i />上游{status.upstream.state === "healthy" ? "正常" : status.upstream.state === "degraded" ? "异常" : "未知"}</span></div></div><div className="header-actions"><button className="icon-button" aria-label="刷新" data-tooltip="刷新状态" onClick={refresh}><RefreshCw size={17} /></button><button className={`button ${status.paused ? "primary" : ""}`} onClick={togglePause}>{status.paused ? <CirclePlay size={17} /> : <CirclePause size={17} />}{status.paused ? "恢复" : "暂停"}</button></div></header>
      {message && <div className={messageKind === "success" ? "success-banner" : "error-banner page-banner"}>{message}</div>}
      {view === "overview" && <><div className="stats"><Stat icon={<Activity />} label="活动请求" value={status.active} /><Stat icon={<ShieldCheck />} label="成功请求" value={status.successful} /><Stat icon={<RotateCcw />} label="失败尝试" value={status.failedAttempts} /></div><section className="content-section"><div className="section-heading"><div><h2>最近活动</h2><p>当前处理中及等待恢复的请求</p></div><span className={`mode ${status.paused ? "paused" : ""}`}>{status.paused ? "已暂停" : "运行中"}</span></div><RequestsTable requests={status.requests.slice(0, 6)} api={api} refresh={refresh} onError={(value) => showMessage(value, "error")} /></section></>}
      {view === "requests" && <section className="content-section"><div className="section-heading"><div><h2>请求队列</h2><p>所有活动和等待重试的连接</p></div><span>{status.requests.length} 个请求</span></div><RequestsTable requests={status.requests} api={api} refresh={refresh} onError={(value) => showMessage(value, "error")} /></section>}
      {view === "settings" && <Settings config={config} setConfig={setConfig} save={save} reload={reload} />}
    </main>
  </div>;
}
