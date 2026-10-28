import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { resolve, sep } from "node:path";
const root = resolve("public");
createServer(async (req, res) => {
  try {
    const path = new URL(req.url, "http://localhost").pathname;
    const file = resolve(root, path === "/" ? "index.html" : path.replace(/^\/static\//, ""));
    if (!file.startsWith(root + sep)) {
      res.writeHead(403).end();
      return;
    }
    const type = file.endsWith(".js") ? "text/javascript" : file.endsWith(".css") ? "text/css" : file.endsWith(".webmanifest") ? "application/manifest+json" : file.endsWith(".png") ? "image/png" : file.endsWith(".svg") ? "image/svg+xml" : "text/html";
    res.writeHead(200, { "Content-Type": type });
    res.end(await readFile(file));
  } catch {
    res.writeHead(404).end();
  }
}).listen(18473, "127.0.0.1");
