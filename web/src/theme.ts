export type Theme = "light" | "dark";
/** "auto" follows the clock: light from 07:00 to 18:59, dark otherwise. */
export type ThemeMode = Theme | "auto";
const preferenceKey = "siagent.theme";

/** Day period used by the automatic mode, in the administrator's local time. */
const dayStartHour = 7;
const nightStartHour = 19;

export function storedThemeMode(): ThemeMode {
  try {
    const value = localStorage.getItem(preferenceKey);
    if (value === "light" || value === "dark" || value === "auto")
      return value;
  } catch {
    // Storage can be unavailable; fall through to the light default.
  }
  return "light";
}

// A clock-based day/night switch, not a solar or geolocation calculation: the
// installer has no location data, so a fixed local-time window is predictable.
export function themeForMode(mode: ThemeMode, now = new Date()): Theme {
  if (mode !== "auto") return mode;
  const hour = now.getHours();
  return hour >= dayStartHour && hour < nightStartHour ? "light" : "dark";
}

export function applyTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
  document.documentElement.style.colorScheme = theme;
}

export function rememberTheme(mode: ThemeMode) {
  try {
    localStorage.setItem(preferenceKey, mode);
  } catch {
    // The visible theme still changes for this session.
  }
}

// themeModeLabel describes the current choice for the toggle's accessible name.
export function themeModeLabel(mode: ThemeMode): string {
  if (mode === "auto") return "外观：自动（跟随时间）";
  return mode === "dark" ? "外观：深色" : "外观：浅色";
}

export function nextThemeMode(mode: ThemeMode): ThemeMode {
  if (mode === "light") return "dark";
  if (mode === "dark") return "auto";
  return "light";
}
