const path = require("node:path");
const { pathToFileURL } = require("node:url");
const fs = require("node:fs");

function loadPlaywright() {
  try {
    return require("playwright");
  } catch {
    return require("C:\\Users\\egois\\Desktop\\novel2script_agent_project\\frontend\\node_modules\\playwright");
  }
}

const { chromium } = loadPlaywright();
const root = __dirname;
const htmlUrl = pathToFileURL(path.join(root, "index.html")).href;
const chromePath = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const evidenceDir = path.join(root, "evidence");

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

async function capture(page, name, fullPage = true) {
  await page.screenshot({ path: path.join(evidenceDir, `${name}.png`), fullPage });
}

async function clickAndConfirm(page, selector) {
  await page.locator(selector).click();
  await page.locator("#modal-confirm").click();
}

async function openProject(page, id) {
  await page.locator(`[data-project-id="${id}"]`).click();
  await page.locator("#workbench-view:not(.hidden)").waitFor();
}

async function backToProjects(page) {
  await page.locator("#back-to-projects").click();
  await page.locator("#projects-view:not(.hidden)").waitFor();
}

async function assertVisibleButtonsBound(page, label) {
  const unbound = await page.locator("button:not(:disabled)").evaluateAll((buttons) => buttons
    .filter((button) => {
      const style = window.getComputedStyle(button);
      const rect = button.getBoundingClientRect();
      return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
    })
    .filter((button) => button.dataset.qaClickBound !== "true")
    .map((button) => button.textContent.trim() || button.getAttribute("title") || button.id));
  assert(unbound.length === 0, `${label} 存在未绑定操作的可见按钮：${unbound.join("、")}`);
}

