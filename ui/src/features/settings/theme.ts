export const themes = ["system", "light", "dark"] as const
export type Theme = (typeof themes)[number]

// public/theme.js reads this key before first paint; keep the two in step.
const storageKey = "agentpack-theme"

export function currentTheme(): Theme {
  return (document.documentElement.dataset.theme as Theme | undefined) ?? "system"
}

export function applyTheme(theme: Theme) {
  const root = document.documentElement
  if (theme === "system") delete root.dataset.theme
  else root.dataset.theme = theme
  try {
    if (theme === "system") localStorage.removeItem(storageKey)
    else localStorage.setItem(storageKey, theme)
  } catch {
    // Storage can be unavailable; the choice then lasts for this page only.
  }
}
