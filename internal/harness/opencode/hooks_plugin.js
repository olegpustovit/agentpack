import { readFileSync } from "node:fs";

const config = JSON.parse(readFileSync(new URL("./config.json", import.meta.url), "utf8"));

function normalizeToolName(name) {
  return String(name ?? "").toLowerCase();
}

function matchesMatcher(matcher, toolName) {
  if (!matcher || matcher === "*") return true;
  const actual = normalizeToolName(toolName);
  return String(matcher)
    .split("|")
    .map((value) => value.trim().toLowerCase())
    .filter(Boolean)
    .some((candidate) => candidate === actual || candidate === "bash" && actual === "shell");
}

async function runHook(kind, specPath, payload) {
  // Hand the payload over as bytes: with stdin "pipe" Bun exposes a FileSink, not a
  // WritableStream, and its shape has changed between OpenCode releases.
  const proc = Bun.spawn(
    [config.agentpack || "agentpack", "hook-exec", kind, "--target", "opencode", "--spec", specPath],
    {
      stdin: new TextEncoder().encode(JSON.stringify(payload)),
      stdout: "pipe",
      stderr: "pipe",
    },
  );
  const [stdout, stderr, status] = await Promise.all([
    new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited,
  ]);
  if (status !== 0) {
    throw new Error(stderr || stdout || `hook-exec exited ${status}`);
  }
  return stdout.trim() ? JSON.parse(stdout) : { decision: "allow" };
}

function entriesFor(event, toolName) {
  return (config.hooks || []).filter((entry) => entry.event === event && matchesMatcher(entry.matcher, toolName));
}

export default {
  id: "agentpack-hooks",
  server: async () => ({
    config: async (settings) => {
      if (config.skillsPath) {
        settings.skills ||= {};
        settings.skills.paths ||= [];
        if (!settings.skills.paths.includes(config.skillsPath)) settings.skills.paths.push(config.skillsPath);
      }
      for (const [name, agent] of Object.entries(config.agents || {})) {
        settings.agent ||= {};
        settings.agent[name] ??= agent;
      }
      for (const [name, command] of Object.entries(config.commands || {})) {
        settings.command ||= {};
        settings.command[name] ??= command;
      }
    },
    "tool.execute.before": async (input, output) => {
      for (const entry of entriesFor("tool.execute.before", input.tool)) {
        const result = await runHook(entry.kind, entry.specPath, { input, output });
        if (result.updated_input !== undefined) {
          output.args = result.updated_input;
        }
        if (result.decision === "deny" || result.decision === "ask") {
          throw new Error(result.message || "OpenCode hook blocked tool execution");
        }
      }
    },
    "tool.execute.after": async (input, output) => {
      for (const entry of entriesFor("tool.execute.after", input.tool)) {
        const result = await runHook(entry.kind, entry.specPath, { input, output });
        if (result.updated_tool_output !== undefined) {
          output.output = typeof result.updated_tool_output === "string"
            ? result.updated_tool_output
            : JSON.stringify(result.updated_tool_output);
        }
        if (result.additional_context) {
          output.output = `${output.output}\n\n${result.additional_context}`;
        }
      }
    },
    "permission.ask": async (input, output) => {
      for (const entry of entriesFor("permission.ask", input.tool || input.command || input.kind)) {
        const result = await runHook(entry.kind, entry.specPath, { input, output });
        if (result.decision === "deny") output.status = "deny";
        else if (result.decision === "ask" && output.status !== "deny") output.status = "ask";
        else if (result.decision === "allow" && output.status !== "deny") output.status = "allow";
      }
    },
    "chat.message": async (input, output) => {
      for (const entry of entriesFor("chat.message", "message")) {
        const result = await runHook(entry.kind, entry.specPath, { input, output });
        if (result.additional_context) {
          output.parts.push({ type: "text", text: result.additional_context });
        }
      }
    },
    "experimental.session.compacting": async (input, output) => {
      for (const entry of entriesFor("experimental.session.compacting", "compact")) {
        const result = await runHook(entry.kind, entry.specPath, { input, output });
        if (result.additional_context) {
          output.context.push(result.additional_context);
        }
      }
    },
  }),
  setup: async (host) => {
    await host.tool.hook("execute.before", async (event) => {
      for (const entry of entriesFor("tool.execute.before", event.tool)) {
        const result = await runHook(entry.kind, entry.specPath, { input: event, output: { args: event.input } });
        if (result.updated_input !== undefined) event.input = result.updated_input;
        if (result.decision === "deny" || result.decision === "ask") {
          throw new Error(result.message || "OpenCode hook blocked tool execution");
        }
      }
    });
    await host.tool.hook("execute.after", async (event) => {
      if (event.status !== "completed") return;
      for (const entry of entriesFor("tool.execute.after", event.tool)) {
        const result = await runHook(entry.kind, entry.specPath, { input: event, output: event.result });
        let content = event.result.content;
        if (result.updated_tool_output !== undefined) {
          content = typeof result.updated_tool_output === "string"
            ? result.updated_tool_output : JSON.stringify(result.updated_tool_output);
        }
        if (result.additional_context) {
          content = Array.isArray(content)
            ? [...content, { type: "text", text: result.additional_context }]
            : `${content || ""}\n\n${result.additional_context}`;
        }
        event.result = { ...event.result, content };
      }
    });
    await host.permission.hook("evaluate", async (event) => {
      for (const entry of entriesFor("permission.ask", event.action)) {
        const result = await runHook(entry.kind, entry.specPath, { input: event, output: { status: event.effect } });
        if (result.decision === "deny") event.effect = "deny";
        else if (result.decision === "ask" && event.effect !== "deny") event.effect = "ask";
        else if (result.decision === "allow" && event.effect !== "deny") event.effect = "allow";
        if (result.message) event.message = result.message;
      }
    });
    await host.session.hook("prompt", async (event) => {
      for (const entry of entriesFor("chat.message", "message")) {
        const result = await runHook(entry.kind, entry.specPath, { input: event, output: event.prompt });
        if (result.additional_context) event.prompt.text += `\n\n${result.additional_context}`;
      }
    });
    await host.session.hook("compaction", async (event) => {
      for (const entry of entriesFor("experimental.session.compacting", "compact")) {
        const result = await runHook(entry.kind, entry.specPath, { input: event, output: { context: event.system } });
        if (result.additional_context) event.system.push({ type: "text", text: result.additional_context });
      }
    });
  },
};
