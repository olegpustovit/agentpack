import { describe, expect, it } from "vitest"
import { listDiffChanges } from "./diff-summary"
import type { ThreadItem } from "./model"

describe("listDiffChanges", () => {
  it("counts lines from tool diff blocks", () => {
    const items: ThreadItem[] = [
      {
        kind: "tool",
        data: {
          content: [
            {
              type: "diff",
              path: "internal/network/terminal_client.go",
              oldText: "\tconn, err := dial(ctx, record, 0)\n",
              newText: "\tsince := record.lastOffset.Load()\n\tconn, err := dial(ctx, record, since)\n",
            },
          ],
        },
      },
    ]
    expect(listDiffChanges(items)).toEqual([
      { path: "internal/network/terminal_client.go", add: 2, del: 1 },
    ])
  })
})
