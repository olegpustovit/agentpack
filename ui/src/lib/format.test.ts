import { describe, expect, it } from "vitest";
import { basename, harnessName, plural, relativeTime, shortHash } from "./format";

describe("format", () => {
  it("shortens hashes to seven characters", () => {
    expect(shortHash("0123456789abcdef")).toBe("0123456");
  });

  it("returns the last path segment for POSIX and Windows paths", () => {
    expect(basename("/home/me/projects/agentpack/")).toBe("agentpack");
    expect(basename("C:\\work\\repo")).toBe("repo");
  });

  it("formats relative times against a fixed clock", () => {
    const now = Date.UTC(2026, 0, 10, 12);
    expect(relativeTime(new Date(now - 3 * 3_600_000), now)).toBe("3 hours ago");
    expect(relativeTime(new Date(now), now)).toBe("now");
    expect(relativeTime(new Date(now + 2 * 86_400_000), now)).toBe("in 2 days");
  });

  it("pluralizes by count", () => {
    expect(plural(1, "session")).toBe("1 session");
    expect(plural(0, "session")).toBe("0 sessions");
  });

  it("maps harness ids to display names and passes unknown ids through", () => {
    expect(harnessName("agy")).toBe("Antigravity");
    expect(harnessName("omp")).toBe("Oh My Pi");
    expect(harnessName("unknown")).toBe("unknown");
  });
});
