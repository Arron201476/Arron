from __future__ import annotations

import html
from pathlib import Path
import re


ROOT = Path(__file__).resolve().parents[1] / "design"


def slug(value: str) -> str:
    value = value.replace("\\", "/")
    value = re.sub(r"[^\w\u4e00-\u9fff-]+", "_", value)
    return value.strip("_") or "section"


def read_text(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def file_id(rel: str) -> str:
    return "file_" + slug(Path(rel).with_suffix("").as_posix())


def render_markdown(rel: str) -> str:
    path = ROOT / rel
    content = html.escape(read_text(path))
    return (
        f"<section class='doc-section' id='{file_id(rel)}'>"
        f"<h3>{html.escape(rel)}</h3>"
        f"<pre>{content}</pre>"
        f"</section>"
    )


def render_html(rel: str) -> str:
    safe_rel = html.escape(rel.replace("\\", "/"))
    return (
        f"<section class='doc-section html-doc' id='{file_id(rel)}'>"
        f"<h3>{html.escape(rel)}</h3>"
        f"<p><a class='open-html' href='{safe_rel}' target='_self'>打开独立 HTML 页面</a></p>"
        f"<iframe src='{safe_rel}' title='{html.escape(rel)}'></iframe>"
        f"</section>"
    )


def doc_link(rel: str, role: str = "") -> str:
    label = html.escape(rel)
    suffix = f"<span>{html.escape(role)}</span>" if role else ""
    return f"<a class='doc-link' href='#{file_id(rel)}'>{label}{suffix}</a>"


CATEGORIES = [
    {
        "title": "00. 开发准入总览",
        "status": "总览",
        "summary": "说明通用 AI 项目进入前后端开发前需要哪些文档，并盘点当前 Novel2Script 的覆盖情况。",
        "docs": [
            ("zero-to-one-remediation-20260713.md", "52 项修复闭环与全量回归证据"),
            ("zero-to-one-project-audit-20260713.md", "当前 0-1 全面审计与整改优先级"),
            ("project-workspace-persistence-contract.md", "作品工作区与持久化合同"),
            ("project-workspace-implementation-20260713.md", "作品工作区实施与回归记录"),
            ("artifact-version-conflict-implementation-20260713.md", "Artifact 版本冲突保护实施记录"),
            ("development-readiness-checklist.md", "通用准入标准"),
            ("development-readiness-gap-analysis.md", "当前项目盘点"),
            ("implementation-contract-consistency-audit.md", "开工前合同一致性审计"),
            ("project-stage-communication-notes.md", "阶段汇报代办"),
        ],
        "missing": [],
    },
    {
        "title": "01. 产品目标与边界",
        "status": "初版具备",
        "summary": "确定用户、输入、输出、主路径、本期做/不做、AI 与普通 UI 的职责边界。",
        "docs": [
            ("product-scope-contract.md", "产品边界合同"),
            ("product-flow-audit.md", "流程边界"),
            ("product-review-checklist.md", "产品审查"),
            ("screenwriter-feedback-analysis.html", "编剧反馈来源"),
        ],
        "missing": ["产品指标验证", "范围变化同步机制", "版本取舍记录"],
    },
    {
        "title": "02. 用户流程与状态机",
        "status": "初版具备",
        "summary": "统一用户状态、run 状态、step 状态、审批、暂停、失败、完成等转移关系。",
        "docs": [
            ("workflow-state-transition-contract.md", "状态转移合同"),
            ("workflow-state.md", "状态定义"),
            ("frontend-interaction-audit.md", "前端状态规则"),
            ("main-agent-operations.md", "Agent 主循环"),
        ],
        "missing": ["后端 state reducer 实现", "事件映射测试", "暂停/继续/确认/失败恢复测试"],
    },
    {
        "title": "03. 信息架构",
        "status": "初版具备",
        "summary": "定义左侧目录、中间内容区、右侧 Agent 入口各自承载什么。",
        "docs": [
            ("information-architecture-contract.md", "信息架构合同"),
            ("frontend-interaction-audit.md", "三栏职责"),
            ("frontend-component-language.md", "组件语义"),
        ],
        "missing": ["React 组件映射", "响应式实现", "视觉终稿校验"],
    },
    {
        "title": "04. 静态 UI 稿 / 高保真交互稿",
        "status": "初版具备",
        "summary": "前端开发前必须有覆盖关键状态的 UI 稿；当前以多状态 HTML gallery 作为主稿。",
        "docs": [
            ("frontend-design-system.md", "设计系统"),
            ("frontend-ui-ue-refactor-contract.md", "UI/UE 重构合同"),
            ("frontend-visual-direction-decision.md", "视觉方向选择"),
            ("frontend-direction1-module-audit.md", "方向 1 模块取舍"),
            ("frontend-final-ui-draft.html", "高保真 UI 定稿草案"),
            ("frontend-ui-state-spec.md", "状态规格"),
            ("frontend-ui-state-gallery.html", "多状态 UI 稿"),
            ("audits/product-design-script-editor/audit-report.md", "Product Design 审计"),
        ],
        "missing": ["React 组件实现", "Playwright 视觉回归", "真实数据接入后复核"],
    },
    {
        "title": "05. 组件语义与文案规范",
        "status": "初版具备",
        "summary": "约束按钮、状态、图标、任务步骤、审批卡、附件、输入框的命名和显示。",
        "docs": [
            ("frontend-ui-ue-implementation-handoff.md", "UI/UE 实现交接"),
            ("frontend-component-library-plan.md", "组件库落地方案"),
            ("figma-component-spec.md", "Figma 组件蓝图"),
            ("figma-phase0-discovery.md", "Figma Phase 0 discovery"),
            ("frontend-component-spec.md", "组件规格"),
            ("frontend-implementation-plan.md", "TypeScript 前端实现计划"),
            ("frontend-component-language.md", "组件语义"),
            ("script-editor-spike-plan.md", "编辑器 spike 合同"),
            ("script-editor-spike-result.md", "编辑器 spike 阶段结果"),
            ("audits/script-editor-selection-gate/gate-report.md", "编辑器选型 gate"),
            ("script-editor-lexical-spec.md", "Lexical 正式规格"),
            ("audits/script-editor-selection-gate/results.json", "编辑器 gate 结果"),
            ("fixtures/script-editor-spike-fixture.json", "编辑器 spike fixture"),
            ("editor-research-notes.md", "编辑器参考"),
        ],
        "missing": ["浏览器交互验证", "长剧本性能测试", "真实 suggestion patch", "React 组件状态页", "组件单测", "可访问性测试"],
    },
    {
        "title": "06. 数据对象 / Artifact Schema",
        "status": "初版具备",
        "summary": "定义输入、中间产物、最终结果、字段来源、可编辑性、失效传播。",
        "docs": [
            ("shared-schema-contract.md", "共享类型合同"),
            ("artifact-schemas.md", "Artifact 结构"),
            ("local-file-attachments.md", "文件对象"),
        ],
        "missing": ["Go struct 实现", "TypeScript types 实现", "schema fixture", "API response 校验"],
    },
    {
        "title": "07. Agent 行为协议",
        "status": "初版具备",
        "summary": "定义 Agent 什么时候只回复、什么时候创建 run、什么时候确认、如何补充和局部修改。",
        "docs": [
            ("agent-behavior-contract.md", "行为边界"),
            ("main-agent-operations.md", "主 Agent 操作"),
            ("agent-startup-contract.md", "启动契约"),
            ("workflow-state.md", "审批与 run 状态"),
        ],
        "missing": ["router 实现对齐", "mock runtime 行为对齐", "Agent 行为测试"],
    },
    {
        "title": "08. Prompt / Tool / Rule 设计",
        "status": "初版具备",
        "summary": "定义每一步模型任务、输入输出、引用 rules 和 prompt 交接关系。",
        "docs": [
            ("runner-prompt-assembly-contract.md", "runner 拼装协议"),
            ("小说-prompts/step1_story_bible.md", "小说 Step1"),
            ("小说-prompts/step2_episode_split.md", "小说 Step2"),
            ("小说-prompts/step3_episode_cards.md", "小说 Step3"),
            ("非小说-prompts/step1_material_bank.md", "非小说 Step1"),
            ("非小说-prompts/step2_story_seed.md", "非小说 Step2"),
            ("非小说-prompts/step3_series_blueprint.md", "非小说 Step3"),
            ("非小说-prompts/step4_episode_cards.md", "非小说 Step4"),
            ("视频-prompts/step1_video_script_extract.md", "视频转高还原剧本"),
            ("prompts/script_generate.md", "公共剧本生成"),
            ("rules/shared/01_素材理解与故事圣经.md", "Shared Rule 01"),
            ("rules/shared/02_人物关系与声口.md", "Shared Rule 02"),
            ("rules/shared/03_结构规划_开头_冲突_爽点_尾钩.md", "Shared Rule 03"),
            ("rules/shared/04_剧本写作技法.md", "Shared Rule 04"),
            ("rules/shared/05_对白规则.md", "Shared Rule 05"),
            ("rules/shared/06_转场_闪回_连续性_格式.md", "Shared Rule 06"),
            ("rules/shared/07_小程序短剧适配.md", "Shared Rule 07"),
            ("rules/shared/08_示例库.md", "Shared Rule 08"),
            ("rules/shared/09_非小说素材与故事种子.md", "Shared Rule 09"),
            ("rules/video_to_script_extract/10_视频高还原剧本生成规则.md", "Video Rule 10"),
        ],
        "missing": ["runner 实现", "输出校验器", "Eino 节点实现", "prompt fixture 测试"],
    },
    {
        "title": "09. API / 事件协议",
        "status": "初版具备",
        "summary": "前后端正式开发最需要的协议层：接口、事件流、错误码、审批、文件。",
        "docs": [
            ("api-event-contract.md", "接口合同"),
            ("runtime-state-api-map.md", "状态到 API 落地清单"),
            ("workflow-state.md", "事件类型草案"),
            ("go-mock-runtime.md", "mock runtime"),
            ("local-file-attachments.md", "文件上传"),
        ],
        "missing": ["Go handler 对齐", "TypeScript client 对齐", "API 测试", "SSE 实现细节"],
    },
    {
        "title": "10. 模型与运行时策略",
        "status": "初版具备",
        "summary": "定义控制模型、生成模型、Eino runtime、上下文裁剪、失败兜底和 mock 切换。",
        "docs": [
            ("model-runtime-eino-contract.md", "Eino / 模型运行时合同"),
            ("main-agent-model-runtime.md", "模型运行时"),
            ("agent-startup-contract.md", "Eino 路线"),
            ("backend-implementation-plan.md", "Go 后端实现计划"),
            ("go-mock-runtime.md", "mock 阶段"),
        ],
        "missing": ["Eino SDK 实现细化", "runtime adapter 代码", "model call 测试", "上下文预算实测"],
    },
    {
        "title": "11. 权限、数据与安全",
        "status": "初版具备",
        "summary": "涉及文件、模型上下文、日志、密钥、用户数据保留与删除的安全边界。",
        "docs": [
            ("data-security-contract.md", "数据安全合同"),
            ("local-file-attachments.md", "文件上传局部说明"),
        ],
        "missing": ["后端权限校验实现", "日志脱敏实现", "删除实现", "生产账号/权限模型"],
    },
    {
        "title": "12. 验收用例与测试清单",
        "status": "初版具备",
        "summary": "把产品规则变成可执行测试：正常流、异常流、确认、暂停、上传、局部修改。",
        "docs": [
            ("acceptance-test-contract.md", "验收合同"),
            ("fixtures/script-editor-spike-fixture.json", "剧本编辑器 fixture"),
            ("product-review-checklist.md", "人工审查清单"),
            ("frontend-interaction-audit.md", "前端验收项"),
            ("go-mock-runtime.md", "mock 验证"),
        ],
        "missing": ["Playwright E2E 落地", "API 测试落地", "mock runtime 测试", "UI 状态回归"],
    },
    {
        "title": "历史参考 / 待归档",
        "status": "参考",
        "summary": "这些资料保留上下文，但不作为正式开发准入包主合同。",
        "docs": [
            ("frontend-visual-mock.html", "旧单状态视觉稿"),
            ("frontend-mock-shell.md", "旧前端壳记录"),
            ("agent-shell-wireframe.html", "早期线框稿"),
            ("agent-framework.html", "架构可视化参考"),
        ],
        "missing": [],
    },
]


def render() -> str:
    rendered_files: set[str] = set()
    nav_parts: list[str] = []
    main_parts: list[str] = []

    for idx, category in enumerate(CATEGORIES):
        category_id = "cat_" + slug(category["title"])
        nav_parts.append(f"<a class='cat-link' href='#{category_id}'>{html.escape(category['title'])}</a>")
        status = html.escape(category["status"])
        missing = category["missing"]
        missing_html = (
            "<ul>" + "".join(f"<li>{html.escape(item)}</li>" for item in missing) + "</ul>"
            if missing
            else "<p class='ok'>暂无关键缺口。</p>"
        )
        doc_items = "".join(
            f"<li>{doc_link(rel, role)}</li>"
            for rel, role in category["docs"]
        )
        main_parts.append(
            f"<section class='category' id='{category_id}'>"
            f"<div class='cat-head'><h2>{html.escape(category['title'])}</h2><span>{status}</span></div>"
            f"<p>{html.escape(category['summary'])}</p>"
            f"<h4>支撑文档</h4><ul>{doc_items}</ul>"
            f"<h4>缺口</h4>{missing_html}"
            f"</section>"
        )
        for rel, _ in category["docs"]:
            if rel in rendered_files:
                continue
            path = ROOT / rel
            if not path.exists():
                main_parts.append(
                    f"<section class='doc-section missing-doc' id='{file_id(rel)}'>"
                    f"<h3>{html.escape(rel)}</h3><p>文件不存在，需要补齐。</p></section>"
                )
                rendered_files.add(rel)
                continue
            if path.suffix.lower() == ".html":
                main_parts.append(render_html(rel))
            else:
                main_parts.append(render_markdown(rel))
            rendered_files.add(rel)

    page = f"""<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Novel2Script Development Readiness Preview</title>
<style>
:root {{
  --bg:#f6f7f9;
  --surface:#ffffff;
  --text:#111827;
  --muted:#667085;
  --line:#e5e7eb;
  --primary:#0f172a;
  --blue:#2563eb;
  --amber:#b45309;
}}
* {{ box-sizing:border-box; }}
body {{ margin:0; font-family:Segoe UI, Microsoft YaHei, Arial, sans-serif; background:var(--bg); color:var(--text); }}
.layout {{ display:grid; grid-template-columns:340px minmax(0,1fr); min-height:100vh; }}
nav {{ position:sticky; top:0; height:100vh; overflow:auto; background:var(--surface); border-right:1px solid var(--line); padding:18px; }}
nav h1 {{ margin:0 0 6px; font-size:18px; }}
nav p {{ margin:0 0 16px; color:var(--muted); font-size:12px; line-height:1.5; }}
.cat-link {{ display:block; padding:9px 10px; margin:4px 0; border-radius:8px; color:#1f2937; text-decoration:none; font-size:13px; line-height:1.35; }}
.cat-link:hover {{ background:#eef2ff; color:var(--blue); }}
main {{ padding:24px 34px 48px; overflow:auto; }}
section {{ background:var(--surface); border:1px solid var(--line); border-radius:10px; margin-bottom:18px; padding:18px; box-shadow:0 1px 2px rgba(16,24,40,.04); }}
.category {{ border-left:4px solid var(--primary); }}
.cat-head {{ display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:10px; }}
h2 {{ margin:0; font-size:22px; }}
h3 {{ margin:0 0 12px; font-size:18px; }}
h4 {{ margin:14px 0 8px; font-size:13px; color:#344054; }}
p, li {{ line-height:1.65; }}
ul {{ margin:0; padding-left:20px; }}
.cat-head span {{ display:inline-flex; align-items:center; min-height:26px; padding:0 10px; border-radius:999px; background:#f1f5f9; color:#475569; font-size:12px; white-space:nowrap; }}
.doc-link {{ color:var(--blue); text-decoration:none; }}
.doc-link:hover {{ text-decoration:underline; }}
.doc-link span {{ margin-left:8px; color:var(--muted); font-size:12px; }}
.doc-section {{ scroll-margin-top:20px; }}
pre {{ white-space:pre-wrap; word-break:break-word; font-family:Consolas, Microsoft YaHei, monospace; font-size:13px; line-height:1.65; margin:0; }}
iframe {{ width:100%; height:760px; border:1px solid var(--line); border-radius:10px; background:#fff; }}
.open-html {{ display:inline-block; margin-bottom:12px; color:var(--blue); text-decoration:none; font-weight:700; }}
.ok {{ color:#047857; }}
.missing-doc {{ border-color:#fed7aa; background:#fff7ed; }}
@media (max-width: 900px) {{
  .layout {{ grid-template-columns:1fr; }}
  nav {{ position:relative; height:auto; }}
  main {{ padding:16px; }}
}}
</style>
</head>
<body>
<div class="layout">
  <nav>
    <h1>开发准入包 Preview</h1>
    <p>按 AI 项目前后端开发前置 12 类文档组织。历史资料单独放在底部，不混入主合同。</p>
    {''.join(nav_parts)}
  </nav>
  <main>{''.join(main_parts)}</main>
</div>
</body>
</html>
"""
    return page


def main() -> int:
    output = ROOT / "preview.html"
    output.write_text(render(), encoding="utf-8")
    print(f"OK: rebuilt readiness preview at {output}")
    print(f"OK: categories: {len(CATEGORIES)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
