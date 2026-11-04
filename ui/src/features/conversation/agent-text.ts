// OSC / ANSI and HTML tag stripping when markdown rendering is off the bundle budget.
const osc = /\u001b\][^\u0007]*(?:\u0007|\u001b\\)/g
const ansi = /\u001b\[[0-9;?]*[ -/]*[@-~]/g
const htmlTag = /<[^>]*>/g

export function stripAgentText(text: string): string {
  return text.replace(osc, "").replace(ansi, "").replace(htmlTag, "").trimEnd()
}
