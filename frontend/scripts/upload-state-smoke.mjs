import { chromium } from "playwright";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8860";
const title = `Upload state smoke ${Date.now()}`;
const browser = await chromium.launch({ channel: "msedge", headless: true });
let projectID = "";

async function cleanupProject(page) {
  if (!projectID) return;
  const headers = {
    "Idempotency-Key": crypto.randomUUID(),
    "X-Client-Instance-ID": "upload-state-smoke",
  };
  const previewResponse = await page.request.post(`${baseURL}/api/v1/projects/${projectID}/delete-previews`, { headers });
  if (!previewResponse.ok()) throw new Error(`cleanup preview failed: ${previewResponse.status()}`);
  const preview = (await previewResponse.json()).data;
  headers["Idempotency-Key"] = crypto.randomUUID();
  const deleteResponse = await page.request.post(`${baseURL}/api/v1/projects/${projectID}/delete-confirmations`, {
    headers,
    data: { preview_hash: preview.snapshot_hash, confirmed: true },
  });
  if (!deleteResponse.ok()) throw new Error(`cleanup confirmation failed: ${deleteResponse.status()}`);
}

try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await page.goto(baseURL, { waitUntil: "networkidle" });
  await page.locator(".result-button").click();
  await page.locator('input[name="project-title"]').fill(title);
  await page.locator(".dialog .primary-button").click();
  await page.waitForURL(/\/projects\/[^/]+$/);
  projectID = new URL(page.url()).pathname.split("/").at(-1) ?? "";

  const fileInput = page.locator('input[type="file"]');
  const video = (name) => ({
    name,
    mimeType: "video/mp4",
    buffer: Buffer.from([0, 0, 0, 24, 102, 116, 121, 112, 109, 112, 52, 50]),
  });
  await fileInput.setInputFiles(video("jimeng-reference.mp4"));
  await page.locator(".batch-dialog").waitFor();
  await page.waitForTimeout(800);
  await page.locator(".batch-dialog").waitFor();
  if (await page.locator(".batch-list > div").count() !== 1) {
    throw new Error("first uploaded video was not retained in the batch dialog");
  }
  await page.getByRole("button", { name: "确认jimeng-reference.mp4的集号" }).click();
  await page.getByText("第 1 集", { exact: true }).waitFor();

  await page.locator(".batch-dialog .secondary-button").click();
  await fileInput.setInputFiles(video("episode-02.mp4"));
  await page.locator(".batch-dialog").waitFor();
  await page.waitForTimeout(800);
  if (await page.locator(".batch-list > div").count() !== 2) {
    throw new Error("background refresh replaced the existing video batch");
  }

  process.stdout.write("upload state smoke passed\n");
} finally {
  const pages = browser.contexts().flatMap((context) => context.pages());
  if (pages[0]) {
    await cleanupProject(pages[0]).catch((error) => process.stderr.write(`upload state smoke cleanup warning: ${error.message}\n`));
  }
  await browser.close();
}
