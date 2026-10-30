import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { type MachineState, StatusDot } from "./status-dot";

const words: [MachineState, string][] = [
  ["running", "Running"],
  ["stopped", "Stopped"],
  ["restarting", "Restarting"],
  ["offline", "Offline"],
  ["readonly", "Read-only"],
  ["needsUpdate", "Needs update"],
];

describe("StatusDot", () => {
  it.each(words)("shows the vocabulary word for %s", (state, word) => {
    const html = renderToStaticMarkup(<StatusDot state={state} />);
    expect(html).toContain(`data-state="${state}"`);
    expect(html).toContain(`>${word}<`);
  });
});
