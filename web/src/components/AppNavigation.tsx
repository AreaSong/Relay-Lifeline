import {
  Activity, Archive, Clock3, FileLock2, HeartPulse, LogOut, PanelLeftClose,
  PanelLeftOpen, ScrollText, Settings2, ShieldAlert, Stethoscope, UserRound,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { Config, RuntimeInfo, SessionInfo } from "../types";
import type { ThemeMode } from "../theme";
import { EnvironmentIdentity } from "./EnvironmentIdentity";
import { LanguageSelector } from "./LanguageSelector";
import { ThemeSelector } from "./ThemeSelector";

export type View = "overview" | "requests" | "history" | "incidents" | "logs" | "captures" | "diagnostics" | "settings";

export const allViews: View[] = ["overview", "requests", "history", "incidents", "logs", "captures", "diagnostics", "settings"];
export const mobileViews: View[] = ["overview", "requests", "history", "diagnostics", "settings"];

export const navigation: Array<{ group: "monitoring" | "forensics" | "maintenance"; items: Array<{ view: View; icon: LucideIcon }> }> = [
  { group: "monitoring", items: [
    { view: "overview", icon: Activity }, { view: "requests", icon: Clock3 }, { view: "incidents", icon: ShieldAlert },
  ] },
  { group: "forensics", items: [
    { view: "history", icon: Archive }, { view: "logs", icon: ScrollText }, { view: "captures", icon: FileLock2 },
  ] },
  { group: "maintenance", items: [
    { view: "diagnostics", icon: Stethoscope }, { view: "settings", icon: Settings2 },
  ] },
];

export function iconForView(view: View) {
  return navigation.flatMap((group) => group.items).find((item) => item.view === view)!.icon;
}

export function AppNavigation({ view, collapsed, session, config, runtimeInfo, themeMode, onThemeChange, onSelect, onCollapse, onLogout }: {
  view: View;
  collapsed: boolean;
  session: SessionInfo;
  config: Config;
  runtimeInfo: RuntimeInfo | null;
  themeMode: ThemeMode;
  onThemeChange: (mode: ThemeMode) => void;
  onSelect: (view: View) => void;
  onCollapse: () => void;
  onLogout: () => void;
}) {
  const { t } = useTranslation("common");
  const canOperate = session.capabilities.includes("operate");

  return <div className="app-rail-container">
    <aside className="app-rail" aria-label={t("brandSubtitle")}>
      <button className="rail-identity" aria-label="Relay-Lifeline" onClick={() => onSelect("overview")}>
        <span className="rail-brand"><HeartPulse size={20} /></span>
        <span className="rail-copy"><strong>Relay-Lifeline</strong><small>{t("brandSubtitle")}</small></span>
      </button>

      <nav>{navigation.map((section) => {
        const items = section.items.filter((item) => canOperate || item.view !== "settings");
        if (!items.length) return null;
        return <section className="rail-group" key={section.group} aria-labelledby={`rail-${section.group}`}>
          <h2 id={`rail-${section.group}`}>{t(`navGroups.${section.group}`)}</h2>
          {items.map(({ view: itemView, icon: Icon }) => <button
            key={itemView}
            aria-label={t(`nav.${itemView}`)}
            title={t(`nav.${itemView}`)}
            aria-current={view === itemView ? "page" : undefined}
            data-tooltip={collapsed ? t(`nav.${itemView}`) : undefined}
            className={view === itemView ? "active" : ""}
            onClick={() => onSelect(itemView)}
          ><Icon size={18} /><span className="rail-label">{t(`nav.${itemView}`)}</span></button>)}
        </section>;
      })}</nav>

      <div className="rail-footer">
        <EnvironmentIdentity config={config} runtimeInfo={runtimeInfo} compact={collapsed} />
        <div className="rail-user">
          <span><UserRound size={16} /></span>
          <div className="rail-label"><strong>{t(`roles.${session.role}`)}</strong><small>{t("account.session")}</small></div>
        </div>
        <div className="rail-utilities">
          <ThemeSelector mode={themeMode} onChange={onThemeChange} compact />
          <LanguageSelector compact />
          <button className="rail-action" aria-label={t("actions.logout")} data-tooltip={t("actions.logout")} onClick={onLogout}><LogOut size={17} /></button>
        </div>
        <button className="rail-collapse" aria-label={collapsed ? t("actions.expandNav") : t("actions.collapseNav")} onClick={onCollapse}>
          {collapsed ? <PanelLeftOpen size={17} /> : <PanelLeftClose size={17} />}
          <span className="rail-label">{t("actions.collapseNav")}</span>
        </button>
      </div>
    </aside>
  </div>;
}
