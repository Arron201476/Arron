import { chromium } from "playwright";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8860";
const outputDir = resolve("../design/audits/2026-08-11-context-interaction-final");
const targetProject = "prj_4c3470daa02805521268b2101f395030";
await mkdir(outputDir, { recursive: true });

const browser = await chromium.launch({ channel: "msedge", headless: true });
const page = await browser.newPage({ viewport: { width: 1920, height: 1080 } });
page.setDefaultTimeout(15_000);
const report = {};

try {
  await page.goto(baseURL, { waitUntil: "domcontentloaded" });
  await page.locator(".project-row:not(.table-head)").first().waitFor();
  const projectMenuButton = page.locator('.project-row:not(.table-head) button[aria-label^="打开"]').first();
  await projectMenuButton.click();
  await page.locator(".anchor-menu").waitFor();
  await page.keyboard.press("Escape");
  if (await page.locator(".anchor-menu").count()) throw new Error("project menu did not close on Escape");
  report.projectMenu = "escape closes";

  await page.goto(`${baseURL}/projects/${targetProject}`, { waitUntil: "domcontentloaded" });
  await page.locator(".artifact-workspace").waitFor();
  const scriptGroup = page.locator(".artifact-group-button", { hasText: "视频还原剧本" }).first();
  await scriptGroup.click();
  const episodeCount = await page.locator(".artifact-episode-list button").count();
  if (!episodeCount) throw new Error("script episode directory did not open");
  await scriptGroup.click();
  if (await page.locator(".artifact-episode-list").count()) throw new Error("script episode directory did not collapse");
  await scriptGroup.click();
  await page.locator(".artifact-episode-list button").first().click();

  const selectedLine = page.locator(".script-body-editor").first();
  await selectedLine.waitFor();
  await selectedLine.selectText();
  await selectedLine.dispatchEvent("mouseup");
  const reference = page.locator(".selection-reference");
  try {
    await reference.waitFor();
  } catch (error) {
    await page.screenshot({ path: resolve(outputDir, "00-selection-failure.png"), fullPage: true });
    const diagnostic = await page.evaluate(() => ({ selection: window.getSelection()?.toString(), context: document.querySelector(".composer-context")?.textContent }));
    throw new Error(`selection reference missing: ${JSON.stringify(diagnostic)}; ${error}`);
  }
  const referenceText = await reference.innerText();
  if (!referenceText.includes("视频还原剧本")) throw new Error(`selection reference has no artifact label: ${referenceText}`);
  await page.screenshot({ path: resolve(outputDir, "01-selection-reference.png"), fullPage: true });

  let requestBody;
  await page.route("**/api/v1/conversations/*/messages", async (route) => {
    requestBody = route.request().postDataJSON();
    const now = new Date().toISOString();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: {
        user_message: { message_id: "msg_context_user", conversation_id: "conversation", project_id: targetProject, role: "user", content: requestBody.content, created_at: now },
        agent_message: { message_id: "msg_context_agent", conversation_id: "conversation", project_id: targetProject, role: "assistant", content: "已按引用内容定位。", created_at: now },
        message_context: { capability_ref: null, attachment_refs: [], selection_snapshot: requestBody.selection_snapshot, client_context: requestBody.client_context },
        agent_decision: { agent_decision_id: "decision", decision: { intent: "chat" } }, proposed_action: null, run_ref: null,
      } }),
    });
  });
  await page.getByRole("textbox", { name: "给 Agent 的消息" }).fill("检查这段内容");
  await page.getByRole("button", { name: "发送" }).click();
  await page.locator(".timeline-message.user .message-reference").last().waitFor();
  if (!requestBody?.selection_snapshot?.selection?.text_range?.selected_text) throw new Error("message request lost selected text");
  if (!requestBody?.client_context?.current_artifact_id || !requestBody?.client_context?.current_artifact_version_id) throw new Error("message request lost active artifact context");
  report.context = {
    selectionSubmitted: true,
    artifactContextSubmitted: true,
    selectionHashLength: requestBody.selection_snapshot.snapshot_hash.length,
    historyReferenceVisible: true,
  };
  await page.screenshot({ path: resolve(outputDir, "02-history-reference.png"), fullPage: true });

  const skillButton = page.getByRole("button", { name: /Skill/ }).last();
  await skillButton.click();
  await page.locator(".capability-menu").waitFor();
  await page.keyboard.press("Escape");
  if (await page.locator(".capability-menu").count()) throw new Error("Skill menu did not close on Escape");
  report.skillMenu = "escape closes";

  const candidateButton = page.locator(".candidate-control > button");
  if (await candidateButton.isEnabled()) {
    await candidateButton.click();
    await page.locator(".candidate-menu").waitFor();
    await page.locator(".artifact-workspace").click({ position: { x: 24, y: 120 } });
    if (await page.locator(".candidate-menu").count()) throw new Error("candidate menu did not close on outside click");
    report.candidateMenu = "outside click closes";
  } else {
    report.candidateMenu = "not available in sample";
  }

  await page.setViewportSize({ width: 1280, height: 800 });
  const dimensions = await page.evaluate(() => ({ client: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
  if (dimensions.scroll > dimensions.client + 1) throw new Error(`horizontal overflow at 1280: ${dimensions.scroll} > ${dimensions.client}`);
  report.layout = { viewport: "1280x800", horizontalOverflow: false };
  await page.screenshot({ path: resolve(outputDir, "03-context-1280.png"), fullPage: true });

  await writeFile(resolve(outputDir, "report.json"), `${JSON.stringify(report, null, 2)}\n`, "utf8");
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
} finally {
  await browser.close();
}
