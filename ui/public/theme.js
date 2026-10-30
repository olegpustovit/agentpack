// Applies the saved light/dark choice before first paint. Loaded synchronously from index.html (CSP script-src 'self').
try {
  const saved = localStorage.getItem("agentpack-theme")
  if (saved === "light" || saved === "dark") document.documentElement.dataset.theme = saved
} catch {}
