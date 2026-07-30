import { ServerCog } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import type { Config, RuntimeInfo } from "../types";

function upstreamHost(baseUrl: string) {
  try {
    return new URL(baseUrl).host;
  } catch {
    return baseUrl || "—";
  }
}

export function EnvironmentIdentity({ config, runtimeInfo, compact = false }: {
  config: Config;
  runtimeInfo: RuntimeInfo | null;
  compact?: boolean;
}) {
  const { t } = useTranslation("common");
  const target = useMemo(() => upstreamHost(config.upstream.baseUrl), [config.upstream.baseUrl]);
  const environment = runtimeInfo?.platform || t("environment.local");

  return <div className={`environment-identity${compact ? " compact" : ""}`} title={`${environment} · ${target}`}>
    <span className="environment-icon"><ServerCog size={16} /></span>
    <span className="environment-copy">
      <small>{t("environment.label")}</small>
      <strong>{environment}</strong>
      <span><i />{target}</span>
    </span>
  </div>;
}
