import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import authEn from "../locales/en-US/auth.json";
import commonEn from "../locales/en-US/common.json";
import capturesEn from "../locales/en-US/captures.json";
import diagnosticsEn from "../locales/en-US/diagnostics.json";
import errorsEn from "../locales/en-US/errors.json";
import overviewEn from "../locales/en-US/overview.json";
import logsEn from "../locales/en-US/logs.json";
import requestsEn from "../locales/en-US/requests.json";
import settingsEn from "../locales/en-US/settings.json";
import incidentsEn from "../locales/en-US/incidents.json";
import authZh from "../locales/zh-CN/auth.json";
import commonZh from "../locales/zh-CN/common.json";
import capturesZh from "../locales/zh-CN/captures.json";
import diagnosticsZh from "../locales/zh-CN/diagnostics.json";
import errorsZh from "../locales/zh-CN/errors.json";
import overviewZh from "../locales/zh-CN/overview.json";
import logsZh from "../locales/zh-CN/logs.json";
import requestsZh from "../locales/zh-CN/requests.json";
import settingsZh from "../locales/zh-CN/settings.json";
import incidentsZh from "../locales/zh-CN/incidents.json";

export const supportedLocales = ["zh-CN", "en-US"] as const;
export type Locale = typeof supportedLocales[number];
export const localeStorageKey = "relay-lifeline-locale";

export function normalizeLocale(value?: string | null): Locale {
  return value?.toLowerCase().startsWith("zh") ? "zh-CN" : "en-US";
}

const initialLocale = normalizeLocale(localStorage.getItem(localeStorageKey) || navigator.language);

void i18n.use(initReactI18next).init({
  resources: {
    "zh-CN": { common: commonZh, auth: authZh, overview: overviewZh, requests: requestsZh, settings: settingsZh, diagnostics: diagnosticsZh, errors: errorsZh, logs: logsZh, captures: capturesZh, incidents: incidentsZh },
    "en-US": { common: commonEn, auth: authEn, overview: overviewEn, requests: requestsEn, settings: settingsEn, diagnostics: diagnosticsEn, errors: errorsEn, logs: logsEn, captures: capturesEn, incidents: incidentsEn },
  },
  lng: initialLocale,
  fallbackLng: "en-US",
  supportedLngs: supportedLocales,
  ns: ["common", "auth", "overview", "requests", "settings", "diagnostics", "errors", "logs", "captures", "incidents"],
  defaultNS: "common",
  interpolation: { escapeValue: false },
  returnNull: false,
});

function applyLocale(locale: string) {
  const normalized = normalizeLocale(locale);
  document.documentElement.lang = normalized;
  localStorage.setItem(localeStorageKey, normalized);
}

applyLocale(initialLocale);
i18n.on("languageChanged", applyLocale);

export default i18n;
