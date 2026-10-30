import { readFileSync } from "node:fs";

const config = JSON.parse(readFileSync(new URL("./agentpack.json", import.meta.url), "utf8"));

function matches(matcher, name) {
  if (!matcher || matcher === "*") return true;
  const actual = String(name ?? "");
  let expression;
  try { expression = new RegExp(`^(?:${matcher})$`, "i"); }
  catch { return true; }
  return expression.test(actual) || actual.toLowerCase() === "shell" && expression.test("Bash");
}

async function run(entry, event, ctx, deadline) {
  const timeout = Math.min(entry.timeoutMs ?? 25000, deadline - Date.now());
  if (timeout <= 0) throw new Error("OMP hook execution deadline exceeded");
  const payload = {
    ...event,
    hook_event_name: entry.canonicalEvent,
    cwd: ctx.cwd,
    session_id: ctx.sessionManager?.getSessionId(),
    tool_name: event.toolName,
    tool_input: event.input,
    tool_response: event.content,
    prompt: event.text,
  };
  const proc = Bun.spawn(
    [config.agentpack, "hook-exec", entry.kind, "--target", "omp", "--spec", entry.specPath],
    { stdin: new TextEncoder().encode(JSON.stringify(payload)), stdout: "pipe", stderr: "pipe", timeout, killSignal: "SIGKILL" },
  );
  const [stdout, stderr, status] = await Promise.all([
    new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited,
  ]);
  if (status === 2) return { decision: "deny", message: stderr || stdout || "Hook blocked execution" };
  if (status !== 0) throw new Error(stderr || stdout || `hook-exec exited ${status}`);
  let result;
  try { result = stdout.trim() ? JSON.parse(stdout) : {}; }
  catch {
    if (["SessionStart", "UserPromptSubmit"].includes(entry.canonicalEvent)) return { additional_context: stdout.trim() };
    return {};
  }
  const specific = result.hookSpecificOutput || {};
  return {
    decision: specific.permissionDecision ?? result.permissionDecision ?? result.decision ?? (result.continue === false ? "deny" : "allow"),
    message: specific.permissionDecisionReason ?? result.permissionDecisionReason ?? result.reason ?? result.message,
    additional_context: specific.additionalContext ?? result.additional_context ?? result.additionalContext,
    updated_input: specific.updatedInput ?? result.updated_input ?? result.updatedInput,
    updated_tool_output: specific.updatedMCPToolOutput ?? result.updated_tool_output ?? result.updatedMCPToolOutput,
  };
}

export default function (pi) {
  let startupContext = "";
  for (const name of new Set(config.hooks.map(entry => entry.event))) {
    pi.on(name, async (event, ctx) => {
      const output = {};
      const deadline = Date.now() + 25000;
      const context = [];
      for (const entry of config.hooks.filter(entry => entry.event === name)) {
        if (!matches(entry.matcher, event.toolName ?? event.reason ?? event.source ?? event.type)) continue;
        if (name === "tool_result" && (entry.canonicalEvent === "PostToolUseFailure") !== !!event.isError) continue;
        // OMP reports extension exceptions and continues: turn execution failures into a block.
        let result;
        try { result = await run(entry, event, ctx, deadline); }
        catch (error) {
          if (name === "tool_call") return { block: true, reason: String(error) };
          if (name === "input") return { handled: true };
          throw error;
        }
        const blocked = result.decision === "deny" || result.decision === "block" || result.decision === "ask";
        if (result.additional_context) context.push(result.additional_context);
        if (name === "tool_call") {
          if (blocked) return { block: true, reason: result.message || "Agentpack hook blocked tool execution" };
          if (result.updated_input != null) {
            output.input = result.updated_input;
            event = { ...event, input: result.updated_input };
          }
        } else if (name === "tool_result") {
          if (result.updated_tool_output != null) {
            const value = result.updated_tool_output;
            output.content = Array.isArray(value) ? value : [{ type: "text", text: typeof value === "string" ? value : JSON.stringify(value) }];
            event = { ...event, content: output.content };
          }
        } else if (name === "input") {
          if (blocked) return { handled: true };
        } else if (name === "session_stop" && blocked) {
          output.continue = true;
          if (result.message) context.push(result.message);
        }
      }
      const additional = context.join("\n\n");
      if (name === "tool_call" || name === "tool_result" || name === "session_stop") {
        if (additional) output.additionalContext = additional;
      } else if (name === "input" && additional) {
        output.text = `${event.text}\n\n${additional}`;
      } else if (name === "session.compacting" && additional) {
        output.context = context;
      } else if (name === "session_start") {
        startupContext = additional;
      }
      return output;
    });
  }
  pi.on("before_agent_start", async (event, ctx) => {
    if (event.reason === "revive") return;
    const context = startupContext ? [startupContext] : [];
    const deadline = Date.now() + 25000;
    startupContext = "";
    // OMP's print/JSON hosts do not emit input events.
    if (ctx.mode === "print" || ctx.mode === "json") {
      for (const entry of config.hooks.filter(entry => entry.event === "input")) {
        if (!matches(entry.matcher, ctx.mode)) continue;
        let result;
        try { result = await run(entry, {text: event.prompt, source: ctx.mode}, ctx, deadline); }
        catch (error) { result = {decision: "deny", message: String(error)}; }
        if (["deny", "block", "ask"].includes(result.decision)) {
          ctx.abort();
          process.exitCode = 2;
          // OMP explicitly exits with its print result after cleanup.
          process.once("exit", () => { process.exitCode = 2; });
          return;
        }
        if (result.additional_context) context.push(result.additional_context);
      }
    }
    if (context.length) return { message: { customType: "agentpack-session-context", content: context.join("\n\n"), display: false } };
  });
}