async function run() {
  fs.mkdirSync(evidenceDir, { recursive: true });
  const browser = await chromium.launch({
    headless: true,
    executablePath: chromePath
  });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  page.setDefaultTimeout(8000);
  await page.addInitScript(() => {
    const original = EventTarget.prototype.addEventListener;
    EventTarget.prototype.addEventListener = function patched(type, listener, options) {
      if (type === "click" && this instanceof Element) this.dataset.qaClickBound = "true";
      return original.call(this, type, listener, options);
    };
  });
  const errors = [];
  const results = {};
  page.on("console", (message) => {
    if (message.type() === "error") errors.push(`console: ${message.text()}`);
  });
  page.on("pageerror", (error) => errors.push(`page: ${error.message}`));

  try {
    await page.goto(htmlUrl, { waitUntil: "load" });
    await page.evaluate(() => window.localStorage.clear());
    await page.reload({ waitUntil: "load" });
    await page.locator("[data-project-id]").first().waitFor();
    await capture(page, "01-projects");
    await assertVisibleButtonsBound(page, "作品入口");
    results.initialProjectCount = await page.locator("[data-project-id]").count();
    assert(results.initialProjectCount === 4, "入口页应有 4 个固定验收作品");

    // P3-F01: anchored menu, keyboard close, inline rename, destructive confirmation.
    const firstMore = page.locator('[data-more="novel"]');
    await firstMore.click();
    assert(await page.locator(".context-menu").isVisible(), "更多操作必须打开锚定菜单");
    assert(await page.locator(".modal-backdrop").count() === 0, "更多操作不能直接打开模态框");
    await capture(page, "02-project-menu");
    await page.keyboard.press("Escape");
    assert(await page.locator(".context-menu").count() === 0, "Esc 应关闭菜单");
    assert(await firstMore.evaluate((element) => element === document.activeElement), "关闭菜单后焦点应返回触发按钮");

    await firstMore.click();
    await page.getByRole("menuitem", { name: "重命名" }).click();
    const renameInput = page.locator('[data-rename-input="novel"]');
    await renameInput.fill("凡骨问仙·测试");
    await renameInput.press("Enter");
    assert(await page.locator('[data-project-id="novel"]').innerText().then((text) => text.includes("凡骨问仙·测试")), "行内重命名应保存");

    await page.locator('[data-more="novel"]').click();
    await page.getByRole("menuitem", { name: "删除作品" }).click();
    assert(await page.locator('[role="dialog"]').isVisible(), "删除必须进入确认 Dialog");
    assert(await page.locator("#modal-confirm").innerText() === "删除作品", "危险确认文案应明确");
    await capture(page, "02b-delete-dialog");
    assert(await page.locator("#modal-cancel").evaluate((element) => element === document.activeElement), "Dialog 初始焦点应落在取消");
    await page.keyboard.press("Shift+Tab");
    assert(await page.locator("#modal-confirm").evaluate((element) => element === document.activeElement), "Dialog 焦点应在内部循环");
    await page.keyboard.press("Escape");
    assert(await page.locator('[data-project-id="novel"]').count() === 1, "取消删除必须保留作品");

    // P3-F02: create project, upload only saves, Skill only inserts, ambiguous intent asks.
    await page.locator("#new-project-button").click();
    await page.locator("#new-project-name").fill("通用入口验证");
    await page.locator("#modal-confirm").click();
    assert(await page.locator("#project-title").innerText() === "通用入口验证", "新建作品应进入空工作台");
    await capture(page, "02c-empty-project");
    await page.locator("#attach-button").click();
    await page.locator('input[name="material"][value="image"]').check();
    await page.locator("#modal-confirm").click();
    assert(await page.locator("#timeline").innerText().then((text) => text.includes("你希望我理解画面内容")), "图片意图不明确时必须追问");
    await capture(page, "02d-image-intent");
    await page.locator("#skill-button").click();
    await page.getByRole("menuitem", { name: "小说转剧本" }).click();
    assert((await page.locator("#composer-input").inputValue()).includes("@小说转剧本"), "点击 Skill 只应插入引用");
    assert(await page.locator("#header-skill").innerText() === "尚未选择", "插入引用不能立即启动 Skill");
    await page.locator("#composer-input").fill("帮我看看这个");
    await page.locator("#send-button").click();
    assert(await page.locator("#timeline").innerText().then((text) => text.includes("希望生成剧本、分析材料")), "模糊意图必须继续追问");
    await backToProjects(page);

    // P3-F03 and P3-F06: novel chain, edit, regenerate impact, script version, review, candidate.
    await openProject(page, "novel");
    await page.locator("#edit-artifact").click();
    assert(await page.locator("#artifact-content").getAttribute("contenteditable") === "true", "产物编辑应进入可编辑状态");
    await page.locator("#artifact-content").pressSequentially(" 补充");
    await page.locator("#edit-artifact").click();
    assert((await page.locator("#workspace-meta").innerText()).includes("版本 3"), "保存编辑应形成新版本");
    await page.locator("#approve-current").click(); // episode split
    await page.locator("#approve-current").click(); // episode cards
    await page.locator("#approve-current").click(); // scripts
    await page.locator("#save-script").click();
    assert((await page.locator("#workspace-meta").innerText()).includes("版本"), "剧本保存后仍停留在待确认状态");
    await page.locator('[data-view-id="storyBible"]').click();
    await page.locator("#regenerate-artifact").click();
    assert(await page.locator('[role="dialog"]').innerText().then((text) => text.includes("影响下游")), "修改已生成上游必须先展示影响");
    await capture(page, "03b-upstream-impact");
    await page.locator("#modal-cancel").click();
    await page.locator('[data-view-id="scripts"]').click();
    await page.locator("#approve-current").click(); // quality
    await page.locator('[data-quality-action="fix-all"]').click();
    await page.locator("#modal-confirm").click();
    assert((await page.locator("#workspace-meta").innerText()).includes("复审通过"), "质量返工后应自动复审");
    await page.locator("#create-candidate").click();
    assert((await page.locator("#workspace-title").innerText()) === "候选稿", "审核通过后应进入 Candidate");
    await assertVisibleButtonsBound(page, "小说 Candidate");
    await capture(page, "03-novel-candidate");
    await backToProjects(page);
    await page.reload({ waitUntil: "load" });
    await openProject(page, "novel");
    assert((await page.locator("#workspace-title").innerText()) === "候选稿", "刷新并重新打开后应恢复小说流程状态");
    await backToProjects(page);

    // P3-F04: non-novel full path.
    await openProject(page, "outline");
    const nonNovelStages = ["素材库", "故事种子", "整剧蓝图", "分集卡", "分集剧本", "质量审核"];
    await page.locator("#approve-expansion").click();
    for (const expected of nonNovelStages.slice(0, 4)) {
      assert((await page.locator("#workspace-title").innerText()) === expected, `非小说链应进入${expected}`);
      await page.locator("#approve-current").click();
    }
    assert((await page.locator("#workspace-title").innerText()) === "分集剧本", "非小说链应进入分集剧本");
    await page.locator("#approve-current").click();
    assert((await page.locator("#workspace-title").innerText()) === "质量审核", "剧本统一确认后应自动进入审核");
    await page.locator('[data-quality-action="fix-all"]').click();
    await page.locator("#modal-confirm").click();
    await page.locator("#create-candidate").click();
    assert((await page.locator("#workspace-title").innerText()) === "候选稿", "非小说链应形成 Candidate");
    await assertVisibleButtonsBound(page, "非小说 Candidate");
    await capture(page, "04-non-novel-candidate");
    await backToProjects(page);

    // P3-F05: video pause/resume, item retry, finish, incomplete confirmation and downstream path.
    await openProject(page, "video");
    await assertVisibleButtonsBound(page, "视频批次");
    assert(await page.locator("#composer-input").isDisabled(), "视频运行中 Composer 必须锁定");
    await page.locator("#pause-run").click();
    assert((await page.locator("#agent-state").innerText()) === "任务已暂停", "暂停状态必须可见");
    assert(await page.locator("#composer-input").isDisabled(), "暂停后仍应先恢复或取消 Run");
    await capture(page, "05a-video-paused");
    await page.locator("#resume-run").click();
    await page.locator('[data-video-action="retry"]').click();
    assert(await page.locator('[data-video-action="retry"]').count() === 0, "失败单集重试后不应继续显示失败操作");
    await page.locator('[data-video-action="finish"]').click();
    assert(await page.locator("#composer-input").isEnabled(), "批次解析结束后 Composer 应恢复");
    await page.locator("#seal-upload").click();
    assert((await page.locator('[role="dialog"]').innerText()).includes("缺少第 6 集"), "缺集继续必须明确提示");
    await capture(page, "05b-incomplete-confirm");
    await page.locator("#modal-confirm").click();
    assert((await page.locator("#workspace-title").innerText()) === "参考剧本", "确认上传完成后应进入参考剧本");
    const videoExpected = ["整剧分析", "改编方案", "Adaptation Brief", "新剧本配置", "故事种子", "整剧蓝图", "分集卡", "分集剧本", "质量审核"];
    for (const expected of videoExpected) {
      await page.locator("#approve-current").click();
      assert((await page.locator("#workspace-title").innerText()) === expected, `视频链应进入${expected}`);
    }
    await page.locator('[data-quality-action="fix-all"]').click();
    await page.locator("#modal-confirm").click();
    await page.locator("#create-candidate").click();
    assert((await page.locator("#workspace-title").innerText()) === "候选稿", "视频链应形成新剧本 Candidate");
    await assertVisibleButtonsBound(page, "视频 Candidate");
    await capture(page, "05-video-candidate");
    await backToProjects(page);

    // P3-F07: final selection, change selection, export.
    await openProject(page, "candidate");
    await page.locator('[data-select-final="B"]').click();
    assert(await page.locator('[role="dialog"]').isVisible(), "选择 Final 必须二次确认");
    await capture(page, "06a-final-dialog");
    await page.locator("#modal-confirm").click();
    assert((await page.locator("#workspace-meta").innerText()).includes("已选择当前最终稿"), "确认后应显示唯一当前 Final");
    await page.locator('[data-select-final="A"]').click();
    assert((await page.locator('[role="dialog"]').innerText()).includes("改选"), "改选 Final 必须再次确认");
    await page.locator("#modal-cancel").click();
    await page.locator('[data-export="txt"]').click();
    assert(await page.locator("#toast-root").innerText().then((text) => text.includes("TXT")), "最终稿应支持 TXT 导出反馈");
    assert(await page.locator("#toast-root .toast").count() === 1, "连续反馈最多显示一个 Toast");
    await page.locator("#export-button").click();
    assert(await page.getByRole("menuitem", { name: "导出 TXT" }).isVisible(), "顶栏导出应打开格式菜单");
    await page.keyboard.press("Escape");
    await assertVisibleButtonsBound(page, "最终稿选择");
    await capture(page, "06-final-selection");

    // P3-F08: switching preserves project state.
    await backToProjects(page);
    await openProject(page, "novel");
    assert((await page.locator("#workspace-title").innerText()) === "候选稿", "重新进入作品应恢复离开前阶段");
    await backToProjects(page);

    // Cancel branch uses an isolated page so the main video journey remains intact.
    const cancelContext = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const cancelPage = await cancelContext.newPage();
    cancelPage.setDefaultTimeout(8000);
    await cancelPage.goto(htmlUrl, { waitUntil: "load" });
    await openProject(cancelPage, "video");
    await cancelPage.locator("#cancel-run").click();
    await cancelPage.locator("#modal-confirm").click();
    assert((await cancelPage.locator("#header-status").innerText()) === "已取消", "取消 Run 后必须进入不可恢复的已取消状态");
    await cancelContext.close();

    // Desktop viewport and density checks.
    for (const viewport of [
      { width: 1280, height: 800, name: "1280x800" },
      { width: 1440, height: 900, name: "1440x900" },
      { width: 1920, height: 1080, name: "1920x1080" }
    ]) {
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      await page.goto(htmlUrl, { waitUntil: "load" });
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      assert(overflow <= 0, `${viewport.name} 不应出现页面级横向溢出`);
      await capture(page, `viewport-${viewport.name}`, false);
      results[viewport.name] = { overflow };
    }

    await page.setViewportSize({ width: 1280, height: 800 });
    await openProject(page, "candidate");
    await capture(page, "09-workbench-1280", false);
    await backToProjects(page);

    await page.setViewportSize({ width: 1440, height: 900 });
    await openProject(page, "video");
    await page.locator('[data-view-id="videoBatch"]').click();
    await page.evaluate(() => {
      const body = document.querySelector(".video-table tbody");
      const source = [...body.querySelectorAll("tr")];
      for (let episode = 11; episode <= 50; episode += 1) {
        const row = source[(episode - 1) % source.length].cloneNode(true);
        row.children[0].textContent = String(episode);
        row.children[1].textContent = `穷剑修${String(episode).padStart(2, "0")}.mov`;
        row.children[2].textContent = `第 ${episode} 集`;
        body.appendChild(row);
      }
    });
    const videoMetrics = await page.locator(".video-table").evaluate((table) => ({
      scrollWidth: table.scrollWidth,
      clientWidth: table.clientWidth,
      parentWidth: table.parentElement.clientWidth
    }));
    assert(videoMetrics.scrollWidth <= videoMetrics.parentWidth, "50 集视频表不应挤破中间工作区");
    await capture(page, "07-video-50");
    results.video50 = videoMetrics;

    await backToProjects(page);
    await openProject(page, "novel");
    await page.locator('[data-view-id="scripts"]').click();
    await page.locator("#script-editor").evaluate((editor) => {
      for (let index = 0; index < 100; index += 1) {
        const block = document.createElement("div");
        block.textContent = "△陆沉继续向前，围观弟子的议论逐渐停下。";
        editor.appendChild(block);
      }
    });
    const scriptMetrics = await page.locator(".script-paper").evaluate((paper) => ({
      scrollWidth: paper.scrollWidth,
      clientWidth: paper.clientWidth,
      scrollHeight: paper.scrollHeight,
      clientHeight: paper.clientHeight
    }));
    assert(scriptMetrics.scrollWidth <= scriptMetrics.clientWidth, "长剧本不应产生横向溢出");
    await capture(page, "08-long-script");
    results.longScript = scriptMetrics;

    results.errors = errors;
    assert(errors.length === 0, `页面不应出现 JS 错误：${errors.join("; ")}`);
    results.status = "pass";
    fs.writeFileSync(path.join(evidenceDir, "results.json"), `${JSON.stringify(results, null, 2)}\n`, "utf8");
    process.stdout.write(`${JSON.stringify(results, null, 2)}\n`);
  } finally {
    await browser.close();
  }
}

run().catch((error) => {
  console.error(error.stack || error);
  process.exitCode = 1;
});
