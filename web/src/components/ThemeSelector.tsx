import { Monitor, Moon, Sun } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { ThemeMode } from "../theme";

const modes: ThemeMode[] = ["system", "light", "dark"];

export function ThemeSelector({ mode, onChange, compact = false }: {
  mode: ThemeMode;
  onChange: (mode: ThemeMode) => void;
  compact?: boolean;
}) {
  const { t } = useTranslation("common");
  const icons = { system: Monitor, light: Sun, dark: Moon };
  if (compact) {
    const Icon = icons[mode];
    const next = modes[(modes.indexOf(mode) + 1) % modes.length];
    return <div className="theme-selector compact"><button type="button" className="active" aria-label={`${t("theme.label")}: ${t(`theme.${mode}`)}`} data-tooltip={t(`theme.${mode}`)} onClick={() => onChange(next)}><Icon size={16} /></button></div>;
  }
  return <div className={`theme-selector${compact ? " compact" : ""}`} role="group" aria-label={t("theme.label")}>
    {modes.map((value) => {
      const Icon = icons[value];
      return <button
        key={value}
        type="button"
        className={mode === value ? "active" : ""}
        aria-pressed={mode === value}
        aria-label={t(`theme.${value}`)}
        data-tooltip={compact ? t(`theme.${value}`) : undefined}
        onClick={() => onChange(value)}
      ><Icon size={16} />{!compact && <span>{t(`theme.${value}`)}</span>}</button>;
    })}
  </div>;
}
