const KEY = "agentpack-terminal-screen-reader"

export function screenReaderEnabled(): boolean {
  try {
    return localStorage.getItem(KEY) === "1"
  } catch {
    return false
  }
}

export function setScreenReaderEnabled(enabled: boolean) {
  try {
    if (enabled) localStorage.setItem(KEY, "1")
    else localStorage.removeItem(KEY)
  } catch {
    // Private windows may refuse storage; the setting then lasts for this page load at most.
  }
}
