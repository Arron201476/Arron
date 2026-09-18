import { chromium } from "playwright";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const projectRoot = path.resolve(__dirname, "..", "..");
const outputDir = path.join(projectRoot, "design", "audits", "product-design-script-editor");
const galleryPath = path.join(projectRoot, "design", "frontend-ui-state-gallery.html");

const targets = [
  {
    name: "01-gallery-novel-flow.png",
    url: `file:///${galleryPath.replaceAll("\\", "/")}#state-novel`,
    waitFor: "body",
  },
  {
    name: "02-gallery-script-edit.png",
    url: `file:///${galleryPath.replaceAll("\\", "/")}#state-edit`,
    waitFor: "body",
  },
  {
    name: "03-tiptap-spike.png",
    url: "http://127.0.0.1:8833/?spike=tiptap",
    waitFor: ".spike-shell",
  },
  {
    name: "04-lexical-spike.png",
    url: "http://127.0.0.1:8833/?spike=lexical",
    waitFor: ".spike-shell",
  },
];

await fs.mkdir(outputDir, { recursive: true });

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 960 }, deviceScaleFactor: 1 });
const results = [];

for (const target of targets) {
  await page.goto(target.url, { waitUntil: "networkidle" });
  await page.locator(target.waitFor).first().waitFor({ state: "visible", timeout: 10000 });
  await page.waitForTimeout(500);

  const filePath = path.join(outputDir, target.name);
  await page.screenshot({ path: filePath, fullPage: false });

  const size = (await fs.stat(filePath)).size;
  const viewport = await page.viewportSize();
  results.push({ file: filePath, url: target.url, bytes: size, viewport });
}

await browser.close();
console.log(JSON.stringify(results, null, 2));
