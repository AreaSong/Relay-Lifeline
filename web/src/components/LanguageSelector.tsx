import { Languages } from "lucide-react";
import { useTranslation } from "react-i18next";
import { normalizeLocale } from "../i18n";

export function LanguageSelector({ compact = false }: { compact?: boolean }) {
  const { t, i18n } = useTranslation("common");
  const locale = normalizeLocale(i18n.resolvedLanguage);
  return <label className={`language-selector ${compact ? "compact" : ""}`}>
    <Languages size={16} aria-hidden="true" />
    <span className="sr-only">{t("language.label")}</span>
    <select aria-label={t("language.label")} value={locale} onChange={(event) => void i18n.changeLanguage(event.target.value)}>
      <option value="zh-CN">{t("language.zhCN")}</option>
      <option value="en-US">{t("language.enUS")}</option>
    </select>
  </label>;
}
