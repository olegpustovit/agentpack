import { appendFileSync } from "node:fs";
const log = value => appendFileSync(process.env.OMP_SMOKE_LOG, JSON.stringify(value) + "\n");
const mode = process.argv[2];
if (mode === "mcp") {
  for await (const line of console) {
    const request = JSON.parse(line);
    if (request.id === undefined) continue;
    log({mode, method: request.method, cwd: process.cwd()});
    let result = {};
    if (request.method === "initialize") result = {protocolVersion: "2024-11-05", capabilities: {tools: {}}, serverInfo: {name: "fixture", version: "1"}};
    if (request.method === "tools/list") result = {tools: [{name: "ping", description: "Fixture ping", inputSchema: {type: "object", properties: {}}}]};
    if (request.method === "tools/call") result = {content: [{type: "text", text: "OMP_MCP_OK"}]};
    console.log(JSON.stringify({jsonrpc: "2.0", id: request.id, result}));
  }
} else if (mode === "lsp") {
  let buffer = Buffer.alloc(0);
  const send = value => {
    const text = JSON.stringify(value);
    process.stdout.write(`Content-Length: ${Buffer.byteLength(text)}\r\n\r\n${text}`);
  };
  for await (const chunk of Bun.stdin.stream()) {
    buffer = Buffer.concat([buffer, Buffer.from(chunk)]);
    while (true) {
      const end = buffer.indexOf("\r\n\r\n");
      if (end < 0) break;
      const length = Number(/Content-Length: (\d+)/i.exec(buffer.subarray(0, end).toString())[1]);
      if (buffer.length < end + 4 + length) break;
      const request = JSON.parse(buffer.subarray(end + 4, end + 4 + length));
      buffer = buffer.subarray(end + 4 + length);
      log({mode, method: request.method});
      if (request.id !== undefined) {
        const result = request.method === "initialize"
          ? {capabilities: {textDocumentSync: 1, diagnosticProvider: {interFileDependencies: false, workspaceDiagnostics: false}}}
          : request.method === "textDocument/diagnostic" ? {kind: "full", items: []} : null;
        send({jsonrpc: "2.0", id: request.id, result});
      }
      if (request.method === "textDocument/didOpen") send({jsonrpc: "2.0", method: "textDocument/publishDiagnostics", params: {uri: request.params.textDocument.uri, diagnostics: []}});
      if (request.method === "exit") process.exit(0);
    }
  }
} else {
  const input = JSON.parse(await Bun.stdin.text());
  log({mode, event: input.hook_event_name, tool: input.tool_name, input: input.tool_input, output: input.tool_response, cwd: process.cwd()});
  let output = {};
  if (mode === "start") output = {hookSpecificOutput: {additionalContext: "OMP_START_CONTEXT"}};
  if (mode === "input") output = input.prompt.includes("OMP_DENY_INPUT") ? {decision: "deny", message: "OMP_INPUT_DENIED"} : {additional_context: "OMP_INPUT_CONTEXT"};
  if (mode === "pre" && input.tool_input.command.includes("timeout")) await new Promise(resolve => setTimeout(resolve, 2500));
  if (mode === "pre") output = input.tool_input.command.includes("forbidden")
    ? {hookSpecificOutput: {permissionDecision: "deny", permissionDecisionReason: "OMP_BLOCKED"}}
    : {hookSpecificOutput: {updatedInput: {command: "printf OMP_REVISED"}}};
  if (mode === "post") output = {updated_tool_output: "OMP_REDACTED"};
  console.log(JSON.stringify(output));
}
