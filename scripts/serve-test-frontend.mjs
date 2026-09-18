import { createReadStream, existsSync, statSync } from "node:fs";
import { createServer, request as proxyRequest } from "node:http";
import { basename, extname, join, normalize, resolve } from "node:path";

const host = process.env.CONTENT_AGENT_FRONTEND_HOST || "0.0.0.0";
const port = Number.parseInt(process.env.CONTENT_AGENT_FRONTEND_PORT || "8860", 10);
const backend = new URL(process.env.CONTENT_AGENT_BACKEND_URL || "http://127.0.0.1:8850");
const webRoot = resolve(process.env.CONTENT_AGENT_FRONTEND_DIST || "frontend/dist");

const contentTypes = new Map([
  [".css", "text/css; charset=utf-8"],
  [".html", "text/html; charset=utf-8"],
  [".ico", "image/x-icon"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".map", "application/json; charset=utf-8"],
  [".png", "image/png"],
  [".svg", "image/svg+xml"],
  [".webp", "image/webp"],
]);

function proxy(req, res) {
  const upstream = proxyRequest(
    {
      protocol: backend.protocol,
      hostname: backend.hostname,
      port: backend.port,
      method: req.method,
      path: req.url,
      headers: { ...req.headers, host: backend.host },
    },
    (upstreamResponse) => {
      res.writeHead(upstreamResponse.statusCode || 502, upstreamResponse.headers);
      upstreamResponse.pipe(res);
    },
  );
  upstream.on("error", () => {
    if (!res.headersSent) res.writeHead(502, { "content-type": "application/json" });
    res.end(JSON.stringify({ status: "error", message: "backend unavailable" }));
  });
  req.pipe(upstream);
}

function staticFile(req, res) {
  let pathname;
  try {
    pathname = decodeURIComponent(new URL(req.url || "/", "http://localhost").pathname);
  } catch {
    res.writeHead(400).end();
    return;
  }

  const relativePath = normalize(pathname).replace(/^[/\\]+/, "");
  let filePath = resolve(join(webRoot, relativePath));
  if (!filePath.startsWith(`${webRoot}/`) && filePath !== webRoot) {
    res.writeHead(403).end();
    return;
  }
  if ((!existsSync(filePath) || !statSync(filePath).isFile()) && relativePath.startsWith("assets/")) {
    const flattenedAssetPath = resolve(join(webRoot, basename(relativePath)));
    if (flattenedAssetPath.startsWith(`${webRoot}/`) && existsSync(flattenedAssetPath) && statSync(flattenedAssetPath).isFile()) {
      filePath = flattenedAssetPath;
    }
  }
  if (!existsSync(filePath) || !statSync(filePath).isFile()) {
    filePath = join(webRoot, "index.html");
  }
  const headers = {
    "cache-control": extname(filePath) === ".html" ? "no-store" : "public, max-age=31536000, immutable",
    "content-type": contentTypes.get(extname(filePath)) || "application/octet-stream",
  };
  res.writeHead(200, headers);
  if (req.method === "HEAD") {
    res.end();
    return;
  }
  createReadStream(filePath).pipe(res);
}

createServer((req, res) => {
  if ((req.url || "").startsWith("/api/") || req.url === "/healthz") {
    proxy(req, res);
    return;
  }
  staticFile(req, res);
}).listen(port, host, () => {
  process.stdout.write(`content-agent frontend listening on http://${host}:${port}\n`);
});
