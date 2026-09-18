import { chromium } from "playwright";

const browser = await chromium.launch({
  headless: true,
  executablePath: "C:/Program Files/Google/Chrome/Application/chrome.exe",
});
const targetURL = process.env.SMOKE_URL || "http://127.0.0.1:8832/";
const screenshotLabel = process.env.SMOKE_LABEL || "global-audit-ui";

const results = [];
for (const viewport of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "compact", width: 1024, height: 768 },
]) {
  const context = await browser.newContext({ viewport });
  const page = await context.newPage();
  const consoleErrors = [];
  const responseErrors = [];
  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });
  page.on("pageerror", (error) => consoleErrors.push(error.message));
  page.on("response", (response) => {
    if (response.status() >= 400) responseErrors.push(`${response.status()} ${response.url()}`);
  });

  const response = await page.goto(targetURL, {
    waitUntil: "networkidle",
    timeout: 30_000,
  });
  await page.waitForTimeout(1_200);
  const metrics = await page.evaluate(() => ({
    title: document.title,
    bodyText: document.body.innerText.slice(0, 400),
    bodyScrollWidth: document.body.scrollWidth,
    bodyClientWidth: document.body.clientWidth,
    bodyScrollHeight: document.body.scrollHeight,
    bodyClientHeight: document.body.clientHeight,
    buttons: [...document.querySelectorAll("button")]
      .map((element) => (element.getAttribute("aria-label") || element.textContent || "").trim())
      .filter(Boolean)
      .slice(0, 40),
    visiblePanes: [...document.querySelectorAll("aside, main, [class*=pane]")].filter((element) => {
      const rect = element.getBoundingClientRect();
      return rect.width > 20 && rect.height > 20;
    }).length,
  }));
  await page.screenshot({
    path: `../backend/.tmp/${screenshotLabel}-${viewport.name}.png`,
    fullPage: false,
  });
  results.push({ viewport, status: response?.status(), ...metrics, consoleErrors, responseErrors });
  await context.close();
}

await browser.close();
console.log(JSON.stringify(results, null, 2));
