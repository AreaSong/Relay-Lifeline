import { Activity, Infinity as InfinityIcon, Pause, Play, RotateCcw, Square } from "lucide-react";
import { useTranslation } from "react-i18next";
import { errorMessage, type ApiClient } from "../api";
import { formatOptionalTime } from "../format";
import type { RepeatTask } from "../types";

export function RepeatTasksPanel({ tasks, api, refresh, onError, canOperate }: {
  tasks: RepeatTask[];
  api: ApiClient;
  refresh: () => Promise<void>;
  onError: (message: string) => void;
  canOperate: boolean;
}) {
  const { t, i18n } = useTranslation(["common", "requests"]);
  async function act(action: () => Promise<unknown>) {
    try { await action(); await refresh(); }
    catch (reason) { onError(errorMessage(reason)); }
  }
  if (!tasks.length) return null;
  const activeTasks = tasks.filter((task) => task.state === "running" || task.state === "paused");
  const endedTasks = tasks.filter((task) => task.state !== "running" && task.state !== "paused");

  const taskTable = (items: RepeatTask[]) => <div className="table-wrap responsive-table"><table><thead><tr><th>{t("requests:repeat.task")}</th><th>{t("requests:columns.status")}</th><th>{t("requests:repeat.results")}</th><th>{t("requests:repeat.nextRun")}</th><th aria-label={t("requests:columns.actions")} /></tr></thead>
    <tbody>{items.map((task) => <tr key={task.id}>
      <td data-label={t("requests:repeat.task")}><strong>{task.method} {task.path}</strong><span className="subtle">{task.id} · {Math.round(task.intervalMilliseconds / 1000)}s {task.durationMilliseconds === 0 && <InfinityIcon size={12} />}</span></td>
		<td data-label={t("requests:columns.status")}><span className={`status repeat-${task.state}`}>{t(`requests:repeat.states.${task.state}`)}</span>{task.inFlight && <span className="subtle">{t("requests:repeat.inFlight")}</span>}{task.circuitOpen && <span className="subtle danger-text">{t("requests:repeat.circuitOpen", { count: task.consecutiveFailures })}</span>}{task.stopReason && !task.circuitOpen && <span className="subtle">{t(`requests:repeat.stopReasons.${task.stopReason}`, { defaultValue: task.stopReason })}</span>}</td>
		<td data-label={t("requests:repeat.results")}><div className="repeat-results">
			<span><small>{t("requests:repeat.total")}</small><strong>{task.executions}{task.maxExecutions ? ` / ${task.maxExecutions}` : ""}</strong></span>
	        <span className="success-text"><small>{t("requests:repeat.success")}</small><strong>{task.successes}</strong></span>
	        <span className="danger-text"><small>{t("requests:repeat.failure")}</small><strong>{task.failures}</strong></span>
			{task.maxTokens ? <span className={task.tokenUsageMissing ? "danger-text" : ""}><small>{t("requests:repeat.tokens")}</small><strong>{task.tokensUsed || 0} / {task.maxTokens}</strong></span> : <span><small>{t("requests:repeat.tokens")}</small><strong>{t("requests:repeat.unlimited")}</strong></span>}
	      </div></td>
      <td data-label={t("requests:repeat.nextRun")}>{formatOptionalTime(task.nextRunAt, i18n.resolvedLanguage)}</td>
      <td data-label={t("requests:columns.actions")}><div className="row-actions">
        {canOperate && task.state === "running" && <button className="icon-button" aria-label={t("requests:repeat.pause")} data-tooltip={t("requests:repeat.pause")} onClick={() => act(() => api.repeatTaskAction(task.id, "pause"))}><Pause size={16} /></button>}
        {canOperate && task.state === "paused" && <button className="icon-button" aria-label={t("requests:repeat.resume")} data-tooltip={t("requests:repeat.resume")} onClick={() => act(() => api.repeatTaskAction(task.id, "resume"))}><Play size={16} /></button>}
        {canOperate && (task.state === "running" || task.state === "paused") && <button className="icon-button" aria-label={t("requests:repeat.runNow")} data-tooltip={t("requests:repeat.runNow")} onClick={() => act(() => api.repeatTaskAction(task.id, "run"))}><RotateCcw size={16} /></button>}
        {canOperate && (task.state === "running" || task.state === "paused") && <button className="icon-button danger" aria-label={t("requests:repeat.stop")} data-tooltip={t("requests:repeat.stop")} onClick={() => act(() => api.stopRepeatTask(task.id))}><Square size={15} /></button>}
      </div></td>
    </tr>)}</tbody></table></div>;

  return <section className="content-section repeat-tasks-section">
    <div className="section-heading"><div><h2>{t("requests:repeat.tasksTitle")}</h2><p>{t("requests:repeat.tasksDescription")}</p></div><span>{t("requests:repeat.activeCount", { count: activeTasks.length })}</span></div>
    {activeTasks.length > 0 ? taskTable(activeTasks) : <div className="empty-state request-empty"><Activity size={20} /><span>{t("requests:repeat.noActive")}</span></div>}
    {endedTasks.length > 0 && <details className="repeat-history"><summary>{t("requests:repeat.endedTasks", { count: endedTasks.length })}</summary>{taskTable(endedTasks)}</details>}
    <div className="repeat-task-footnote"><Activity size={14} />{t("requests:repeat.memoryOnly")}</div>
  </section>;
}
