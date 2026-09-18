import { chromium } from "playwright";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8860";
const projectID = process.argv[2];
if (!projectID) throw new Error("Usage: node scripts/artifact-workspace-smoke.mjs <project-id>");

const evidencePath = resolve("..", "acceptance", "evidence", "stage9-artifact-workspace.json");
const screenshotPath = resolve("..", "acceptance", "evidence", "stage9-artifact-workspace.png");
await mkdir(dirname(evidencePath), { recursive: true });

const browser = await chromium.launch({ channel: "msedge", headless: true });
const page = await browser.newPage({ viewport: { width: 1600, height: 1000 }, deviceScaleFactor: 1 });
const results = [];
try {
  await page.goto(`${baseURL}/projects/${projectID}`, { waitUntil: "networkidle" });
  await page.locator(".artifact-list button").first().waitFor();
  const count = await page.locator(".artifact-list button").count();
  for (let index = 0; index < count; index += 1) {
    const button = page.locator(".artifact-list button").nth(index);
    const label = (await button.locator("strong").innerText()).trim();
    const status = (await button.locator("small").innerText()).trim();
    await button.click();
    await page.locator(".script-paper").waitFor();
    await page.waitForTimeout(80);
    const readable = await page.locator(".artifact-document, .script-document").count();
    const rawFallback = await page.locator(".data-body").count();
    const openRaw = await page.locator(".artifact-raw[open]").count();
    const failure = await page.locator(".paper-placeholder").filter({ hasText: "正文读取失败" }).count();
    if (!readable || rawFallback || openRaw || failure) throw new Error(`${label}: readable=${readable} rawFallback=${rawFallback} openRaw=${openRaw} failure=${failure}`);
    if (!status) throw new Error(`${label}: missing artifact status`);
    results.push({ label, status, readable: true, raw_debug_collapsed: true });
  }
  if (results.at(0)?.label !== "来源材料" || results.at(-1)?.label !== "完整剧本") throw new Error(`Unexpected artifact flow order: ${results.map((item) => item.label).join(" -> ")}`);

  const editableButton = page.locator(".artifact-list button").filter({ hasText: /故事圣经|拆集方案|素材库|故事种子|系列蓝图|分集规划|视频还原剧本|剧本分析|改编 Brief|单集剧本/ }).first();
  if (await editableButton.count()) {
    await editableButton.click();
    const edit = page.getByRole("button", { name: "手动编辑" });
    await edit.click();
    const fields = await page.locator(".artifact-field-textarea, .artifact-field-input, .artifact-toggle").count();
    if (!fields) throw new Error("Editable artifact did not expose structured controls");
    await page.reload({ waitUntil: "networkidle" });
    await page.locator(".artifact-list button").first().waitFor();
  }

  const scriptUnitLabel = page.locator(".artifact-list button strong", { hasText: /^单集剧本$/ }).first();
  const episodeGroup = scriptUnitLabel.locator("xpath=ancestor::button");
  if (await episodeGroup.count()) {
    await episodeGroup.click();
    const episodeNav = page.locator(".artifact-episode-nav");
    await episodeNav.waitFor();
    const episodeButtons = await episodeNav.locator("button").count();
    if (episodeButtons < 2) throw new Error(`Episode navigation only exposed ${episodeButtons} items`);
    await episodeNav.locator("button").nth(1).click();
    await page.locator(".artifact-episode-nav button.selected").filter({ hasText: "第 2 集" }).waitFor();
  }
  await page.screenshot({ path: screenshotPath, fullPage: true });
  await writeFile(evidencePath, `${JSON.stringify({ checked_at: new Date().toISOString(), project_id: projectID, step_count: results.length, steps: results }, null, 2)}\n`, "utf8");
  process.stdout.write(`artifact workspace smoke passed: ${results.length} steps\n`);
} finally {
  await browser.close();
}
