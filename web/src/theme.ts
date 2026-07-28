import { useEffect, useState } from "react";

export type ThemeMode = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

export const themeStorageKey = "relay-lifeline-theme";

function storedTheme(): ThemeMode {
  const value = localStorage.getItem(themeStorageKey);
  return value === "light" || value === "dark" ? value : "system";
}

function systemTheme(): ResolvedTheme {
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function resolveTheme(mode: ThemeMode): ResolvedTheme {
  return mode === "system" ? systemTheme() : mode;
}

function applyTheme(mode: ThemeMode) {
  const resolved = resolveTheme(mode);
  document.documentElement.dataset.themeMode = mode;
  document.documentElement.dataset.theme = resolved;
  document.documentElement.style.colorScheme = resolved;
  document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')?.setAttribute(
    "content",
    resolved === "dark" ? "#0d100e" : "#eef1ed",
  );
  return resolved;
}

export function useTheme() {
  const [mode, setModeState] = useState<ThemeMode>(storedTheme);
  const [resolved, setResolved] = useState<ResolvedTheme>(() => resolveTheme(storedTheme()));

  useEffect(() => {
    setResolved(applyTheme(mode));
    if (mode === "system") localStorage.removeItem(themeStorageKey);
    else localStorage.setItem(themeStorageKey, mode);
  }, [mode]);

  useEffect(() => {
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!media || mode !== "system") return;
    const changed = () => setResolved(applyTheme("system"));
    media.addEventListener("change", changed);
    return () => media.removeEventListener("change", changed);
  }, [mode]);

  return { mode, resolved, setMode: setModeState };
}
