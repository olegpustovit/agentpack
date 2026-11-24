export function isFolderPath(query: string): boolean {
  return /^(~|\/|\\\\|[A-Za-z]:[\\/])/.test(query)
}

export function splitFolderPath(query: string) {
  const cut = Math.max(query.lastIndexOf("/"), query.lastIndexOf("\\"))
  return cut < 0 ? { dir: "~/", segment: query.slice(1) } : { dir: query.slice(0, cut + 1), segment: query.slice(cut + 1) }
}

export function parentFolderInput(query: string): string | undefined {
  if (!/[\\/]$/.test(query) || /^(~|[A-Za-z]:)?[\\/]$/.test(query)) return undefined
  return splitFolderPath(query.slice(0, -1)).dir
}

export function matchingFolders<T extends { name: string; hidden: boolean }>(entries: T[], segment: string, showHidden = false): T[] {
  const needle = segment.toLowerCase()
  return entries.filter((entry) => (showHidden || !entry.hidden || needle.startsWith(".")) && entry.name.toLowerCase().startsWith(needle))
}
