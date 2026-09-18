import { chromium } from "playwright";
import { mkdir } from "node:fs/promises";
import path from "node:path";

const outputDir = path.resolve("..", "artifacts", "visual-regression");
await mkdir(outputDir, { recursive: true });

const apiBase = "http://127.0.0.1:8831";
const projects = (await (await fetch(`${apiBase}/api/projects`)).json()).projects || [];
const completedProjects = projects
  .filter((project) => project.status === "completed" && project.active_run_id)
  .sort((left, right) => String(right.updated_at || "").localeCompare(String(left.updated_at || "")));
let completedProject = null;
for (const project of completedProjects) {
  const snapshot = await (await fetch(`${apiBase}/api/runs/${project.active_run_id}`)).json();
  if ((snapshot.artifacts || []).some((artifact) => artifact.artifact_type === "script_unit" && !["superseded", "invalidated"].includes(artifact.status))) {
    completedProject = project;
    break;
  }
}
const idleProject = projects
  .filter((project) => !project.active_run_id)
  .sort((left, right) => String(right.updated_at || "").localeCompare(String(left.updated_at || "")))[0];
if (!completedProject || !idleProject) throw new Error("Visual audit requires one completed script project and one idle project.");

const browser = await chromium.launch({ channel: "chrome", headless: true });
const checks = [];

