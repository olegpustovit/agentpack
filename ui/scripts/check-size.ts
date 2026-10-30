// Gzip budget gate, run after vite build. Budgets follow build-react-stack performance.md.
import { readFileSync, readdirSync } from "node:fs"
import { gzipSync } from "node:zlib"

const dist = new URL("../dist/", import.meta.url)
const gz = (f: string) => gzipSync(readFileSync(new URL(f, dist))).length
const html = readFileSync(new URL("index.html", dist), "utf8")
const initial = [...html.matchAll(/(?:src|href)="\/(assets\/[^"]+\.(?:js|css))"/g)].map((m) => m[1]!)
const sum = (ext: string) => initial.filter((f) => f.endsWith(ext)).reduce((n, f) => n + gz(f), 0)

const files = readdirSync(new URL("assets/", dist)).sort()
for (const f of files) console.log(`${gz(`assets/${f}`).toString().padStart(8)} B gzip  assets/${f}`)

const failures: string[] = []
if (sum(".js") > 170_000) failures.push(`initial JS ${sum(".js")} B > 170 KB`)
if (sum(".css") > 30_000) failures.push(`initial CSS ${sum(".css")} B > 30 KB`)
for (const f of files)
  if (f.endsWith(".js") && !initial.includes(`assets/${f}`) && gz(`assets/${f}`) > 100_000)
    failures.push(`chunk ${f} ${gz(`assets/${f}`)} B > 100 KB`)
if (failures.length) {
  console.error(failures.join("\n"))
  process.exit(1)
}
