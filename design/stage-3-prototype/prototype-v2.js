(() => {
  "use strict";

  const projects = [
    {
      id: "novel",
      name: "凡骨问仙",
      scenario: "novel",
      skill: "小说转剧本",
      updated: "今天 09:42"
    },
    {
      id: "outline",
      name: "雾城夜谈",
      scenario: "nonNovel",
      skill: "非小说文本转剧本",
      updated: "昨天 18:16"
    },
    {
      id: "video",
      name: "穷剑修参考创作",
      scenario: "video",
      skill: "视频参考创作",
      updated: "今天 10:08"
    },
    {
      id: "candidate",
      name: "逆风长歌",
      scenario: "candidate",
      skill: "小说转剧本",
      updated: "7 月 28 日"
    }
  ];

  const projectState = {
    novel: {
      stage: "storyBible",
      status: "waiting",
      statusLabel: "等待确认",
      todo: "确认故事圣经",
      version: 2,
      candidateCount: 0,
      finalSelected: false,
      messages: [
        ["user", "把这本小说改成 12 集、每集 2 分钟的漫剧剧本。"],
        ["agent", "已读取小说原文并生成故事圣经。请先检查人物、冲突和主线，确认后再进行原文拆集。"]
      ]
    },
    outline: {
      stage: "completeness",
      status: "waiting",
      statusLabel: "等待确认",
      todo: "选择扩写策略",
      version: 1,
      candidateCount: 0,
      finalSelected: false,
      messages: [
        ["user", "根据这个故事大纲生成 24 集漫剧剧本。"],
        ["agent", "现有材料不足以直接支撑 24 集。我列出了缺口和扩写策略，需要先由你确认。"]
      ]
    },
    video: {
      stage: "videoBatch",
      status: "running",
      statusLabel: "解析中",
      todo: "已解析 7 / 9 集",
      runStatus: "running",
      videoFailed: true,
      videoRunning: true,
      incompleteConfirmed: false,
      version: 1,
      candidateCount: 0,
      finalSelected: false,
      messages: [
        ["user", "这些是同一部漫剧，先帮我逐集转成剧本。"],
        ["agent", "已收到 9 个视频。当前缺少第 6 集，第 9 集解析失败；系统不会自动判断已经上传完成。"]
      ]
    },
    candidate: {
      stage: "candidates",
      status: "done",
      statusLabel: "剧本已完成",
      todo: "选择最终稿",
      version: 18,
      candidateCount: 2,
      finalSelected: false,
      messages: [
        ["agent", "修订版已通过质量审核并形成 Candidate B。请选择当前最终稿，或继续查看和修改。"]
      ]
    }
  };

  const storageKey = "content-agent-stage3-prototype-v2";
  try {
    const saved = JSON.parse(window.localStorage.getItem(storageKey) || "null");
    if (saved?.projects?.length && saved?.projectState) {
      projects.splice(0, projects.length, ...saved.projects);
      Object.keys(projectState).forEach((key) => delete projectState[key]);
      Object.assign(projectState, saved.projectState);
    }
  } catch {
    window.localStorage.removeItem(storageKey);
  }

  const ui = {
    currentProjectId: null,
    currentView: null,
    lastFocus: null,
    menuTrigger: null,
    editingProjectId: null,
    selectedEpisode: 4
  };

  const chains = {
    novel: [
      ["source", "小说原文"],
      ["config", "生成配置"],
      ["storyBible", "故事圣经"],
      ["episodeSplit", "原文拆集"],
      ["episodeCards", "分集卡"],
      ["scripts", "分集剧本"],
      ["quality", "质量审核"],
      ["candidates", "候选稿"]
    ],
    nonNovel: [
      ["source", "故事大纲"],
      ["completeness", "材料完整度"],
      ["materialBank", "素材库"],
      ["storySeed", "故事种子"],
      ["blueprint", "整剧蓝图"],
      ["episodeCards", "分集卡"],
      ["scripts", "分集剧本"],
      ["quality", "质量审核"],
      ["candidates", "候选稿"]
    ],
    video: [
      ["videoBatch", "视频批次"],
      ["referenceScripts", "参考剧本"],
      ["analysis", "整剧分析"],
      ["adaptationOptions", "改编方案"],
      ["brief", "Adaptation Brief"],
      ["config", "新剧本配置"],
      ["storySeed", "故事种子"],
      ["blueprint", "整剧蓝图"],
      ["episodeCards", "分集卡"],
      ["scripts", "分集剧本"],
      ["quality", "质量审核"],
      ["candidates", "候选稿"]
    ],
    candidate: [
      ["source", "小说原文"],
      ["storyBible", "故事圣经"],
      ["episodeSplit", "原文拆集"],
      ["episodeCards", "分集卡"],
      ["scripts", "分集剧本"],
      ["quality", "质量审核"],
      ["candidates", "候选稿"],
      ["final", "当前最终稿"],
      ["versions", "版本历史"]
    ]
  };

  const els = {
    projectsView: document.getElementById("projects-view"),
    workbenchView: document.getElementById("workbench-view"),
    projectRows: document.getElementById("project-rows"),
    sidebar: document.getElementById("sidebar"),
    workspaceTitle: document.getElementById("workspace-title"),
    workspaceMeta: document.getElementById("workspace-meta"),
    workspaceActions: document.getElementById("workspace-actions"),
    workspaceBody: document.getElementById("workspace-body"),
    timeline: document.getElementById("timeline"),
    composerInput: document.getElementById("composer-input"),
    sendButton: document.getElementById("send-button"),
    skillButton: document.getElementById("skill-button"),
    attachButton: document.getElementById("attach-button"),
    skillMenu: document.getElementById("skill-menu"),
    menuRoot: document.getElementById("menu-root"),
    modalRoot: document.getElementById("modal-root"),
    toastRoot: document.getElementById("toast-root")
  };

  function currentProject() {
    return projects.find((project) => project.id === ui.currentProjectId) || null;
  }

  function currentState() {
    return projectState[ui.currentProjectId] || null;
  }

  function saveSnapshot() {
    window.localStorage.setItem(storageKey, JSON.stringify({ projects, projectState }));
  }

  function statusPill(label, kind = "") {
    return `<span class="status ${kind}">${label}</span>`;
  }

  function escapeHtml(value) {
    return String(value)
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;");
  }

  function closeMenu({ restoreFocus = true } = {}) {
    els.menuRoot.innerHTML = "";
    if (restoreFocus && ui.menuTrigger && document.contains(ui.menuTrigger)) {
      ui.menuTrigger.focus();
    }
    ui.menuTrigger = null;
  }

  function openProjectMenu(projectId, trigger) {
    const project = projects.find((item) => item.id === projectId);
    if (!project) return;
    closeMenu({ restoreFocus: false });
    ui.menuTrigger = trigger;
    const rect = trigger.getBoundingClientRect();
    const top = Math.min(rect.bottom + 4, window.innerHeight - 110);
    const left = Math.min(rect.right - 168, window.innerWidth - 176);
    els.menuRoot.innerHTML = `
      <div class="context-menu" role="menu" style="top:${top}px;left:${Math.max(8, left)}px">
        <button role="menuitem" data-menu-action="rename">重命名</button>
        <button role="menuitem" class="danger" data-menu-action="delete">删除作品</button>
      </div>
    `;
    const menu = els.menuRoot.querySelector(".context-menu");
    const items = [...menu.querySelectorAll('[role="menuitem"]')];
    items[0].focus();
    menu.addEventListener("keydown", (event) => {
      const index = items.indexOf(document.activeElement);
      if (event.key === "ArrowDown") {
        event.preventDefault();
        items[(index + 1) % items.length].focus();
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        items[(index - 1 + items.length) % items.length].focus();
      }
    });
    menu.querySelector('[data-menu-action="rename"]').addEventListener("click", () => {
      closeMenu({ restoreFocus: false });
      startInlineRename(projectId);
    });
    menu.querySelector('[data-menu-action="delete"]').addEventListener("click", () => {
      closeMenu({ restoreFocus: false });
      showDialog({
        title: `删除“${project.name}”？`,
        body: "删除后，该作品的材料、任务、候选稿和历史版本将从共享工作区移除。第一版不提供回收站。",
        confirmLabel: "删除作品",
        danger: true,
        trigger,
        onConfirm: () => {
          const index = projects.findIndex((item) => item.id === projectId);
          if (index >= 0) projects.splice(index, 1);
          delete projectState[projectId];
          renderProjectRows();
          showToast("作品已删除");
        }
      });
    });
  }

  function startInlineRename(projectId) {
    ui.editingProjectId = projectId;
    renderProjectRows();
    const input = els.projectRows.querySelector(`[data-rename-input="${projectId}"]`);
    if (input) {
      input.focus();
      input.select();
    }
  }

  function finishInlineRename(projectId, save) {
    const project = projects.find((item) => item.id === projectId);
    const input = els.projectRows.querySelector(`[data-rename-input="${projectId}"]`);
    if (save && project && input) {
      const nextName = input.value.trim();
      if (!nextName) {
        input.setAttribute("aria-invalid", "true");
        input.focus();
        showToast("作品名称不能为空", "warning");
        return;
      }
      project.name = nextName;
      project.updated = "刚刚";
      showToast("作品已重命名");
    }
    ui.editingProjectId = null;
    renderProjectRows();
  }

  function renderProjectRows() {
    saveSnapshot();
    const query = document.getElementById("project-search").value.trim().toLowerCase();
    const filter = document.getElementById("project-filter").value;
    const filtered = projects.filter((project) => {
      const state = projectState[project.id];
      const haystack = `${project.name}${project.skill}${state.todo}`.toLowerCase();
      return (!query || haystack.includes(query)) && (filter === "all" || state.status === filter);
    });

    if (!filtered.length) {
      els.projectRows.innerHTML = `
        <tr><td colspan="6"><div class="empty-state" style="min-height:220px">
          <div class="empty-state-inner"><h2>没有匹配的作品</h2><p class="muted">调整搜索词或状态筛选。</p></div>
        </div></td></tr>`;
      return;
    }

    els.projectRows.innerHTML = filtered.map((project) => {
      const state = projectState[project.id];
      const isEditing = ui.editingProjectId === project.id;
      const nameCell = isEditing
        ? `<div class="inline-rename">
             <label class="sr-only" for="rename-${project.id}">作品名称</label>
             <input id="rename-${project.id}" data-rename-input="${project.id}" value="${escapeHtml(project.name)}">
             <button data-rename-save="${project.id}" title="保存重命名">保存</button>
             <button data-rename-cancel="${project.id}" title="取消重命名">取消</button>
           </div>`
        : `<div class="project-name">${escapeHtml(project.name)}</div><div class="project-meta">共享作品</div>`;
      return `
        <tr data-project-id="${project.id}" tabindex="${isEditing ? "-1" : "0"}">
          <td>${nameCell}</td>
          <td>${statusPill(state.statusLabel, state.status)}</td>
          <td>${escapeHtml(project.skill)}</td>
          <td>${escapeHtml(state.todo)}</td>
          <td>${escapeHtml(project.updated)}</td>
          <td><button class="icon-button more-button" title="更多操作" aria-haspopup="menu" data-more="${project.id}">⋯</button></td>
        </tr>`;
    }).join("");

    els.projectRows.querySelectorAll("[data-project-id]").forEach((row) => {
      row.addEventListener("click", (event) => {
        if (event.target.closest("button,input") || ui.editingProjectId) return;
        openProject(row.dataset.projectId);
      });
      row.addEventListener("keydown", (event) => {
        if (event.key === "Enter" && !event.target.closest("button,input")) {
          openProject(row.dataset.projectId);
        }
      });
    });
    els.projectRows.querySelectorAll("[data-more]").forEach((button) => {
      button.addEventListener("click", (event) => {
        event.stopPropagation();
        openProjectMenu(button.dataset.more, button);
      });
    });
    els.projectRows.querySelectorAll("[data-rename-save]").forEach((button) => {
      button.addEventListener("click", () => finishInlineRename(button.dataset.renameSave, true));
    });
    els.projectRows.querySelectorAll("[data-rename-cancel]").forEach((button) => {
      button.addEventListener("click", () => finishInlineRename(button.dataset.renameCancel, false));
    });
    els.projectRows.querySelectorAll("[data-rename-input]").forEach((input) => {
      input.addEventListener("keydown", (event) => {
        if (event.key === "Enter") finishInlineRename(input.dataset.renameInput, true);
        if (event.key === "Escape") finishInlineRename(input.dataset.renameInput, false);
      });
    });
  }

  function showDialog({
    title,
    body,
    confirmLabel = "确认",
    cancelLabel = "取消",
    danger = false,
    trigger = document.activeElement,
    onConfirm = () => {},
    onCancel = () => {}
  }) {
    closeMenu({ restoreFocus: false });
    ui.lastFocus = trigger;
    els.modalRoot.innerHTML = `
      <div class="modal-backdrop ${danger ? "dialog-danger" : ""}">
        <div class="modal" role="dialog" aria-modal="true" aria-labelledby="modal-title" aria-describedby="modal-description">
          <div class="modal-header"><h2 id="modal-title">${escapeHtml(title)}</h2></div>
          <div class="modal-body" id="modal-description">${body}</div>
          <div class="modal-footer">
            <button id="modal-cancel">${escapeHtml(cancelLabel)}</button>
            <button id="modal-confirm" class="primary">${escapeHtml(confirmLabel)}</button>
          </div>
        </div>
      </div>`;

    const backdrop = els.modalRoot.querySelector(".modal-backdrop");
    const cancel = document.getElementById("modal-cancel");
    const confirm = document.getElementById("modal-confirm");
    const close = (confirmed) => {
      const dialog = backdrop.querySelector(".modal");
      els.modalRoot.innerHTML = "";
      if (ui.lastFocus && document.contains(ui.lastFocus)) ui.lastFocus.focus();
      confirmed ? onConfirm(dialog) : onCancel(dialog);
    };
    cancel.addEventListener("click", () => close(false));
    confirm.addEventListener("click", () => close(true));
    backdrop.addEventListener("mousedown", (event) => {
      if (event.target === backdrop) close(false);
    });
    backdrop.addEventListener("keydown", (event) => {
      if (event.key !== "Tab") return;
      const focusable = [...backdrop.querySelectorAll("button,input,textarea,select,[tabindex]:not([tabindex='-1'])")]
        .filter((element) => !element.disabled);
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    });
    cancel.focus();
  }

  function showToast(text, kind = "") {
    els.toastRoot.innerHTML = "";
    const toast = document.createElement("div");
    toast.className = `toast ${kind}`;
    toast.textContent = text;
    els.toastRoot.appendChild(toast);
    window.setTimeout(() => toast.remove(), 2200);
  }

  function openProject(projectId) {
    const project = projects.find((item) => item.id === projectId);
    const state = projectState[projectId];
    if (!project || !state) return;
    ui.currentProjectId = projectId;
    ui.currentView = state.stage;
    document.getElementById("project-title").textContent = project.name;
    els.projectsView.classList.add("hidden");
    els.workbenchView.classList.remove("hidden");
    renderWorkbench();
  }

  function renderWorkbench() {
    const project = currentProject();
    const state = currentState();
    if (!project || !state) return;
    saveSnapshot();
    document.getElementById("project-title").textContent = project.name;
    document.getElementById("header-skill").textContent = project.skill;
    document.getElementById("header-status").textContent = state.statusLabel;
    document.getElementById("header-status").className = `status ${state.status}`;
    renderSidebar();
    renderWorkspace();
    renderAgent();
    updateComposerState();
  }

  function stageIndex(scenario, stage) {
    return (chains[scenario] || []).findIndex(([id]) => id === stage);
  }

  function isViewUnlocked(scenario, viewId, state) {
    if (viewId === "source" || viewId === "versions") return true;
    if (scenario === "candidate") return viewId !== "final" || state.finalSelected;
    return stageIndex(scenario, viewId) <= stageIndex(scenario, state.stage);
  }

  function renderSidebar() {
    const project = currentProject();
    const state = currentState();
    if (project.scenario === "empty") {
      els.sidebar.innerHTML = `
        <div class="nav-group"><div class="nav-label">材料</div>
          <button class="nav-item active" data-view-id="empty"><span class="nav-state"></span><span class="nav-text">添加材料</span></button>
        </div>
        <div class="nav-group"><div class="nav-label">当前流程</div>
          <button class="nav-item locked" disabled title="引用 Skill 或由 Agent 识别意图后可用"><span class="nav-state"></span><span class="nav-text">尚未开始</span></button>
        </div>`;
      return;
    }

    const chain = chains[project.scenario] || [];
    const groups = project.scenario === "video"
      ? [["材料", chain.slice(0, 1)], ["参考内容", chain.slice(1, 5)], ["新剧本", chain.slice(5, 12)]]
      : [["材料", chain.slice(0, 1)], ["当前流程", chain.slice(1, -3)], ["剧本", chain.slice(-3, -1)], ["候选与最终稿", chain.slice(-1)]];

    if (project.scenario === "candidate") {
      groups.splice(0, groups.length,
        ["材料", chain.slice(0, 1)],
        ["当前流程", chain.slice(1, 4)],
        ["剧本", chain.slice(4, 6)],
        ["候选与最终稿", chain.slice(6, 8)],
        ["历史记录", chain.slice(8)]
      );
    } else {
      groups.push(["历史记录", [["versions", "版本历史"]]]);
    }

    els.sidebar.innerHTML = groups.map(([label, items]) => `
      <div class="nav-group">
        <div class="nav-label">${label}</div>
        ${items.map(([id, itemLabel]) => {
          const unlocked = isViewUnlocked(project.scenario, id, state);
          const current = ui.currentView === id;
          const complete = stageIndex(project.scenario, id) < stageIndex(project.scenario, state.stage);
          return `
            <button class="nav-item ${current ? "active" : ""} ${unlocked ? "" : "locked"}"
              data-view-id="${id}" ${unlocked ? "" : "disabled"}
              title="${unlocked ? itemLabel : "完成并确认前置步骤后可用"}">
              <span class="nav-state ${complete ? "done" : id === state.stage ? state.status : ""}"></span>
              <span class="nav-text">${itemLabel}</span>
              ${unlocked ? "" : '<span class="locked-reason">待前置步骤</span>'}
            </button>`;
        }).join("")}
      </div>`).join("");

    els.sidebar.querySelectorAll("[data-view-id]:not(:disabled)").forEach((button) => {
      button.addEventListener("click", () => {
        ui.currentView = button.dataset.viewId;
        renderWorkbench();
      });
    });
  }

  function setWorkspace(title, meta, body, actions = "") {
    els.workspaceTitle.textContent = title;
    els.workspaceMeta.textContent = meta;
    els.workspaceBody.innerHTML = `<div class="content-column">${body}</div>`;
    els.workspaceActions.innerHTML = actions;
    bindWorkspaceActions();
  }

  function renderWorkspace() {
    const renderers = {
      empty: renderEmpty,
      source: renderSource,
      config: renderConfig,
      completeness: renderCompleteness,
      storyBible: () => renderArtifact("故事圣经", "storyBible"),
      episodeSplit: () => renderArtifact("原文拆集", "episodeSplit"),
      materialBank: () => renderArtifact("素材库", "materialBank"),
      storySeed: () => renderArtifact("故事种子", "storySeed"),
      blueprint: () => renderArtifact("整剧蓝图", "blueprint"),
      episodeCards: () => renderArtifact("分集卡", "episodeCards"),
      videoBatch: renderVideoBatch,
      referenceScripts: renderReferenceScripts,
      analysis: renderAnalysis,
      adaptationOptions: renderAdaptationOptions,
      brief: renderBrief,
      scripts: renderScripts,
      quality: renderQuality,
      candidates: renderCandidates,
      final: renderFinal,
      versions: renderVersions
    };
    (renderers[ui.currentView] || renderUnavailable)();
  }

  function renderEmpty() {
    setWorkspace(
      "添加创作材料",
      "当前作品尚未启动 Skill",
      `<div class="empty-state"><div class="empty-state-inner">
        <div class="empty-mark">+</div>
        <h2>从材料或对话开始</h2>
        <p class="muted">添加小说、故事大纲、图片或视频。上传只保存材料，不自动启动生成任务。</p>
        <div class="inline-actions" style="justify-content:center">
          <button id="empty-upload">添加材料</button>
          <button id="empty-novel">引用小说转剧本</button>
        </div>
      </div></div>`
    );
  }

  function renderSource() {
    const project = currentProject();
    const sourceTitle = project.scenario === "video" ? "视频来源" : project.scenario === "nonNovel" ? "故事大纲" : "小说原文";
    setWorkspace(
      sourceTitle,
      "来源材料 · 已保存",
      `<div class="notice success"><strong>来源材料已保存</strong>上传材料本身不会启动 Run；后续产物引用当前来源版本。</div>
       <section class="section"><h2>${escapeHtml(project.name)}来源材料</h2>
       <p>${project.scenario === "nonNovel" ? "电台主播追查旧城停电事件，并在每次停电时听到十年前失踪者的声音。" : "少年在宗门试炼中发现残缺灵根可以吸收废弃法器中的灵力。"}</p>
       <p class="muted">TXT · 来源版本 v1 · 已保存</p></section>`,
      `<button id="source-info">查看来源信息</button><button id="replace-source">替换文件</button>`
    );
  }

  function renderConfig() {
    const state = currentState();
    setWorkspace(
      currentProject().scenario === "video" ? "新剧本配置" : "生成配置",
      `版本 ${state.version} · 等待确认`,
      `<div class="notice warning"><strong>启动生成前需要确认体量</strong>集数和单集时长会影响拆集密度及后续生成。</div>
       <section class="section"><div class="form-grid">
         <div class="field"><label class="field-label" for="episode-count">目标集数</label><input id="episode-count" type="number" min="1" value="${currentProject().scenario === "novel" ? 12 : 24}"></div>
         <div class="field"><label class="field-label" for="episode-duration">单集时长（分钟）</label><input id="episode-duration" type="number" min="1" value="2"></div>
       </div></section>`,
      `<button id="save-config">保存</button><button id="approve-current" class="primary">确认配置</button>`
    );
  }

  const artifactContent = {
    storyBible: ["核心故事", "陆沉以通过宗门试炼、进入内门并查清父亲旧案为主线。废器灵力是能力来源，也是身份暴露风险。"],
    episodeSplit: ["拆集原则", "前四集完成受辱、能力发现、首次试炼和当众复测；每集结尾保留明确动作钩子。"],
    materialBank: ["材料事实与新增内容", "材料事实：停电、失踪者声音、十年前地铁事故。新增内容：调查搭档和三次升级事件。"],
    storySeed: ["故事种子", "主角每次接近真相都会触发全城停电，而失踪者声音逐步指向主角家人。"],
    blueprint: ["整剧蓝图", "前 8 集建立规则，中 8 集反转调查对象，后 8 集回到地铁事故并完成身份揭示。"],
    episodeCards: ["分集卡", "第 1 集建立主角困境，第 2 集触发异常，第 3 集首次主动调查，第 4 集遭遇规则反噬。"]
  };

  function renderArtifact(title, viewId) {
    const state = currentState();
    const [sectionTitle, text] = artifactContent[viewId] || [title, "当前产物内容。"];
    setWorkspace(
      title,
      `版本 ${state.version} · 等待确认`,
      `<div class="notice warning"><strong>请确认后继续</strong>可以手动编辑、要求 Agent 修改或重新生成；三种动作都会形成可追踪版本。</div>
       <section class="section"><h2>${sectionTitle}</h2><div id="artifact-content" contenteditable="false">${text}</div></section>`,
      `<button id="edit-artifact">编辑</button><button id="ask-ai-edit">要求 Agent 修改</button><button id="regenerate-artifact">重新生成</button><button id="approve-current" class="primary">确认并继续</button>`
    );
  }

  function renderCompleteness() {
    setWorkspace(
      "材料完整度",
      "需要确认扩写策略",
      `<div class="notice warning"><strong>现有材料不足以直接支撑 24 集</strong>中段冲突、反派动机和人物关系缺少明确内容。</div>
       <section class="section"><h2>已明确</h2><ul class="bullet-list">
         <li>主角是调查旧城停电事件的电台主播。</li><li>每次停电都会出现同一个失踪者的声音。</li><li>结局回到十年前地铁事故。</li>
       </ul></section>
       <section class="section"><h2>选择扩写方式</h2><div class="option-list">
         <label class="option-card"><input type="radio" name="expand" value="full" checked><span><strong>补全 24 集</strong><br><span class="muted">保留主线，由 Agent 补全中段冲突和配角，不新增关键设定。</span></span></label>
         <label class="option-card"><input type="radio" name="expand" value="short"><span><strong>缩减为 12 集</strong><br><span class="muted">不新增主要人物。</span></span></label>
         <label class="option-card"><input type="radio" name="expand" value="more"><span><strong>先补充材料</strong><br><span class="muted">保持当前步骤，不启动生成。</span></span></label>
       </div></section>`,
      `<button id="supplement-material">补充材料</button><button id="approve-expansion" class="primary">确认扩写策略</button>`
    );
  }

  function videoRow(order, file, episode, duration, label, kind) {
    let action = `<button class="mini" data-video-action="view" data-episode="${order}">查看剧本</button>`;
    if (kind === "failed") action = `<button class="mini" data-video-action="retry" data-episode="${order}">重试</button>`;
    if (kind === "running") action = `<button class="mini" data-video-action="finish" data-episode="${order}">刷新状态</button>`;
    return `<tr data-video-row="${order}"><td>${order}</td><td>${file}</td><td>${episode}</td><td>${duration}</td><td>${statusPill(label, kind)}</td><td>${action}</td></tr>`;
  }

  function renderVideoBatch() {
    const state = currentState();
    const failedLabel = state.videoFailed ? ["解析失败", "failed"] : ["已完成", "done"];
    const runningLabel = state.videoRunning ? ["解析中 64%", "running"] : ["已完成", "done"];
    const completeCount = 9 - Number(state.videoFailed) - Number(state.videoRunning);
    setWorkspace(
      "视频批次",
      `9 个视频 · ${completeCount} 个已完成${state.videoFailed ? " · 1 个失败" : ""}${state.videoRunning ? " · 1 个解析中" : ""}`,
      `<div class="notice warning"><strong>检测到缺少第 6 集</strong>请继续上传，或在全部视频到齐后明确结束上传。</div>
       <div class="inline-actions" style="margin-bottom:12px">
         <button id="add-video">添加视频</button><button id="reorder-video">调整顺序</button><button id="seal-upload" class="primary">上传完成</button>
       </div>
       <table class="video-table"><thead><tr><th>顺序</th><th>文件名</th><th>集号</th><th>时长</th><th>解析状态</th><th>操作</th></tr></thead><tbody>
         ${videoRow(1, "穷剑修01.mov", "第 1 集", "01:47", "已完成", "done")}
         ${videoRow(2, "穷剑修02.mov", "第 2 集", "01:53", "已完成", "done")}
         ${videoRow(3, "穷剑修03.mov", "第 3 集", "02:04", "已完成", "done")}
         ${videoRow(4, "穷剑修04.mov", "第 4 集", "01:58", "已完成", "done")}
         ${videoRow(5, "穷剑修05.mov", "第 5 集", "01:51", "已完成", "done")}
         ${videoRow(6, "穷剑修07.mov", "第 7 集", "02:01", "已完成", "done")}
         ${videoRow(7, "穷剑修08.mov", "第 8 集", "01:49", "已完成", "done")}
         ${videoRow(8, "穷剑修09.mov", "第 9 集", "01:55", failedLabel[0], failedLabel[1])}
         ${videoRow(9, "穷剑修10.mov", "第 10 集", "02:08", runningLabel[0], runningLabel[1])}
       </tbody></table>
       <p class="prototype-footnote">视频源文件保存 7 天；剧本、分析和来源元数据随作品保留。</p>`
    );
  }

  function renderReferenceScripts() {
    const state = currentState();
    setWorkspace(
      "参考剧本",
      `版本 ${state.version} · 按不完整材料生成 · 等待统一确认`,
      `<div class="notice warning"><strong>材料完整度：缺少第 6 集</strong>不会自行补写第 5 集和第 7 集之间的内容。</div>
       <section class="section"><h2>单集剧本</h2><ul class="bullet-list">
         <li>第 1 集：陆尘在杂役房发现破剑能够吞噬灵石残渣。</li>
         <li>第 2 集：外门考核前夜，陆尘被逼交出报名灵石。</li>
         <li>第 3 集：陆尘用废剑完成基础剑招考核。</li>
         <li>第 4 集：执事质疑作弊，陆尘要求当众复测。</li>
         <li>第 5 集：剑碑异动，长老开始调查陆尘身份。</li>
       </ul></section>`,
      `<button id="edit-artifact">编辑单集</button><button id="regenerate-artifact">重新生成单集</button><button id="approve-current" class="primary">统一确认参考剧本</button>`
    );
  }

  function renderAnalysis() {
    setWorkspace(
      "整剧分析",
      "基于已确认参考剧本 · 缺少第 6 集",
      `<div class="notice warning"><strong>完整度说明</strong>第 5 集到第 7 集之间的动机衔接无法确认。</div>
       <section class="section"><h2>核心内容判断</h2><dl class="definition-grid">
         <dt>开场策略</dt><dd>极端贫穷与破剑异能形成身份反差。</dd>
         <dt>核心钩子</dt><dd>使用破剑会暴露主角与失踪剑主的关系。</dd>
         <dt>情绪路径</dt><dd>受辱、隐忍、低成本反击、规则质疑、身份悬念。</dd>
         <dt>证据缺口</dt><dd>第 6 集缺失，首次长线反派动机证据不足。</dd>
       </dl></section>`,
      `<button id="ask-ai-edit">要求 Agent 修改</button><button id="approve-current" class="primary">确认分析</button>`
    );
  }

  function renderAdaptationOptions() {
    setWorkspace(
      "改编方案",
      "选择一条主改编方向",
      `<div class="notice"><strong>参考规律不会直接照搬剧情表达</strong>第一版仅保留来源标记、禁止照搬规则和用户确认。</div>
       <section class="section"><div class="option-list">
         <label class="option-card"><input type="radio" name="adapt" checked><span><strong>现代都市异能</strong><br><span class="muted">保留“低成本能力 + 规则反杀”，更换世界观和人物关系。</span></span></label>
         <label class="option-card"><input type="radio" name="adapt"><span><strong>末世资源争夺</strong><br><span class="muted">保留稀缺资源困境，重构能力代价与冲突来源。</span></span></label>
       </div></section>`,
      `<button id="ask-ai-edit">让 Agent 提供其他方案</button><button id="approve-current" class="primary">确认改编方向</button>`
    );
  }

  function renderBrief() {
    setWorkspace(
      "Adaptation Brief",
      `版本 ${currentState().version} · 改编前必须确认`,
      `<div class="notice warning"><strong>确认后才能生成新剧本</strong>Brief 支持手动编辑和要求 Agent 修改。</div>
       <section class="section"><h2>新剧本方向</h2><dl class="definition-grid">
         <dt>世界观</dt><dd>近未来城市，废弃设备可以回收异常能量。</dd>
         <dt>主角</dt><dd>被维修工会排挤的青年维修师。</dd>
         <dt>保留规律</dt><dd>资源匮乏开场、低成本能力、规则内反击、身份长钩。</dd>
         <dt>禁止照搬</dt><dd>不复用原剧人物名、具体事件、台词和场景组合。</dd>
       </dl></section>`,
      `<button id="edit-artifact">编辑 Brief</button><button id="ask-ai-edit">要求 Agent 修改</button><button id="approve-current" class="primary">确认 Brief</button>`
    );
  }

  function renderScripts() {
    const state = currentState();
    setWorkspace(
      "分集剧本",
      `12 集 · 版本 ${state.version} · 修改后待统一确认`,
      `<div class="notice warning"><strong>第 ${ui.selectedEpisode} 集处于可编辑状态</strong>保存只形成新版本，不等于确认整个剧本。</div>
       <div class="episode-layout">
         <nav class="episode-rail" aria-label="剧集">${Array.from({ length: 12 }, (_, index) => {
           const episode = index + 1;
           return `<button class="episode-button ${episode === ui.selectedEpisode ? "active" : ""}" data-episode-select="${episode}">第 ${episode} 集</button>`;
         }).join("")}</nav>
         <article class="script-paper"><div id="script-editor" contenteditable="true" aria-label="第 ${ui.selectedEpisode} 集剧本正文">
           <strong>${ui.selectedEpisode}-1 剑宗演武场 外 日</strong><br>
           人物：陆沉、执事、外门弟子<br><br>
           △剑碑前围满弟子。执事将陆沉的木牌丢回石阶。<br><br>
           执事（冷声）：一个杂役，也配让剑碑为你再开一次？<br><br>
           陆沉：规矩写着，考核结果有异议，可以当众复测。<br><br>
           △陆沉捡起木牌，走到剑碑前。
         </div></article>
       </div>`,
      `<button id="save-script">保存</button><button id="ask-ai-edit">要求 Agent 修改</button><button id="regenerate-artifact">重新生成本集</button><button id="show-versions">查看版本</button><button id="approve-current" class="primary">统一确认剧本</button>`
    );
  }

  function renderQuality() {
    const state = currentState();
    const issues = state.qualityFixed ? 0 : 2;
    setWorkspace(
      "质量审核",
      issues ? "需要处理 · 2 个问题" : "复审通过",
      issues
        ? `<div class="notice danger"><strong>剧本暂未形成 Candidate</strong>修订后自动重新审核。</div>
           <section class="section"><h2>问题 1：人物知情范围冲突</h2><p>第 4 集提前说出第 7 集才获得的信息。</p><div class="inline-actions"><button data-quality-action="locate">定位到第 4 集</button><button class="primary" data-quality-action="fix">按建议修改</button></div></section>
           <section class="section"><h2>问题 2：尾钩重复</h2><p>第 8 集和第 9 集结尾缺少新的信息增量。</p><div class="inline-actions"><button data-quality-action="compare">查看两集</button><button class="primary" data-quality-action="fix-all">统一返工并复审</button></div></section>`
        : `<div class="notice success"><strong>质量审核已通过</strong>当前剧本精确版本将形成 Candidate，不会自动成为最终稿。</div>
           <section class="section"><h2>审核结果</h2><p>结构、连续性、格式和确认要求均通过。</p></section>`,
      issues ? "" : `<button id="create-candidate" class="primary">查看 Candidate</button>`
    );
  }

  function renderCandidates() {
    const state = currentState();
    const count = Math.max(state.candidateCount || 0, currentProject().scenario === "candidate" ? 2 : 1);
    setWorkspace(
      "候选稿",
      `${count} 个候选 · ${state.finalSelected ? "已选择当前最终稿" : "尚未选择最终稿"}`,
      `<div class="notice ${state.finalSelected ? "success" : "warning"}"><strong>${state.finalSelected ? "Candidate B 是当前最终稿" : "请选择当前最终剧本"}</strong>选择不会删除其他候选；改选仍需确认。</div>
       <table class="candidate-table"><thead><tr><th>候选</th><th>来源</th><th>状态</th><th>操作</th></tr></thead><tbody>
         <tr><td><strong>Candidate A</strong><br><span class="muted">完整分集剧本</span></td><td>${escapeHtml(currentProject().skill)}</td><td>${statusPill(state.finalSelected ? "历史候选" : "候选")}</td><td class="row-actions"><button class="mini" data-candidate-view="A">查看</button><button class="mini ${state.finalSelected ? "" : "primary"}" data-select-final="A">${state.finalSelected ? "改选为最终稿" : "设为最终稿"}</button></td></tr>
         ${count > 1 ? `<tr><td><strong>Candidate B</strong><br><span class="muted">修订版</span></td><td>修订 Run</td><td>${statusPill(state.finalSelected ? "当前最终稿" : "候选", state.finalSelected ? "done" : "waiting")}</td><td class="row-actions"><button class="mini" data-candidate-view="B">查看</button><button class="mini primary" data-select-final="B">${state.finalSelected ? "已选择" : "设为最终稿"}</button></td></tr>` : ""}
       </tbody></table>`,
      state.finalSelected ? `<button data-export="txt">导出 TXT</button><button data-export="docx">导出 DOCX</button>` : ""
    );
  }

  function renderFinal() {
    const state = currentState();
    if (!state.finalSelected) return renderUnavailable();
    setWorkspace(
      "当前最终稿",
      "已确认 · 历史版本保留",
      `<div class="notice success"><strong>当前最终稿已确认</strong>后续修改形成新修订版本，不覆盖当前已确认版本。</div>
       <section class="section"><h2>${escapeHtml(currentProject().name)}</h2><p>共 24 集，每集约 2 分钟。质量审核已通过。</p></section>`,
      `<button data-export="txt">导出 TXT</button><button data-export="docx">导出 DOCX</button><button id="start-revision">发起修订</button>`
    );
  }

  function renderVersions() {
    setWorkspace(
      "版本历史",
      "按时间倒序 · 不可覆盖",
      `<table class="candidate-table"><thead><tr><th>版本</th><th>变更</th><th>来源</th><th>时间</th></tr></thead><tbody>
        <tr><td>v${currentState().version}</td><td>当前版本</td><td>最近操作</td><td>刚刚</td></tr>
        <tr><td>v${Math.max(1, currentState().version - 1)}</td><td>上一确认版本</td><td>Skill 生成</td><td>昨天 17:22</td></tr>
        <tr><td>v1</td><td>首次生成</td><td>${escapeHtml(currentProject().skill)}</td><td>昨天 15:12</td></tr>
      </tbody></table>`
    );
  }

  function renderUnavailable() {
    setWorkspace(
      "尚未生成",
      "需要完成前置步骤",
      `<div class="empty-state"><div class="empty-state-inner"><div class="empty-mark">·</div><h2>当前内容尚不可用</h2><p class="muted">完成并确认前置步骤后会出现在这里。</p></div></div>`
    );
  }

  function addMessage(type, text) {
    const state = currentState();
    if (!state) return;
    state.messages.push([type, text]);
    renderAgent();
  }

  function renderAgent() {
    const state = currentState();
    const items = [...state.messages];
    if (state.runStatus === "running") items.push(["run", state.todo]);
    if (state.runStatus === "paused") items.push(["paused", "任务已暂停，可恢复或取消"]);
    if (state.status === "failed") items.push(["failed", "当前步骤失败，可从失败位置重试"]);
    if (state.status === "waiting") items.push(["approval", state.todo]);

    els.timeline.innerHTML = items.map(([type, text]) => {
      if (type === "run") return `<div class="run-strip"><strong>${escapeHtml(text)}</strong><div class="progress"><span style="width:78%"></span></div><div class="run-controls"><button id="pause-run" class="mini">暂停</button><button id="cancel-run" class="mini danger">取消</button></div></div>`;
      if (type === "paused") return `<div class="run-strip"><strong>${escapeHtml(text)}</strong><div class="run-controls"><button id="resume-run" class="mini primary">恢复</button><button id="cancel-run" class="mini danger">取消</button></div></div>`;
      if (type === "failed") return `<div class="run-strip"><strong>${escapeHtml(text)}</strong><div class="run-controls"><button id="retry-run" class="mini primary">重试</button></div></div>`;
      if (type === "approval") return `<div class="message"><div class="message-label">待处理</div>${escapeHtml(text)}</div>`;
      return `<div class="message ${type === "user" ? "user" : ""}"><div class="message-label">${type === "user" ? "你" : "Agent"}</div>${escapeHtml(text)}</div>`;
    }).join("");
    els.timeline.scrollTop = els.timeline.scrollHeight;
    document.getElementById("agent-state").textContent =
      state.runStatus === "running" ? "任务运行中" : state.runStatus === "paused" ? "任务已暂停" : "可以对话";

    document.getElementById("pause-run")?.addEventListener("click", () => {
      state.runStatus = "paused";
      state.status = "waiting";
      state.statusLabel = "已暂停";
      state.todo = "恢复或取消当前任务";
      renderWorkbench();
    });
    document.getElementById("resume-run")?.addEventListener("click", () => {
      state.runStatus = "running";
      state.status = "running";
      state.statusLabel = "运行中";
      state.todo = "继续逐集解析";
      renderWorkbench();
    });
    document.getElementById("cancel-run")?.addEventListener("click", (event) => {
      showDialog({
        title: "取消当前 Run？",
        body: "已经成功生成的单集和版本会保留，但取消后的 Run 不能恢复。",
        confirmLabel: "取消 Run",
        danger: true,
        trigger: event.currentTarget,
        onConfirm: () => {
          state.runStatus = "canceled";
          state.status = "waiting";
          state.statusLabel = "已取消";
          state.todo = "可以基于现有材料重新开始";
          state.messages.push(["agent", "当前 Run 已取消，已完成的产物仍然保留。"]);
          renderWorkbench();
        }
      });
    });
    document.getElementById("retry-run")?.addEventListener("click", () => {
      state.status = "running";
      state.statusLabel = "重试中";
      state.runStatus = "running";
      state.todo = "正在从失败位置重试";
      renderWorkbench();
    });
  }

  function updateComposerState() {
    const state = currentState();
    const locked = state.runStatus === "running" || state.runStatus === "paused";
    els.composerInput.disabled = locked;
    els.sendButton.disabled = locked;
    els.skillButton.disabled = locked;
    els.attachButton.disabled = locked;
    els.composerInput.placeholder = locked
      ? state.runStatus === "paused" ? "恢复或取消当前任务后可以继续输入" : "当前任务运行中，暂停或取消后可以继续输入"
      : "描述你想做什么";
  }

  function nextStage() {
    const project = currentProject();
    const state = currentState();
    const chain = chains[project.scenario] || [];
    const index = stageIndex(project.scenario, state.stage);
    const next = chain[index + 1]?.[0];
    if (!next) return;
    state.stage = next;
    ui.currentView = next;
    state.status = "waiting";
    state.statusLabel = "等待确认";
    state.todo = `确认${chain[index + 1][1]}`;
    state.version += 1;
    state.messages.push(["agent", `${chain[index][1]}已确认，${chain[index + 1][1]}已生成，请检查后继续。`]);
    renderWorkbench();
  }

  function showImpactThen(actionLabel, onConfirm) {
    const project = currentProject();
    const state = currentState();
    const chain = chains[project.scenario] || [];
    const index = stageIndex(project.scenario, ui.currentView);
    const generatedDownstream = chain.slice(index + 1, stageIndex(project.scenario, state.stage) + 1).map(([, label]) => label);
    if (!generatedDownstream.length) {
      onConfirm();
      return;
    }
    showDialog({
      title: `${actionLabel}会影响下游`,
      body: `<p>以下已生成产物将被标记为受影响：</p><ul class="impact-list">${generatedDownstream.map((label) => `<li>${label}</li>`).join("")}</ul><p>旧版本不会删除。</p>`,
      confirmLabel: actionLabel,
      onConfirm
    });
  }

  function bindWorkspaceActions() {
    document.getElementById("empty-upload")?.addEventListener("click", showMaterialDialog);
    document.getElementById("empty-novel")?.addEventListener("click", () => {
      els.composerInput.value = "@小说转剧本 ";
      els.composerInput.focus();
    });
    document.getElementById("source-info")?.addEventListener("click", () => {
      showDialog({ title: "来源信息", body: "<p>来源版本 v1，上传时间今天 09:12。后续版本通过 Artifact 依赖引用。</p>", confirmLabel: "知道了" });
    });
    document.getElementById("replace-source")?.addEventListener("click", () => {
      showDialog({
        title: "替换来源材料？",
        body: "<p>替换后，已生成产物不会消失，但会标记为依赖旧来源版本。</p>",
        confirmLabel: "选择新文件",
        onConfirm: () => showToast("已进入模拟文件选择")
      });
    });
    document.getElementById("save-config")?.addEventListener("click", () => showToast("配置草稿已保存"));
    document.getElementById("approve-current")?.addEventListener("click", nextStage);
    document.getElementById("edit-artifact")?.addEventListener("click", toggleArtifactEdit);
    document.getElementById("ask-ai-edit")?.addEventListener("click", () => {
      els.composerInput.value = `请修改当前${els.workspaceTitle.textContent}：`;
      els.composerInput.focus();
    });
    document.getElementById("regenerate-artifact")?.addEventListener("click", () => {
      showImpactThen("重新生成", () => {
        const state = currentState();
        state.version += 1;
        state.status = "waiting";
        state.statusLabel = "等待确认";
        showToast(`已生成版本 ${state.version}`);
        renderWorkbench();
      });
    });
    document.getElementById("supplement-material")?.addEventListener("click", showMaterialDialog);
    document.getElementById("approve-expansion")?.addEventListener("click", () => {
      const value = document.querySelector('input[name="expand"]:checked')?.value;
      if (value === "more") {
        showMaterialDialog();
        return;
      }
      nextStage();
    });
    document.getElementById("add-video")?.addEventListener("click", () => showToast("已添加一个模拟视频批次"));
    document.getElementById("reorder-video")?.addEventListener("click", () => showToast("已按文件名自然排序，可拖动进行人工调整"));
    document.getElementById("seal-upload")?.addEventListener("click", sealVideoUpload);
    document.querySelectorAll("[data-video-action]").forEach((button) => {
      button.addEventListener("click", () => handleVideoAction(button));
    });
    document.getElementById("save-script")?.addEventListener("click", () => {
      currentState().version += 1;
      showToast(`已保存为版本 ${currentState().version}，仍需统一确认`);
      renderWorkbench();
    });
    document.getElementById("show-versions")?.addEventListener("click", () => {
      ui.currentView = "versions";
      renderWorkbench();
    });
    document.querySelectorAll("[data-episode-select]").forEach((button) => {
      button.addEventListener("click", () => {
        ui.selectedEpisode = Number(button.dataset.episodeSelect);
        renderWorkbench();
      });
    });
    document.querySelectorAll("[data-quality-action]").forEach((button) => {
      button.addEventListener("click", () => handleQualityAction(button.dataset.qualityAction));
    });
    document.getElementById("create-candidate")?.addEventListener("click", () => {
      const state = currentState();
      state.candidateCount = Math.max(1, state.candidateCount);
      state.stage = "candidates";
      state.status = "done";
      state.statusLabel = "剧本已完成";
      state.todo = "选择最终稿";
      ui.currentView = "candidates";
      renderWorkbench();
    });
    document.querySelectorAll("[data-candidate-view]").forEach((button) => {
      button.addEventListener("click", () => showDialog({
        title: `Candidate ${button.dataset.candidateView}`,
        body: "<p>完整分集剧本预览。返回后候选选择状态不变。</p>",
        confirmLabel: "返回候选稿"
      }));
    });
    document.querySelectorAll("[data-select-final]").forEach((button) => {
      button.addEventListener("click", () => selectFinal(button.dataset.selectFinal, button));
    });
    document.querySelectorAll("[data-export]").forEach((button) => {
      button.addEventListener("click", () => showToast(`已准备 ${button.dataset.export.toUpperCase()} 导出`));
    });
    document.getElementById("start-revision")?.addEventListener("click", () => {
      currentState().stage = "scripts";
      currentState().status = "waiting";
      currentState().statusLabel = "修订中";
      currentState().todo = "统一确认修订剧本";
      ui.currentView = "scripts";
      renderWorkbench();
    });
  }

  function toggleArtifactEdit() {
    const content = document.getElementById("artifact-content");
    if (!content) {
      showToast("已进入单集编辑视图");
      return;
    }
    const editing = content.contentEditable === "true";
    if (!editing) {
      content.contentEditable = "true";
      content.focus();
      document.getElementById("edit-artifact").textContent = "保存";
      return;
    }
    content.contentEditable = "false";
    currentState().version += 1;
    showToast(`已保存为版本 ${currentState().version}，需要重新确认`);
    renderWorkbench();
  }

  function handleVideoAction(button) {
    const state = currentState();
    const action = button.dataset.videoAction;
    if (action === "retry") {
      state.videoFailed = false;
      state.messages.push(["agent", "第 9 集已从失败位置重试并完成，其他成功单集没有重做。"]);
      showToast("第 9 集重试成功");
      renderWorkbench();
    }
    if (action === "finish") {
      state.videoRunning = false;
      state.runStatus = null;
      state.status = "waiting";
      state.statusLabel = "等待上传完成";
      state.todo = "继续上传或确认上传完成";
      showToast("第 10 集解析完成");
      renderWorkbench();
    }
    if (action === "view") {
      showDialog({
        title: `第 ${button.dataset.episode} 集参考剧本`,
        body: "<p>单集剧本已保存，可在整剧统一确认前继续编辑或重新生成。</p>",
        confirmLabel: "返回列表"
      });
    }
  }

  function sealVideoUpload(event) {
    const state = currentState();
    const problems = ["缺少第 6 集"];
    if (state.videoFailed) problems.push("第 9 集解析失败");
    if (state.videoRunning) problems.push("第 10 集仍在解析");
    showDialog({
      title: "按当前材料结束上传？",
      body: `<p>${problems.join("；")}。</p><p>继续后会明确标记材料不完整，未完成或失败单集不会被当作已解析内容。</p>`,
      confirmLabel: "按不完整材料继续",
      trigger: event?.currentTarget,
      onConfirm: () => {
        state.incompleteConfirmed = true;
        state.runStatus = null;
        state.stage = "referenceScripts";
        state.status = "waiting";
        state.statusLabel = "等待确认";
        state.todo = "统一确认参考剧本";
        ui.currentView = "referenceScripts";
        state.messages.push(["agent", "已按不完整材料结束上传。参考剧本会持续标记缺少第 6 集。"]);
        renderWorkbench();
      }
    });
  }

  function handleQualityAction(action) {
    if (action === "locate") {
      ui.selectedEpisode = 4;
      ui.currentView = "scripts";
      renderWorkbench();
      return;
    }
    if (action === "compare") {
      showDialog({ title: "第 8、9 集尾钩对照", body: "<p>两集都以剑碑发光结束。第 9 集需要增加新的身份信息。</p>", confirmLabel: "返回审核" });
      return;
    }
    showDialog({
      title: "应用审核建议并自动复审？",
      body: "<p>修改会形成新剧本版本；旧审核结果失效，系统只重审受影响范围。</p>",
      confirmLabel: "修改并复审",
      onConfirm: () => {
        const state = currentState();
        state.version += 1;
        state.qualityFixed = true;
        showToast("返工完成，复审通过");
        renderWorkbench();
      }
    });
  }

  function selectFinal(candidate, trigger) {
    const state = currentState();
    const isChange = state.finalSelected && state.finalSelected !== candidate;
    showDialog({
      title: `${isChange ? "改选" : "选择"} Candidate ${candidate} 为最终稿？`,
      body: "<p>其他候选和历史最终版本会继续保留。导出始终使用当前选择的精确版本。</p>",
      confirmLabel: isChange ? "确认改选" : "确认设为最终稿",
      trigger,
      onConfirm: () => {
        state.finalSelected = candidate;
        state.todo = "可导出或发起修订";
        renderWorkbench();
      }
    });
  }

  function showMaterialDialog() {
    showDialog({
      title: "添加模拟材料",
      body: `<p>选择一种材料以验证通用上传入口。上传本身不会启动 Skill。</p>
        <div class="option-list">
          <label class="option-card"><input type="radio" name="material" value="novel" checked><span><strong>小说 TXT</strong><br><span class="muted">示例：长篇小说原文</span></span></label>
          <label class="option-card"><input type="radio" name="material" value="outline"><span><strong>故事大纲 DOCX</strong><br><span class="muted">示例：非小说文本</span></span></label>
          <label class="option-card"><input type="radio" name="material" value="image"><span><strong>参考图片 PNG</strong><br><span class="muted">意图不明确时由 Agent 追问</span></span></label>
          <label class="option-card"><input type="radio" name="material" value="video"><span><strong>漫剧视频 MOV</strong><br><span class="muted">示例：同一部剧的第一批视频</span></span></label>
        </div>`,
      confirmLabel: "添加材料",
      onConfirm: (dialog) => {
        const type = dialog.querySelector('input[name="material"]:checked')?.value || "novel";
        const state = currentState();
        state.uploadedType = type;
        state.messages.push(["agent", type === "image"
          ? "图片已保存。你希望我理解画面内容、提取文字，还是作为创作参考？"
          : "材料已保存。请描述目标或引用一个 Skill；上传不会自动启动 Run。"]);
        showToast("材料已保存，尚未启动 Skill");
        renderWorkbench();
      }
    });
  }

  function showNewProjectDialog(prefill = "", afterCreate = null) {
    showDialog({
      title: "新建作品",
      body: `<div class="field"><label class="field-label" for="new-project-name">作品名称</label><input id="new-project-name" value="${escapeHtml(prefill || "未命名作品")}"></div>
        <p class="muted">创建后进入空工作台。Skill 可以显式引用，也可以由 Agent 根据需求识别。</p>`,
      confirmLabel: "创建",
      onConfirm: (dialog) => {
        const input = dialog.querySelector("#new-project-name");
        const name = input?.value.trim() || prefill || "未命名作品";
        const id = `project-${Date.now()}`;
        projects.unshift({ id, name, scenario: "empty", skill: "尚未选择", updated: "刚刚" });
        projectState[id] = {
          stage: "empty",
          status: "waiting",
          statusLabel: "等待材料",
          todo: "添加材料或描述需求",
          version: 0,
          candidateCount: 0,
          finalSelected: false,
          messages: [["agent", prefill ? `我已记录你的需求：“${prefill}”。正式启动前会先确认材料和必要配置。` : "这是一个新作品。你可以上传材料，也可以直接描述目标。"]]
        };
        renderProjectRows();
        openProject(id);
        if (afterCreate) window.setTimeout(afterCreate, 0);
      }
    });
  }

  function routeComposerMessage(text) {
    const project = currentProject();
    const state = currentState();
    state.messages.push(["user", text]);
    if (project.scenario === "empty") {
      const requestedScenario = text.includes("视频参考创作") || text.includes("视频") ? "video"
        : text.includes("非小说文本转剧本") || text.includes("大纲") ? "nonNovel"
          : text.includes("小说转剧本") || text.includes("小说") ? "novel" : null;
      if (!requestedScenario) {
        state.messages.push(["agent", "你希望生成剧本、分析材料，还是仅理解当前内容？确认目标前我不会启动 Run。"]);
      } else if (!state.uploadedType) {
        state.messages.push(["agent", "已识别目标，但当前还没有可用材料。请先添加来源文件；引用 Skill 本身不会启动 Run。"]);
      } else {
        project.scenario = requestedScenario;
        project.skill = requestedScenario === "novel" ? "小说转剧本" : requestedScenario === "nonNovel" ? "非小说文本转剧本" : "视频参考创作";
        state.stage = requestedScenario === "novel" ? "config" : requestedScenario === "nonNovel" ? "completeness" : "videoBatch";
        state.status = requestedScenario === "video" ? "running" : "waiting";
        state.statusLabel = requestedScenario === "video" ? "解析中" : "等待确认";
        state.todo = requestedScenario === "novel" ? "确认生成配置" : requestedScenario === "nonNovel" ? "选择扩写策略" : "逐集解析中";
        state.runStatus = requestedScenario === "video" ? "running" : null;
        state.messages.push(["agent", `已选择${project.skill}。我先展示启动前必须确认的内容。`]);
        ui.currentView = state.stage;
      }
    } else {
      state.version += 1;
      state.status = "waiting";
      state.statusLabel = "等待确认";
      state.messages.push(["agent", `我会修改当前${els.workspaceTitle.textContent}并形成新版本，不会覆盖旧版本。请在中间区域检查后确认。`]);
    }
    els.composerInput.value = "";
    renderWorkbench();
  }

  document.getElementById("new-project-button").addEventListener("click", () => showNewProjectDialog());
  document.getElementById("quick-send").addEventListener("click", () => {
    const value = document.getElementById("quick-input").value.trim();
    if (!value) {
      document.getElementById("quick-input").focus();
      showToast("请先描述创作需求", "warning");
      return;
    }
    showNewProjectDialog(value.slice(0, 16));
  });
  document.getElementById("quick-upload").addEventListener("click", () => {
    showNewProjectDialog("新建创作作品", showMaterialDialog);
  });
  document.getElementById("project-search").addEventListener("input", renderProjectRows);
  document.getElementById("project-filter").addEventListener("change", renderProjectRows);
  document.getElementById("back-to-projects").addEventListener("click", () => {
    els.workbenchView.classList.add("hidden");
    els.projectsView.classList.remove("hidden");
    ui.currentProjectId = null;
    ui.currentView = null;
    renderProjectRows();
  });
  document.getElementById("candidate-shortcut").addEventListener("click", (event) => {
    const state = currentState();
    if (!state.candidateCount && currentProject().scenario !== "candidate") {
      showDialog({
        title: "还没有 Candidate",
        body: "<p>完成并确认剧本，且自动质量审核通过后才会形成 Candidate。</p>",
        confirmLabel: "知道了",
        trigger: event.currentTarget
      });
      return;
    }
    ui.currentView = "candidates";
    renderWorkbench();
  });
  document.getElementById("export-button").addEventListener("click", (event) => {
    const state = currentState();
    if (!state.finalSelected) {
      showDialog({
        title: "还没有当前最终稿",
        body: "<p>请先在候选稿中选择一个当前最终剧本，再导出 TXT 或 DOCX。</p>",
        confirmLabel: "查看候选稿",
        trigger: event.currentTarget,
        onConfirm: () => {
          if (!state.candidateCount && currentProject().scenario !== "candidate") {
            showToast("当前还没有 Candidate", "warning");
            return;
          }
          ui.currentView = "candidates";
          renderWorkbench();
        }
      });
      return;
    }
    closeMenu({ restoreFocus: false });
    const trigger = event.currentTarget;
    ui.menuTrigger = trigger;
    const rect = trigger.getBoundingClientRect();
    els.menuRoot.innerHTML = `
      <div class="context-menu" role="menu" style="top:${rect.bottom + 4}px;left:${Math.max(8, rect.right - 168)}px">
        <button role="menuitem" data-header-export="txt">导出 TXT</button>
        <button role="menuitem" data-header-export="docx">导出 DOCX</button>
      </div>`;
    const items = [...els.menuRoot.querySelectorAll("[data-header-export]")];
    items.forEach((button) => button.addEventListener("click", () => {
      const format = button.dataset.headerExport.toUpperCase();
      closeMenu();
      showToast(`已准备 ${format} 导出`);
    }));
    items[0]?.focus();
  });
  els.skillButton.addEventListener("click", () => {
    els.skillMenu.classList.toggle("hidden");
    if (!els.skillMenu.classList.contains("hidden")) els.skillMenu.querySelector("button")?.focus();
  });
  document.querySelectorAll("[data-skill]").forEach((button) => {
    button.addEventListener("click", () => {
      els.composerInput.value = `@${button.dataset.skill} `;
      els.skillMenu.classList.add("hidden");
      els.composerInput.focus();
    });
  });
  els.attachButton.addEventListener("click", showMaterialDialog);
  els.sendButton.addEventListener("click", () => {
    const text = els.composerInput.value.trim();
    if (text && !els.sendButton.disabled) routeComposerMessage(text);
  });
  els.composerInput.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) els.sendButton.click();
  });

  document.addEventListener("mousedown", (event) => {
    if (els.menuRoot.firstElementChild && !event.target.closest(".context-menu") && !event.target.closest("[data-more]")) {
      closeMenu();
    }
    if (!event.target.closest("#skill-menu") && !event.target.closest("#skill-button")) {
      els.skillMenu.classList.add("hidden");
    }
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    if (els.modalRoot.firstElementChild) {
      document.getElementById("modal-cancel")?.click();
      return;
    }
    if (els.menuRoot.firstElementChild) closeMenu();
    els.skillMenu.classList.add("hidden");
  });

  renderProjectRows();
})();
