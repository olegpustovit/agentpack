// Publishes ui/dist into internal/network/webdist, the tree the gateway embeds.
import { existsSync } from "node:fs"
import { cp, readFile, readdir, rename, rm, stat, writeFile } from "node:fs/promises"
import { gzipSync } from "node:zlib"

const dist = new URL("../dist/", import.meta.url)
const target = new URL("../../internal/network/webdist/", import.meta.url)
const staging = new URL("../../internal/network/webdist.tmp/", import.meta.url)

if (!existsSync(new URL("index.html", dist))) throw new Error("ui/dist/index.html missing; run bun run build first")

const html = await readFile(new URL("index.html", dist), "utf8")
for (const match of html.matchAll(/(?:src|href)="(\/assets\/[^"]+)"/g)) {
  const ref = match[1]
  if (ref === undefined || !existsSync(new URL(ref.slice(1), dist))) throw new Error(`index.html references missing ${ref}`)
}

for (const name of await readdir(new URL("assets/", dist))) {
  if (!/\.(js|css|svg)$/.test(name)) continue
  const body = await readFile(new URL(`assets/${name}`, dist))
  await writeFile(new URL(`assets/${name}.gz`, dist), gzipSync(body, { level: 9 }))
}

await rm(staging, { recursive: true, force: true })
await cp(dist, staging, { recursive: true })
// Tracked, so staging never dirties the checkout; the gateway serves it only when index.html is absent.
await cp(new URL("placeholder.html", target), new URL("placeholder.html", staging))

if (existsSync(target) && (await sameTree(staging, target))) {
  await rm(staging, { recursive: true, force: true })
  console.log("webdist unchanged")
} else {
  await rm(target, { recursive: true, force: true })
  await rename(staging, target)
  console.log("webdist updated")
}

async function sameTree(a: URL, b: URL): Promise<boolean> {
  const left = await files(a)
  const right = await files(b)
  if (left.length !== right.length) return false
  for (const [i, name] of left.entries()) {
    if (name !== right[i]) return false
    if (!(await readFile(new URL(name, a))).equals(await readFile(new URL(name, b)))) return false
  }
  return true
}

async function files(root: URL): Promise<string[]> {
  const out: string[] = []
  for (const name of await readdir(root, { recursive: true })) {
    if ((await stat(new URL(name, root))).isFile()) out.push(name)
  }
  return out.sort()
}
