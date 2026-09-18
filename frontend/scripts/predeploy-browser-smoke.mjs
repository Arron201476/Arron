import { chromium } from "playwright";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8860";
const title = `Predeploy smoke ${Date.now()}`;
const renamedTitle = `${title} renamed`;
const browser = await chromium.launch({ channel: "msedge", headless: true });

async function assertNoHorizontalOverflow(page, label) {
  const overflow = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  if (overflow.scrollWidth > overflow.clientWidth + 1) {
    throw new Error(`${label}: horizontal overflow ${overflow.scrollWidth} > ${overflow.clientWidth}`);
  }
}

try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  await page.goto(baseURL, { waitUntil: "networkidle" });
  await page.locator(".project-workspace").waitFor();
  await assertNoHorizontalOverflow(page, "project list 1280x800");

  await page.locator(".result-button").click();
  await page.locator('input[name="project-title"]').fill(title);
  await page.locator(".dialog .primary-button").click();
  await page.waitForURL(/\/projects\/[^/]+$/);
  await page.locator(".agent-panel").waitFor();
  await assertNoHorizontalOverflow(page, "workbench 1280x800");

  await page.locator(".composer-input textarea").fill("predeploy-ping");
  await page.locator(".send-action").click();
  await page.locator(".timeline-message.user", { hasText: "predeploy-ping" }).waitFor();
  await page.locator(".timeline-message.assistant").waitFor();
  if (await page.locator(".timeline-message").count() < 2) {
    throw new Error("agent exchange did not render both messages");
  }

  await page.reload({ waitUntil: "networkidle" });
  await page.locator(".timeline-message.user", { hasText: "predeploy-ping" }).waitFor();
  await page.locator('.back-button[href="/"]').click();
  await page.waitForURL(`${baseURL}/`);
  const row = page.locator(".project-row", { hasText: title });
  await row.locator(".row-action .icon-button").click();
  await row.locator(".anchor-menu button").first().click();
  await page.locator('input[name="project-title"]').fill(renamedTitle);
  await page.locator(".dialog .primary-button").click();
  await page.locator(".project-row", { hasText: renamedTitle }).waitFor();

  const renamedRow = page.locator(".project-row", { hasText: renamedTitle });
  await renamedRow.locator(".row-action .icon-button").click();
  await renamedRow.locator(".anchor-menu .danger").click();
  await page.locator(".delete-dialog .destructive-outline").click();
  await page.locator(".delete-dialog .destructive-button").click();
  await page.locator(".project-row", { hasText: renamedTitle }).waitFor({ state: "detached" });

  await page.setViewportSize({ width: 1920, height: 1080 });
  await assertNoHorizontalOverflow(page, "project list 1920x1080");
  process.stdout.write("predeploy browser smoke passed\n");
} finally {
  await browser.close();
}
