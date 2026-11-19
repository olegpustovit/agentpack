// Fails when React Compiler skips any component or hook: a skipped one re-renders without memoization.
import { readFileSync } from "node:fs"
import { glob } from "node:fs/promises"
// @ts-expect-error -- @babel/core ships no types and only transformSync is used
import { transformSync } from "@babel/core"

const failures: string[] = []
for await (const file of glob("src/**/*.{ts,tsx}")) {
  if (/\.test\.tsx?$/.test(file)) continue
  transformSync(readFileSync(file, "utf8"), {
    filename: file,
    babelrc: false,
    configFile: false,
    parserOpts: { plugins: ["typescript", "jsx"] },
    plugins: [["babel-plugin-react-compiler", {
      logger: {
        logEvent(_: string, event: { kind: string; fnLoc?: { start: { line: number } }; detail?: { reason?: string; loc?: { start: { line: number } } } }) {
          if (event.kind !== "CompileError" && event.kind !== "PipelineError") return
          failures.push(`${file}:${event.detail?.loc?.start.line ?? event.fnLoc?.start.line ?? "?"} ${event.detail?.reason ?? event.kind}`)
        },
      },
    }]],
  })
}
if (failures.length) {
  console.error(`React Compiler skipped ${failures.length} function(s):\n${failures.join("\n")}`)
  process.exit(1)
}
