export type Theme = "light" | "dark";
const preferenceKey = "siagent.theme";

export function storedTheme(): Theme {
  try {
    const value = localStorage.getItem(preferenceKey);
    if (value === "light" || value === "dark") return value;
  } catch {
    // Storage can be unavailable; fall through to the light default.
  }
  return "light";
}

export function applyTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
  document.documentElement.style.colorScheme = theme;
}

export function rememberTheme(theme: Theme) {
  try {
    localStorage.setItem(preferenceKey, theme);
  } catch {
    // The visible theme still changes for this session.
  }
}
