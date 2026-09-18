import { chromium } from "playwright";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8860";
const outputDir = resolve("../design/audits/2026-08-11-full-frontend/after");
const samples = {
  novel: "prj_7be204db6b71c1a97e49247fbbdf4c22",
  nonNovel: "prj_a90732e1004ce53e84f1eb73dfc07c05",
  video: "prj_4c3470daa02805521268b2101f395030",
};

await mkdir(outputDir, { recursive: true });
const browser = await chromium.launch({ channel: "msedge", headless: true });
const page = await browser.newPage({ viewport: { width: 1920, height: 1080 } });
page.setDefaultTimeout(15_000);

async function openProject(id) {
  await page.goto(`${baseURL}/projects/${id}`, { waitUntil: "domcontentloaded" });
  await page.locator(".artifact-workspace").waitFor();
}

async function selectArtifact(label) {
  const button = page.locator(".artifact-group-button", { hasText: label }).first();
  await button.scrollIntoViewIfNeeded();
  await button.click();
}

async function assertNoUserFacingTechnicalData(label) {
  const body = await page.locator(".artifact-workspace").innerText();
  for (const forbidden of ["来源与技术信息", "原始数据（调试）", "source_refs", "unit_refs"]) {
    if (body.includes(forbidden)) throw new Error(`${label}: leaked ${forbidden}`);
  }
}

let passed = false;
const report = {};
try {

  await openProject(samples.novel);
  await selectArtifact("单集剧本");
  const novelEpisodes = await page.locator(".artifact-episode-list button").count();
  if (!novelEpisodes) throw new Error("novel: missing nested episode directory");
  const novelGroup = page.locator(".artifact-group-button", { hasText: "单集剧本" }).first();
  await novelGroup.click();
  if (await page.locator(".artifact-episode-list").count()) throw new Error("novel: episode directory did not collapse");
  await novelGroup.click();
  if (await page.locator(".artifact-episode-list button").count() !== novelEpisodes) throw new Error("novel: episode directory did not reopen");
  if (await page.locator(".artifact-episode-nav").count()) throw new Error("novel: obsolete middle episode strip still visible");
  await page.locator(".artifact-episode-list button").first().click();
  await page.locator(".script-body-editor").waitFor();
  for (const label of ["撤销", "重做", "加粗", "斜体", "下划线", "删除线", "保存新版本"]) {
    if (!await page.getByRole("button", { name: label }).count()) throw new Error(`novel editor: missing ${label}`);
  }
  await page.screenshot({ path: resolve(outputDir, "00-script-editor.png"), fullPage: true });
  await page.locator(".artifact-workspace").evaluate((element) => { element.scrollTop = 900; });
  const saveButtonBox = await page.getByRole("button", { name: "保存新版本" }).boundingBox();
  if (!saveButtonBox || saveButtonBox.y > 180) throw new Error("novel editor: save command is not sticky after scrolling");
  report.editor = { controls: 7, saveVisibleAfterScroll: true };
  await selectArtifact("完整剧本");
  await page.locator(".script-collection-episode").first().waitFor();
  const novelCollectionEpisodes = await page.locator(".script-collection-episode").count();
  await page.screenshot({ path: resolve(outputDir, "01-novel.png"), fullPage: true });
  report.novel = { nestedEpisodes: novelEpisodes, collectionEpisodes: novelCollectionEpisodes };

  await openProject(samples.nonNovel);
  await selectArtifact("素材库");
  await assertNoUserFacingTechnicalData("non-novel material bank");
  const nonNovelStatus = await page.locator(".workbench-title span").innerText();
  await page.screenshot({ path: resolve(outputDir, "02-non-novel.png"), fullPage: true });
  report.nonNovel = { status: nonNovelStatus };

  await openProject(samples.video);
  await selectArtifact("视频还原剧本");
  const videoEpisodes = await page.locator(".artifact-episode-list button").count();
  if (!videoEpisodes) throw new Error("video: missing nested episode directory");
  await selectArtifact("参考剧本合集");
  await page.locator(".script-collection-episode").first().waitFor();
  const referenceEpisodes = await page.locator(".script-collection-content").evaluate((element) => element.querySelectorAll(".script-collection-episode").length);
  if (!referenceEpisodes) throw new Error("video: reference collection has no readable episode content");
  const runContext = await page.locator(".agent-run-context").count();
  await page.screenshot({ path: resolve(outputDir, "03-video.png"), fullPage: true });
  report.video = { nestedEpisodes: videoEpisodes, collectionEpisodes: referenceEpisodes, runContextVisible: Boolean(runContext) };

  await page.setViewportSize({ width: 1280, height: 800 });
  const layout = await page.evaluate(() => ({ clientWidth: document.documentElement.clientWidth, scrollWidth: document.documentElement.scrollWidth }));
  if (layout.scrollWidth > layout.clientWidth + 1) throw new Error(`1280 layout: horizontal overflow ${layout.scrollWidth} > ${layout.clientWidth}`);
  report.layout = { viewport: "1280x800", horizontalOverflow: false };
  await page.screenshot({ path: resolve(outputDir, "04-video-1280.png"), fullPage: true });

  await writeFile(resolve(outputDir, "report.json"), `${JSON.stringify(report, null, 2)}\n`, "utf8");
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  passed = true;
} catch (error) {
  process.stderr.write(`${error instanceof Error ? error.stack : String(error)}\n`);
} finally {
  await Promise.race([browser.close(), new Promise((resolve) => setTimeout(resolve, 5_000))]);
  process.exit(passed ? 0 : 1);
}