for (const viewport of [
  { name: "desktop", width: 1920, height: 1000 },
  { name: "compact", width: 1024, height: 768 },
]) {
  const page = await browser.newPage({ viewport });
  await page.addInitScript((projectID) => window.localStorage.setItem("n2s.activeProjectId", projectID), completedProject.project_id);
  await page.goto("http://127.0.0.1:8832/", { waitUntil: "networkidle" });
  await page.locator(".project-picker-backdrop").waitFor({ state: "detached", timeout: 15000 });
  await page.locator(".script-editor-layout").waitFor({ state: "visible", timeout: 15000 });
  await page.screenshot({ path: path.join(outputDir, `${viewport.name}.png`), fullPage: true });
  let editorChecks = null;
  let layoutChecks = null;
  let sourceChecks = null;
  let artifactReaderChecks = null;
  let emptyProcessChecks = null;
  if (viewport.name === "desktop") {
    layoutChecks = await page.evaluate(() => {
      const rect = (selector) => {
        const value = document.querySelector(selector)?.getBoundingClientRect();
        return value ? { top: Math.round(value.top), bottom: Math.round(value.bottom), left: Math.round(value.left), right: Math.round(value.right), width: Math.round(value.width), height: Math.round(value.height) } : null;
      };
      return {
        projectHead: rect(".project-card"),
        contentHead: rect(".artifact-head"),
        agentHead: rect(".agent-pane > .pane-head"),
        scriptContent: rect(".script-editor-layout"),
      };
    });
    await page.getByRole("button", { name: "过程产物" }).click();
    const sourceInput = page.locator(".nav-item").filter({ hasText: "输入材料" });
    if (await sourceInput.count() === 1) await sourceInput.click();
    sourceChecks = await page.evaluate(() => {
      const pre = document.querySelector(".source-preview-text pre");
      const card = document.querySelector(".source-preview-card.source-preview-text");
      return {
        hasGenerationConfig: document.querySelector(".source-config-block") !== null,
        preOverflowY: pre ? getComputedStyle(pre).overflowY : null,
        cardOverflowY: card ? getComputedStyle(card).overflowY : null,
      };
    });
    const sourceRect = await page.locator(".source-preview-card").boundingBox();
    layoutChecks.sourceContent = sourceRect ? { left: Math.round(sourceRect.x), right: Math.round(sourceRect.x + sourceRect.width), width: Math.round(sourceRect.width) } : null;
    await page.screenshot({ path: path.join(outputDir, "desktop-source-input.png"), fullPage: true });
    let readableArtifact = null;
    for (const label of ["故事圣经", "素材库", "原文拆集", "故事种子"]) {
      const candidate = page.locator(".nav-item").filter({ hasText: label });
      if (await candidate.count() === 1) {
        readableArtifact = candidate;
        break;
      }
    }
    if (!readableArtifact) throw new Error("Completed project has no readable planning artifact.");
    await readableArtifact.click();
    const artifactRect = await page.locator(".artifact-card").boundingBox();
    layoutChecks.artifactContent = artifactRect ? { left: Math.round(artifactRect.x), right: Math.round(artifactRect.x + artifactRect.width), width: Math.round(artifactRect.width) } : null;
    const artifactSections = page.locator(".artifact-section");
    for (let index = 0; index < await artifactSections.count(); index += 1) {
      const section = artifactSections.nth(index);
      if (!(await section.evaluate((element) => element.open))) await section.locator("summary").click();
    }
    artifactReaderChecks = await page.evaluate(() => {
      const outline = document.querySelector(".artifact-reader-outline");
      const content = document.querySelector(".artifact-section-list");
      const body = document.querySelector(".artifact-body");
      if (!outline || !content || !body) return null;
      const topBefore = Math.round(outline.getBoundingClientRect().top);
      content.scrollTop = Math.min(800, content.scrollHeight - content.clientHeight);
      const topAfter = Math.round(outline.getBoundingClientRect().top);
      const dimensions = (element) => ({
        clientHeight: element.clientHeight,
        scrollHeight: element.scrollHeight,
        height: Math.round(element.getBoundingClientRect().height),
        overflowY: getComputedStyle(element).overflowY,
      });
      return {
        bodyClassName: body.className,
        bodyDimensions: dimensions(body),
        focusDimensions: dimensions(document.querySelector(".artifact-focus-view")),
        stackDimensions: dimensions(document.querySelector(".artifact-stack")),
        cardDimensions: dimensions(document.querySelector(".artifact-card")),
        readableDimensions: dimensions(document.querySelector(".artifact-readable")),
        layoutDimensions: dimensions(document.querySelector(".artifact-reader-layout")),
        contentDimensions: dimensions(content),
        outlineTopBefore: topBefore,
        outlineTopAfter: topAfter,
        outlineStayedFixed: topBefore === topAfter,
        contentScrollTop: Math.round(content.scrollTop),
        bodyScrollTop: Math.round(body.scrollTop),
        contentOwnsScroll: content.scrollHeight > content.clientHeight && content.scrollTop > 0 && body.scrollTop === 0,
      };
    });
    await page.screenshot({ path: path.join(outputDir, "desktop-artifact.png"), fullPage: true });
    const runLog = page.locator(".nav-item").filter({ hasText: "运行记录" });
    if (await runLog.count() === 1) await runLog.click();
    const processRect = await page.locator(".process-log").boundingBox();
    layoutChecks.processContent = processRect ? { left: Math.round(processRect.x), right: Math.round(processRect.x + processRect.width), width: Math.round(processRect.width) } : null;
    await page.screenshot({ path: path.join(outputDir, "desktop-events.png"), fullPage: true });

    const scripts = page.locator(".nav-item").filter({ hasText: /^剧本/ });
    if (await scripts.count() === 1) await scripts.click();
    const firstLine = page.locator(".lexical-production-editor .script-line").first();
    await firstLine.waitFor({ state: "visible" });
    const selectFirstText = () => firstLine.evaluate((element) => {
      const text = element.firstChild;
      if (!text) return;
      const range = document.createRange();
      range.selectNodeContents(element);
      const selection = window.getSelection();
      selection?.removeAllRanges();
      selection?.addRange(range);
    });
    await selectFirstText();
    await page.getByRole("button", { name: "下划线" }).click();
    await selectFirstText();
    await page.getByRole("button", { name: "删除线" }).click();
    editorChecks = {
      hasLineTypeSelect: await page.getByRole("combobox", { name: "行类型" }).count() > 0,
      underlineApplied: await firstLine.locator(".script-text-underline, .script-text-underline-strikethrough").count() > 0,
      strikeApplied: await firstLine.locator(".script-text-strikethrough, .script-text-underline-strikethrough").count() > 0,
    };
    await page.screenshot({ path: path.join(outputDir, "desktop-script-format.png"), fullPage: true });

    const emptyPage = await browser.newPage({ viewport });
    await emptyPage.addInitScript((projectID) => window.localStorage.setItem("n2s.activeProjectId", projectID), idleProject.project_id);
    await emptyPage.goto("http://127.0.0.1:8832/", { waitUntil: "networkidle" });
    await emptyPage.locator(".project-picker-backdrop").waitFor({ state: "detached", timeout: 15000 });
    await emptyPage.locator(".project-name").filter({ hasText: idleProject.title }).waitFor({ state: "visible", timeout: 15000 });
    await emptyPage.getByRole("button", { name: "输入材料", exact: true }).waitFor({ state: "visible", timeout: 15000 });
    emptyProcessChecks = await emptyPage.evaluate(() => {
      const labels = [...document.querySelectorAll(".nav-item")].map((item) => item.textContent || "");
      const tabs = [...document.querySelectorAll(".view-tabs button")].map((item) => item.textContent?.trim() || "");
      const userMessages = [...document.querySelectorAll(".chat-message.user .chat-text")].map((item) => item.textContent || "");
      const agentMessages = [...document.querySelectorAll(".chat-message.agent .chat-text")].map((item) => item.textContent || "");
      return {
        projectName: document.querySelector(".project-name")?.textContent?.trim() || "",
        hasProcessNavigation: labels.some((label) => label.includes("过程产物")),
        hasInputNavigation: labels.some((label) => label.includes("输入材料")),
        persistedMessageCount: userMessages.length + agentMessages.length,
        hasActiveApproval: document.querySelector(".inline-approval:not(.inactive)") !== null,
        tabs,
      };
    });
    await emptyPage.close();
  } else {
    await page.getByRole("tab", { name: "Agent" }).click();
    await page.screenshot({ path: path.join(outputDir, "compact-agent.png"), fullPage: true });
  }
  checks.push(await page.evaluate(({ name, width, height, editorChecks, layoutChecks, sourceChecks, artifactReaderChecks, emptyProcessChecks }) => {
    const visible = (element) => {
      const style = getComputedStyle(element);
      const rect = element.getBoundingClientRect();
      return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0;
    };
    const overflow = [...document.querySelectorAll("body *")]
      .filter(visible)
      .map((element) => {
        const rect = element.getBoundingClientRect();
        return {
          className: String(element.className || "").slice(0, 100),
          tag: element.tagName,
          left: Math.round(rect.left),
          right: Math.round(rect.right),
          top: Math.round(rect.top),
          bottom: Math.round(rect.bottom),
        };
      })
      .filter((rect) => rect.left < -2 || rect.right > width + 2 || rect.top < -2 || rect.bottom > height + 2)
      .slice(0, 20);

    const workspace = document.querySelector(".workspace")?.getBoundingClientRect();
    const artifact = document.querySelector(".artifact-pane")?.getBoundingClientRect();
    return {
      name,
      viewport: { width, height },
      workspace: workspace ? { width: Math.round(workspace.width), height: Math.round(workspace.height) } : null,
      artifact: artifact ? { width: Math.round(artifact.width), height: Math.round(artifact.height) } : null,
      horizontalOverflow: document.documentElement.scrollWidth > width,
      overflow,
      editorChecks,
      layoutChecks,
      sourceChecks,
      artifactReaderChecks,
      emptyProcessChecks,
    };
  }, { ...viewport, editorChecks, layoutChecks, sourceChecks, artifactReaderChecks, emptyProcessChecks }));
  await page.close();
}

