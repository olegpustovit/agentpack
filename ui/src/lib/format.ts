const harnessNames: Record<string, string> = {
  agent: "Cursor CLI",
  cursor: "Cursor CLI",
  codex: "Codex",
  claude: "Claude",
  agy: "Antigravity",
  grok: "Grok",
  omp: "Oh My Pi",
  opencode: "OpenCode",
};

const relative = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 86_400],
  ["month", 30 * 86_400],
  ["week", 7 * 86_400],
  ["day", 86_400],
  ["hour", 3_600],
  ["minute", 60],
  ["second", 1],
];

// A content hash keeps its algorithm prefix, so the 7 characters are digest, not "sha256-".
export function shortHash(value: string): string {
  const prefix = value.lastIndexOf(":") + 1;
  return value.slice(0, prefix + 7);
}

export function basename(path: string): string {
  return path.replace(/[\\/]+$/, "").split(/[\\/]/).pop() ?? "";
}

export function relativeTime(date: Date, now: number = Date.now()): string {
  const seconds = Math.round((date.getTime() - now) / 1000);
  const fallback: [Intl.RelativeTimeFormatUnit, number] = ["second", 1];
  const [unit, size] = units.find(([, s]) => Math.abs(seconds) >= s) ?? fallback;
  return relative.format(Math.round(seconds / size), unit);
}

// Compact age for row trailers: "12 m", "2 h", "3 d".
export function shortAge(date: Date, now: number = Date.now()): string {
  const minutes = Math.max(0, Math.round((now - date.getTime()) / 60_000));
  if (minutes < 60) return `${minutes} m`;
  const hours = Math.round(minutes / 60);
  return hours < 48 ? `${hours} h` : `${Math.round(hours / 24)} d`;
}

export function plural(count: number, word: string): string {
  return `${count} ${count === 1 ? word : `${word}s`}`;
}

export function harnessName(id: string): string {
  return harnessNames[id] ?? id;
}
