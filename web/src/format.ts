import i18n, { normalizeLocale } from "./i18n";

export function formatAge(value: string) {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 1000));
	return i18n.t("common:time.ago", { value: formatSeconds(seconds) });
}

export function formatSeconds(seconds: number) {
  if (seconds < 60) return i18n.t("common:time.seconds", { count: seconds });
  const minutes = Math.floor(seconds / 60);
  return minutes < 60
    ? i18n.t("common:time.minutes", { count: minutes })
    : i18n.t("common:time.hoursMinutes", { hours: Math.floor(minutes / 60), minutes: minutes % 60 });
}

export function formatDuration(start: string, end?: string) {
  const seconds = Math.max(0, Math.floor((new Date(end || Date.now()).getTime() - new Date(start).getTime()) / 1000));
  return formatSeconds(seconds);
}

export function formatTime(value: string) {
  return new Date(value).toLocaleTimeString(normalizeLocale(i18n.resolvedLanguage), { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  return `${(value / 1024).toFixed(1)} KiB`;
}
