// OSC / ANSI and HTML tag stripping when markdown rendering is off the bundle budget.
// oxlint-disable-next-line no-control-regex -- stripping control sequences is the point
const osc = /\u001b\][^\u0007]*(?:\u0007|\u001b\\)/g
// oxlint-disable-next-line no-control-regex
const ansi = /\u001b\[[0-9;?]*[ -/]*[@-~]/g
const htmlTag = /<[^>]*>/g

export function stripAgentText(text: string): string {
  return text.replace(osc, "").replace(ansi, "").replace(htmlTag, "").trimEnd()
}