await browser.close();
console.log(JSON.stringify(checks, null, 2));

const failures = [];
const desktop = checks.find((check) => check.name === "desktop");
const compact = checks.find((check) => check.name === "compact");
if (!desktop || !compact) failures.push("missing viewport result");
if (desktop?.horizontalOverflow || compact?.horizontalOverflow) failures.push("horizontal overflow detected");
if (desktop?.editorChecks?.hasLineTypeSelect) failures.push("obsolete line-type selector is visible");
if (!desktop?.editorChecks?.underlineApplied || !desktop?.editorChecks?.strikeApplied) failures.push("script underline or strike formatting failed");
if (desktop?.sourceChecks?.hasGenerationConfig) failures.push("source input exposes generation config");
if (desktop?.sourceChecks?.preOverflowY !== "visible" || desktop?.sourceChecks?.cardOverflowY !== "visible") failures.push("source input contains an internal vertical scrollbar");
if (!desktop?.artifactReaderChecks?.outlineStayedFixed || !desktop?.artifactReaderChecks?.contentOwnsScroll) failures.push("artifact outline or content scrolling failed");
if (desktop?.emptyProcessChecks?.hasProcessNavigation || !desktop?.emptyProcessChecks?.hasInputNavigation) failures.push("idle project navigation is incorrect");
if ((desktop?.emptyProcessChecks?.persistedMessageCount || 0) < 2) failures.push("idle project chat history did not survive reload");
if (desktop?.emptyProcessChecks?.hasActiveApproval) failures.push("idle greeting incorrectly exposes an approval card");
const contentWidths = [
  desktop?.layoutChecks?.sourceContent?.width,
  desktop?.layoutChecks?.artifactContent?.width,
  desktop?.layoutChecks?.scriptContent?.width,
  desktop?.layoutChecks?.processContent?.width,
];
if (new Set(contentWidths).size !== 1 || contentWidths.some((width) => !width)) failures.push("content view widths are not aligned");
if (failures.length) throw new Error(`Visual audit failed: ${failures.join("; ")}`);
