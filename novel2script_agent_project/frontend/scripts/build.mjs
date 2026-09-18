import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const source = resolve(root, "src", "app.ts");
const target = resolve(root, "dist", "app.js");

const code = await readFile(source, "utf8");

await mkdir(dirname(target), { recursive: true });
await writeFile(target, code, "utf8");

console.log(`built ${target}`);
