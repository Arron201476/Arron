import { chromium } from "playwright";

const baseURL = process.env.CONTENT_AGENT_WEB_URL ?? "http://127.0.0.1:8860";
const projectTitle = `B7-0 浏览器联调 ${Date.now()}`;
const renamedTitle = `${projectTitle} 已重命名`;
const browser = await chromium.launch({ channel: "msedge", headless: true });

try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await page.goto(baseURL, { waitUntil: "networkidle" });
  await page.getByRole("heading", { name: "作品", exact: true }).waitFor();
  await page.getByRole("button", { name: "新建作品" }).click();
  await page.getByLabel("作品名称").fill(projectTitle);
  await page.getByRole("button", { name: "创建并打开" }).click();
  await page.waitForURL(/\/projects\/[^/]+$/);
  await page.getByText(projectTitle, { exact: true }).waitFor();
  await page.getByLabel("给 Agent 的消息").fill("Reply only: connected");
  await page.getByRole("button", { name: "发送" }).click();
  await page.getByText("connected", { exact: true }).waitFor();
  await page.getByRole("button", { name: "能力" }).click();
  await page.getByText("小说转剧本", { exact: true }).waitFor();
  await page.getByText("非小说文本转剧本", { exact: true }).waitFor();
  await page.getByText("视频参考创作", { exact: true }).waitFor();
  await page.getByText("小说转剧本", { exact: true }).click();
  await page.getByLabel("给 Agent 的消息").fill("Start novel to script");
  await page.getByRole("button", { name: "发送" }).click();
  await page.getByRole("heading", { name: "设置剧本体量" }).waitFor();
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.getByRole("heading", { name: "设置剧本体量" }).waitFor();
  await page.getByRole("link", { name: "作品", exact: true }).click();
  await page.waitForURL(`${baseURL}/`);

  await page.getByRole("button", { name: `打开 ${projectTitle} 的操作菜单` }).click();
  await page.getByRole("button", { name: "重命名" }).click();
  await page.getByLabel("作品名称").fill(renamedTitle);
  await page.getByRole("button", { name: "保存名称" }).click();
  await page.getByText(renamedTitle, { exact: true }).waitFor();

  await page.getByRole("button", { name: `打开 ${renamedTitle} 的操作菜单` }).click();
  await page.getByRole("button", { name: "删除作品" }).click();
  await page.getByRole("button", { name: "查看删除影响" }).click();
  await page.getByRole("button", { name: "确认删除作品" }).click();
  await page.getByText(renamedTitle, { exact: true }).waitFor({ state: "detached" });

  process.stdout.write("B7-0 real-backend browser smoke passed\n");
} finally {
  await browser.close();
}
