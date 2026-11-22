import { expect, test } from "vitest"
import { isFolderPath, matchingFolders, parentFolderInput, splitFolderPath } from "./folder-path"

test("splits a browse path so only the directory goes to the gateway", () => {
  expect(splitFolderPath("~/pro")).toEqual({ dir: "~/", segment: "pro" })
  expect(splitFolderPath("~")).toEqual({ dir: "~/", segment: "" })
  expect(splitFolderPath("/")).toEqual({ dir: "/", segment: "" })
  expect(splitFolderPath("C:\\Us")).toEqual({ dir: "C:\\", segment: "Us" })
  expect(isFolderPath("project")).toBe(false)
  expect(isFolderPath("C:\\Us")).toBe(true)
})

test("matches the last segment by prefix and reveals hidden folders with a dot", () => {
  const entries = [{ name: "Projects", hidden: false }, { name: ".private", hidden: true }, { name: "my-project", hidden: false }]
  expect(matchingFolders(entries, "pro")).toEqual([entries[0]])
  expect(matchingFolders(entries, "")).toEqual([entries[0], entries[2]])
  expect(matchingFolders(entries, ".p")).toEqual([entries[1]])
})

test("Backspace on a trailing separator moves up one segment without crossing the root", () => {
  expect(parentFolderInput("~/projects/")).toBe("~/")
  expect(parentFolderInput("/work/project/")).toBe("/work/")
  expect(parentFolderInput("C:\\Users\\")).toBe("C:\\")
  for (const path of ["/", "~/", "C:\\", "~/projects"]) expect(parentFolderInput(path)).toBeUndefined()
})
