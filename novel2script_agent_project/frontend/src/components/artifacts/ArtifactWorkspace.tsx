import { lazy, Suspense, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowDown, ArrowUp, ChevronDown, Download, ListTree, Plus, Save, Search, Trash2, X } from "lucide-react";
import { updateArtifact } from "../../api/client";
import { currentArtifactFromConflict } from "../../api/errors";
import type { Artifact, ArtifactType, ArtifactUpdateResponse, FileAttachment, RunEvent, SelectionContext, ViewMode } from "../../api/types";
import { artifactLabels, artifactStatusLabel, formatFileSize, isPrimaryRunEvent, labelEventMessage, labelEventType } from "../../lib/workbenchLabels";
import { manifestControlFor, manifestFallbackLabel } from "../../lib/artifactManifest";
import { downloadTextFile } from "../../lib/textDownload";
import { EmptyState } from "../ui/EmptyState";

const ScriptWorkbench = lazy(() => import("./ScriptWorkbench").then((module) => ({ default: module.ScriptWorkbench })));

const hiddenPresentationFields = new Set([
  "approval_id",
  "approval_request_id",
  "artifact_ref",
  "artifact_id",
  "attachment_id",
  "capability_id",
  "character_id",
  "chat_id",
  "config_version",
  "content_hash",
  "context_hash",
  "core_hook_ref",
  "created_at",
  "downstream_ref",
  "end_line_id",
  "end_scene_id",
  "file_id",
  "generation_config_ref",
  "generation_config_version",
  "line_id",
  "message_id",
  "node_id",
  "phase_id",
  "project_id",
  "requires_user_at",
  "run_id",
  "scene_id",
  "schema_version",
  "script_unit_artifact_id",
  "selection_hash",
  "source_ref",
  "source_ref_id",
  "source_unit_id",
  "start_line_id",
  "start_scene_id",
  "step_id",
  "task_id",
  "updated_at",
]);

interface ArtifactWorkspaceProps {
  artifacts: Artifact[];
  events: RunEvent[];
  inputText: string;
  latestByType: Map<ArtifactType, Artifact>;
  onArtifactUpdated?: (payload: ArtifactUpdateResponse) => void;
  onArtifactConflict?: (artifact: Artifact) => void;
  onArtifactSelect: (artifactType: ArtifactType) => void;
  onEditStateChange?: (state: { artifactId: string; artifactType: ArtifactType; editing: boolean; dirty: boolean } | null) => void;
  onEditSaveComplete?: (saved: boolean) => void;
  editSaveRequest?: number;
  editDiscardRequest?: number;
  onSelectionChange?: (selection: SelectionContext | null) => void;
  selectedArtifactType: ArtifactType | null;
  setInputText: (value: string) => void;
  sourcePreviewText: string;
  sourceFiles: FileAttachment[];
  visibleTypes: ArtifactType[];
  view: ViewMode;
  editingLocked?: boolean;
  exportLocked?: boolean;
  projectTitle?: string;
  targetEpisodeCount?: number;
}

const text = {
  sourceTitle: "输入材料",
  sourceHint: "这里只读预览已上传或已发送的材料；修改请通过 Agent 发起。",
  noSource: "尚未提供输入材料。",
  edit: "编辑内容",
  save: "保存",
  cancel: "取消",
  noArtifact: "还没有该类产物。",
  noEvents: "还没有运行记录。",
  emptyArray: "暂无内容",
  emptyObject: "暂无结构化内容",
  sectionHint: "可选中内容后，在右侧向 Agent 提修改要求。",
};

const fieldLabels: Record<string, string> = {
  adaptation_risks: "改编风险",
  angle: "切入角度",
  audience_hook: "受众钩子",
  background: "背景",
  beats: "情节节点",
  boundaries: "边界",
  boundary_check: "边界检查",
  boundary_detection_window_chars: "边界检测窗口字数",
  card_point_function: "本集功能",
  central_conflict: "中心冲突",
  character_arc: "人物弧光",
  character_state: "人物状态",
  character_turn: "人物转折",
  characters: "人物",
  climax_map: "高潮地图",
  conflict: "冲突",
  conflict_materials: "冲突素材",
  continuity_delta: "连续性变化",
  core_appeal: "核心看点",
  core_conflict: "核心冲突",
  core_event: "核心事件",
  core_question: "核心问题",
  dialogue_style: "对白风格",
  discard_or_later: "暂缓素材",
  duration_minutes: "时长分钟",
  emotional_drives: "情绪驱动力",
  emotional_payoff: "情绪回报",
  ending_hook: "尾钩",
  episode_duration_minutes: "单集分钟",
  episode_function: "本集作用",
  episode_id: "集数",
  episodes: "分集",
  event_summary: "事件摘要",
  evidence: "依据",
  existing_episode_markers_detected: "检测到原分集",
  family_state: "家庭状态",
  files: "文件",
  first_major_climax_candidate: "首个全剧高潮候选",
  fit_risks: "体量风险",
  gaps_and_questions: "缺口与追问",
  generated_additions: "模型新增内容",
  generation_config: "生成配置",
  global_risks: "全局风险",
  goal: "目标",
  hook: "钩子",
  hook_strength: "钩子强度",
  hook_type: "钩子类型",
  impact: "影响",
  information_density: "信息密度",
  key_events: "关键事件",
  line_id: "行 ID",
  location: "地点",
  main_conflict: "主要冲突",
  material_bank: "素材库",
  memory_points: "记忆点",
  must_keep_dialogue_or_moments: "必须保留的台词或瞬间",
  must_keep_facts: "必须保留的事实",
  name: "名称",
  opening_pressure: "开场压力",
  opening_state: "开场状态",
  pacing_plan: "节奏计划",
  payoff_or_reversal: "爽点或反转",
  plot: "剧情",
  premise: "前提",
  preserve_existing_episode_marks: "保留原分集",
  protagonist: "主角",
  recommended_position_note: "推荐位置说明",
  regional_speech: "地域口吻",
  relationship_position: "关系位置",
  risk_notes: "风险备注",
  risks: "风险",
  role: "角色",
  scene_id: "场景 ID",
  scene_no: "场次",
  scene_outline: "场景大纲",
  scene_plan: "场景规划",
  scenes: "场景",
  script_text: "剧本文本",
  script_units: "分集剧本",
  series_blueprint: "剧集蓝图",
  setting: "设定",
  source_basis: "来源依据",
  source_evidence: "来源证据",
  source_refs: "原文引用",
  source_summary: "原文摘要",
  source_text: "原始文本",
  source_trace: "来源追踪",
  source_unit_id: "来源单元 ID",
  speaker: "说话人",
  speech_profile: "声口",
  story_bible: "故事圣经",
  story_goal: "故事目标",
  story_outline: "故事梗概",
  story_seed: "故事种子",
  structure: "结构",
  summary: "摘要",
  target_episode_count: "目标集数",
  target_script_chars: "单集目标字数",
  target_source_chars_per_episode: "单集目标原文字数",
  text: "正文",
  theme: "主题",
  time_of_day: "时间",
  title: "标题",
  tone: "调性",
  usage_boundary: "使用边界",
  visual_strategy: "视觉策略",
  world_rules: "世界规则",
};

Object.assign(fieldLabels, {
  actual_episode_count: "实际集数",
  allowed_additions: "允许补充的内容",
  block_type: "内容类型",
  blocks: "剧本段落",
  character_motivation: "人物动机",
  character_progression: "人物成长线",
  closing_hook: "结尾钩子",
  core_premise: "核心故事前提",
  coverage_check: "原文覆盖检查",
  desire: "人物欲望",
  explicit_user_material: "用户明确提供的素材",
  forbidden_changes: "禁止改动的内容",
  global_continuity_state: "全局连续性状态",
  heading: "段落标题",
  identity: "人物身份",
  inferred_material: "推断补充的素材",
  main_characters: "主要人物",
  missing_information: "缺失信息",
  most_promising_direction: "最值得发展的方向",
  must_follow_facts: "必须遵守的事实",
  one_sentence_logline: "一句话梗概",
  opening_image: "开场画面",
  opposition: "阻力",
  payoff_distribution: "爽点分布",
  phase_plan: "阶段规划",
  quality_flags: "质量提示",
  recommended_episode_count: "建议集数",
  relationship_map: "人物关系图",
  relationship_state: "人物关系状态",
  reversal: "反转",
  self_check: "自检结果",
  series_promise: "全剧核心承诺",
  short_summary: "简短摘要",
  source_material: "来源素材",
  source_volume_assessment: "原文体量评估",
  stakes: "利害关系",
  task_id: "任务编号",
  turning_points: "关键转折",
  user_supplied_facts: "用户提供的事实",
  voice: "说话方式",
  weakness: "人物弱点",
});

Object.assign(fieldLabels, {
  action_summary: "动作摘要",
  change: "变化",
  character: "人物",
  character_a: "人物 A",
  character_b: "人物 B",
  compliance: "合规检查",
  core_hook: "核心钩子",
  core_props: "核心道具",
  current_timeline: "当前时间线",
  description: "说明",
  dialogue: "对白",
  dynamic: "关系动态",
  emotional_drive: "情绪驱动力",
  environment: "环境",
  foreshadowing: "伏笔",
  format: "格式",
  format_risks: "格式风险",
  function: "作用",
  general: "通用说明",
  locked_locations: "锁定场景地点",
  motivation: "动机",
  notes: "说明",
  open_hooks: "待回收钩子",
  pacing: "节奏",
  payoff: "回收",
  payoff_expectation: "回报预期",
  relation: "关系",
  relationship: "人物关系",
  relationship_type: "关系类型",
  resolved_hooks: "已回收钩子",
  risk_type: "风险类型",
  rule_name: "规则名称",
  scene_number: "场景序号",
  script_unit_artifact_id: "分集剧本产物编号",
  state: "状态",
  status: "状态",
  target_format: "目标格式",
  time: "时间",
  type: "类型",
  version: "版本",
  visual: "视觉表现",
  visual_style: "视觉风格",
  adaptation_added: "改编新增内容",
  adaptation_suggestions: "改编建议",
  applied_visual_strategy: "已采用的视觉策略",
  base_style: "基础口吻",
  basis: "依据",
  boundary_reason: "分集边界原因",
  can_support_target: "是否支撑目标体量",
  candidate_window_chars: "候选窗口字数",
  character_changes: "人物变化",
  character_state_changes: "人物状态变化",
  compression_candidates: "压缩候选",
  conflict_stage: "冲突阶段",
  continuity_risk: "连续性风险",
  continuity_rules: "连续性规则",
  continuity_state: "连续性状态",
  core_hook_refinement: "核心钩子优化",
  covered_source_ranges: "已覆盖原文范围",
  cut_after_anchor: "切点前锚点",
  cut_before_anchor: "切点后锚点",
  delete_or_deemphasize_candidates: "删除或弱化候选",
  detected_episode_markers: "检测到的分集标记",
  development_notes: "开发说明",
  dialogue_lines: "对白内容",
  duplicated_source_ranges: "重复覆盖的原文范围",
  effective_source_chars: "有效原文字数",
  emotional_drive_frontload: "情绪驱动力前置",
  end_anchor: "结束锚点",
  end_offset: "结束位置",
  episode_count: "总集数",
  episode_count_reason: "集数依据",
  episode_range: "集数范围",
  escalation_path: "冲突升级路径",
  events: "事件",
  expansion_strategy: "扩展策略",
  final_payoff_direction: "最终回报方向",
  first_major_climax_plan: "首个全剧高潮规划",
  flashback_or_memory_use: "闪回或回忆用法",
  foreshadowing_and_payoff: "伏笔与回收",
  foreshadowing_opened: "新增伏笔",
  foreshadowing_resolved: "已回收伏笔",
  from_material_bank: "来自素材库",
  from_series_blueprint: "来自剧集蓝图",
  from_source_text: "来自原文",
  from_story_bible: "来自故事圣经",
  from_story_seed: "来自故事种子",
  from_user_material: "来自用户素材",
  frontload_candidates: "前置候选",
  genre_tags: "类型标签",
  high_value_conflicts: "高价值冲突",
  hook_candidates: "钩子候选",
  hook_distribution: "钩子分布",
  hook_engine: "钩子机制",
  hook_or_suspense_potential: "钩子或悬念潜力",
  hook_source: "钩子来源",
  hook_strategy: "钩子策略",
  hook_text: "钩子内容",
  hooks_opened: "新增钩子",
  hooks_resolved: "已回收钩子",
  inferred: "推断内容",
  inferred_candidates: "推断候选",
  inner_need: "内在需求",
  input_type_tags: "输入类型标签",
  key_visual_moments: "关键视觉瞬间",
  logline: "一句话故事",
  main_emotional_drive: "主要情绪驱动力",
  main_plotline: "主线剧情",
  major_climax_candidates: "主要高潮候选",
  major_plotline: "主要剧情线",
  major_turn: "重大转折",
  manual_review_required: "需要人工复核",
  marker_policy: "分集标记策略",
  material_sufficiency: "素材充足度",
  merge_or_compress_candidates: "合并或压缩候选",
  missing_source_ranges: "遗漏的原文范围",
  model_inference: "模型推断",
  must_not_expand: "禁止扩写内容",
  name_or_role: "姓名或角色",
  new_facts: "新增事实",
  next_action: "下一步动作",
  next_episode_start: "下一集开头",
  opening_situation: "开局情境",
  order_issues: "顺序问题",
  pacing_density_plan: "节奏密度规划",
  pacing_execution_notes: "节奏执行说明",
  pacing_risk: "节奏风险",
  padding_risks: "注水风险",
  payoff_candidates: "爽点候选",
  payoff_chain: "爽点链路",
  payoff_focus: "爽点重点",
  phase_function: "阶段作用",
  phase_id: "阶段编号",
  preserve_candidates: "保留候选",
  pressure: "压力",
  previous_episode_end: "上一集结尾",
  psychology_to_scene_candidates: "心理活动场景化候选",
  qa_or_action_split_risk: "问答或动作被截断风险",
  questions: "待确认问题",
  recommended_episode_range: "建议集数区间",
  refs: "引用",
  relationship_changes: "人物关系变化",
  relationship_engine: "关系驱动机制",
  relationship_progression: "关系发展线",
  relationships: "人物关系",
  requires_user_attention: "需要用户关注",
  resolved_episode_count: "最终集数",
  risk: "风险说明",
  risk_flags: "风险标记",
  selling_points: "卖点",
  short_drama_assets: "短剧化资产",
  sound_or_object_triggers: "声音或物件触发点",
  source_chars: "原文字数",
  source_mode: "素材类型",
  source_range: "原文范围",
  source_structure: "原文结构",
  split_confidence: "拆集置信度",
  split_strategy: "拆集策略",
  start_anchor: "开始锚点",
  start_offset: "开始位置",
  story_overview: "故事概览",
  style_constraints: "风格约束",
  supporting_basis: "支撑依据",
  used_adaptation_suggestions: "已采用的改编建议",
  used_generated_additions: "已采用的模型新增内容",
  user_notes: "用户说明",
  visual_scene_candidates: "视觉场景候选",
  visualization_candidates: "视觉化候选",
  volume_fit_notes: "体量适配说明",
  volume_plan_notes: "体量规划说明",
  weak_episode_handling: "弱集处理方式",
  weak_episode_reason: "弱集原因",
  why_it_can_hold_first_card: "适合作为首个卡点的原因",
});

const preferredOrder: Record<ArtifactType, string[]> = {
  source_input: ["attachments", "files", "source_text", "text"],
  story_bible: ["story_goal", "summary", "core_conflict", "characters", "climax_map", "emotional_payoff", "adaptation_risks", "risk_notes", "source_evidence"],
  episode_split: ["generation_config", "source_volume_assessment", "episodes", "coverage_check", "global_risks", "risk_notes"],
  material_bank: ["generation_config", "user_supplied_facts", "emotional_drives", "conflict_materials", "gaps_and_questions", "discard_or_later", "risks", "source_trace"],
  story_seed: ["generation_config", "logline", "core_premise", "protagonist", "main_characters", "central_conflict", "risks", "source_trace"],
  series_blueprint: ["generation_config", "series_promise", "phase_plan", "payoff_distribution", "character_progression", "fit_risks", "source_trace"],
  episode_cards: ["generation_config", "episodes", "continuity_delta", "global_risks", "risk_notes", "source_evidence"],
  script_context: ["generation_config", "must_follow_facts", "allowed_additions", "forbidden_changes", "character_state", "relationship_state", "continuity_state", "source_material"],
  script_unit: ["episode_id", "title", "scenes", "script_text", "source_trace"],
  scripts: ["script_units", "episodes", "script_text", "source_trace"],
};

export function ArtifactWorkspace(props: ArtifactWorkspaceProps) {
  if (props.view === "events") return <EventsPanel events={props.events} />;

  if (props.view === "script") {
    return (
	  <Suspense fallback={<EmptyState>正在打开剧本编辑器...</EmptyState>}>
      <ScriptWorkbench
        artifacts={props.artifacts}
        latestByType={props.latestByType}
        onArtifactUpdated={props.onArtifactUpdated}
        onArtifactConflict={props.onArtifactConflict}
        onEditSaveComplete={props.onEditSaveComplete}
        onEditStateChange={props.onEditStateChange}
        onSelectionChange={props.onSelectionChange}
        editingLocked={props.editingLocked}
        exportLocked={props.exportLocked}
        projectTitle={props.projectTitle}
        targetEpisodeCount={props.targetEpisodeCount}
      />
	  </Suspense>
    );
  }

  const availableTypes = props.visibleTypes.filter((type) => props.latestByType.has(type));
  const activeType = props.selectedArtifactType || availableTypes[0] || "source_input";

  if (activeType === "source_input") {
    return (
      <section className="artifact-focus-view">
        <SourceInputPanel
          inputText={props.inputText}
          latestByType={props.latestByType}
          sourceFiles={props.sourceFiles}
          sourcePreviewText={props.sourcePreviewText}
        />
      </section>
    );
  }

  const filtered = [props.latestByType.get(activeType)].filter((artifact): artifact is Artifact => Boolean(artifact));

  if (!filtered.length) return <EmptyState>{text.noArtifact}</EmptyState>;

  return (
    <section className="artifact-focus-view">
      <section className="artifact-stack">
        {filtered.map((artifact) => (
          <ArtifactCard
            artifact={artifact}
            previousArtifact={latestPreviousArtifact(props.artifacts, artifact)}
            discardRequest={props.editDiscardRequest || 0}
            key={artifact.artifact_type}
            onEditSaveComplete={props.onEditSaveComplete}
            onEditStateChange={props.onEditStateChange}
            onSelectionChange={props.onSelectionChange}
            onUpdated={props.onArtifactUpdated}
            onConflict={props.onArtifactConflict}
            saveRequest={props.editSaveRequest || 0}
            editingLocked={props.editingLocked}
            exportLocked={props.exportLocked}
            projectTitle={props.projectTitle}
          />
        ))}
      </section>
    </section>
  );
}

function SourceInputPanel({
  inputText,
  latestByType,
  sourceFiles,
  sourcePreviewText,
}: {
  inputText: string;
  latestByType: Map<ArtifactType, Artifact>;
  sourceFiles: FileAttachment[];
  sourcePreviewText: string;
}) {
  const sourceInput = latestByType.get("source_input");
  const artifactFiles = readArray(sourceInput?.payload?.attachments || sourceInput?.payload?.files);
  const localFiles = sourceFiles.map((file) => ({
    file_name: file.file_name,
    mime_type: file.mime_type,
    size: file.size,
    text_preview: file.text_content,
  }));
  const rawSourceText =
    readString(sourceInput?.payload?.text) ||
    readString(sourceInput?.payload?.source_text) ||
    sourcePreviewText ||
    inputText ||
    sourceTextFromFiles(artifactFiles.length ? artifactFiles : localFiles);
  const sourcePresentation = parseSourcePresentation(rawSourceText, readObject(sourceInput?.payload));
  const files = artifactFiles.length ? artifactFiles : localFiles.length ? localFiles : sourcePresentation.legacyFiles;
  const sourceText = sourcePresentation.text || sourceTextFromFiles(files);
  return (
    <div className="source-preview-shell">
      <section className="source-preview-layout">
        <section className="source-preview-card source-preview-text">
          <header className="source-preview-head">
            <div>
              <h2>{text.sourceTitle}</h2>
              <p>{text.sourceHint}</p>
            </div>
          </header>
          {files.length ? <FileList files={files} /> : null}
          <pre>{sourceText || text.noSource}</pre>
        </section>
      </section>
    </div>
  );
}

function parseSourcePresentation(value: string, payload: Record<string, unknown>) {
  const lines = value.replace(/\r\n/g, "\n").split("\n");
  let cursor = 0;
  const skipBlankLines = () => {
    while (cursor < lines.length && !lines[cursor].trim()) cursor += 1;
  };
  skipBlankLines();

  const hasGenerationReference = Object.keys(readObject(payload.generation_config_ref)).length > 0 || Object.keys(readObject(payload.generation_config)).length > 0;
  if (hasGenerationReference && /^确认生成配置\s*[：:]/.test(lines[cursor] || "")) {
    cursor += 1;
    skipBlankLines();
  }

  const legacyFiles: unknown[] = [];
  while (cursor < lines.length) {
    const match = lines[cursor].trim().match(/^【附件\s*[：:]\s*(.+?)】$/);
    if (!match) break;
    legacyFiles.push({ file_name: match[1] });
    cursor += 1;
    skipBlankLines();
  }

  return { legacyFiles, text: lines.slice(cursor).join("\n").trimStart() };
}

function FileList({ files }: { files: unknown[] }) {
  return (
    <div className="source-file-list">
      {files.map((file, index) => {
        const item = readObject(file);
        const fileName = readString(item.file_name) || readString(item.name) || `文件 ${index + 1}`;
        const size = Number(item.size ?? item.size_bytes ?? 0);
        return (
          <div className="source-file-row" key={`${fileName}:${index}`}>
            <strong>{fileName}</strong>
            {size > 0 ? <span>{formatFileSize(size)}</span> : null}
          </div>
        );
      })}
    </div>
  );
}

function ArtifactCard({
  artifact,
  previousArtifact,
  onSelectionChange,
  onUpdated,
  onConflict,
  onEditStateChange,
  onEditSaveComplete,
  saveRequest,
  discardRequest,
  editingLocked = false,
  exportLocked = false,
  projectTitle = "未命名作品",
}: {
  artifact: Artifact;
  previousArtifact?: Artifact;
  onSelectionChange?: (selection: SelectionContext | null) => void;
  onUpdated?: (payload: ArtifactUpdateResponse) => void;
  onConflict?: (artifact: Artifact) => void;
  onEditStateChange?: (state: { artifactId: string; artifactType: ArtifactType; editing: boolean; dirty: boolean } | null) => void;
  onEditSaveComplete?: (saved: boolean) => void;
  saveRequest: number;
  discardRequest: number;
  editingLocked?: boolean;
  exportLocked?: boolean;
  projectTitle?: string;
}) {
  const [editing, setEditing] = useState(false);
  const [draftPayload, setDraftPayload] = useState<Record<string, unknown>>(() => clonePayload(artifact.payload));
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [saveNotice, setSaveNotice] = useState("");
  const lastSaveRequest = useRef(saveRequest);
  const lastDiscardRequest = useRef(discardRequest);
  const title = artifactLabels[artifact.artifact_type] || "过程产物";
  const status = artifactStatusLabel(artifact.status);
  const dirty = editing && !sameValue(draftPayload, artifact.payload);
  const exportDisabled = editing || saving || exportLocked;

  useEffect(() => {
    if (!editing) setDraftPayload(clonePayload(artifact.payload));
  }, [artifact.payload, editing]);

  useEffect(() => {
    onEditStateChange?.(editing ? { artifactId: artifact.artifact_id, artifactType: artifact.artifact_type, editing: true, dirty } : null);
  }, [artifact.artifact_id, artifact.artifact_type, dirty, editing, onEditStateChange]);

  useEffect(() => {
    if (lastSaveRequest.current === saveRequest) return;
    lastSaveRequest.current = saveRequest;
    if (editing && dirty) void save();
  }, [saveRequest]);

  useEffect(() => {
    if (lastDiscardRequest.current === discardRequest) return;
    lastDiscardRequest.current = discardRequest;
    if (editing) cancelEditing();
  }, [discardRequest]);

  async function save() {
	if (editingLocked) {
	  setError("当前正在生成下游内容，请先暂停流程，或等待生成完成后再编辑。");
	  return;
	}
    setError("");
    setSaving(true);
    try {
      const response = await updateArtifact(artifact.artifact_id, { base_version: artifact.version, payload: draftPayload });
      setSaveNotice(`已保存为 v${response.artifact.version}`);
      setEditing(false);
      onEditStateChange?.(null);
      onUpdated?.(response);
      onEditSaveComplete?.(true);
    } catch (saveError) {
      const currentArtifact = currentArtifactFromConflict(saveError);
      if (currentArtifact) {
        setEditing(false);
        onEditStateChange?.(null);
        onConflict?.(currentArtifact);
      }
      setError(saveError instanceof Error ? saveError.message : String(saveError));
      onEditSaveComplete?.(false);
    } finally {
      setSaving(false);
    }
  }

  function cancelEditing() {
    setDraftPayload(clonePayload(artifact.payload));
    setEditing(false);
    setError("");
    onEditStateChange?.(null);
  }

  function emitReadableSelection() {
    if (!onSelectionChange || editing) return;
    const selection = window.getSelection();
    const selectedText = selection?.toString().trim() || "";
    if (!selection || !selectedText) {
      onSelectionChange(null);
      return;
    }
    const anchorNode = selection.anchorNode;
    const focusNode = selection.focusNode;
    const anchorElement = anchorNode instanceof Element ? anchorNode : anchorNode?.parentElement;
    const focusElement = focusNode instanceof Element ? focusNode : focusNode?.parentElement;
    const host = anchorElement?.closest<HTMLElement>("[data-artifact-id]") || focusElement?.closest<HTMLElement>("[data-artifact-id]");
    if (host?.dataset.artifactId !== artifact.artifact_id) {
      onSelectionChange(null);
      return;
    }
    const fieldElement = anchorElement?.closest<HTMLElement>("[data-field-path]") || focusElement?.closest<HTMLElement>("[data-field-path]");
    const fullText = host.innerText || "";
    const start = fullText.indexOf(selectedText);
    const end = start >= 0 ? start + selectedText.length : undefined;
    onSelectionChange({
      artifact_id: artifact.artifact_id,
      artifact_type: artifact.artifact_type,
      version: artifact.version,
      field_path: fieldElement?.dataset.fieldPath,
      selected_text: selectedText,
      selection_start: start >= 0 ? start : undefined,
      selection_end: end,
      before_context: start > 0 ? fullText.slice(Math.max(0, start - 120), start) : "",
      after_context: end !== undefined ? fullText.slice(end, end + 120) : "",
      selection_source: "artifact_payload_view",
    });
  }

  return (
    <article className="artifact-card">
      <header className={`artifact-card-head ${editing ? "editing" : ""}`}>
        <div className="artifact-title-block">
          <h2>{title}</h2>
          <div className="artifact-meta-line">
            <span>v{artifact.version}</span>
            <span className={`artifact-status-badge ${statusClass(artifact.status)}`}>{status}</span>
            {dirty ? <span className="artifact-dirty-indicator">有未保存修改</span> : null}
            {!editing && saveNotice ? <span className="artifact-save-notice" aria-live="polite">{saveNotice}</span> : null}
          </div>
        </div>
        {editing ? (
          <div className="artifact-actions">
            <button disabled={saving || !dirty} onClick={save} type="button">
              <Save size={14} />
              {text.save}
            </button>
            <button
              disabled={saving}
              onClick={cancelEditing}
              type="button"
            >
              <X size={14} />
              {text.cancel}
            </button>
          </div>
        ) : (
          <div className="artifact-actions">
            <button
              aria-label={`导出${title}`}
              className="artifact-export-button"
              disabled={exportDisabled}
              onClick={() => downloadTextFile(`${projectTitle}_${title}`, formatArtifactText(artifact))}
              title={exportLocked ? "当前有内容正在生成或修改，完成后可导出" : "导出为 TXT"}
              type="button"
            >
              <Download size={14} />
              导出
            </button>
            <button className="artifact-json-button" aria-label={`${text.edit}：${title}`} disabled={editingLocked} onClick={() => setEditing(true)} title={editingLocked ? "生成期间暂不可编辑" : undefined} type="button">
              {text.edit}
            </button>
          </div>
        )}
      </header>
	  {editingLocked ? <p className="artifact-edit-lock" role="status">当前正在生成下游内容，完成或暂停后可编辑。</p> : null}

      <div
        aria-live="polite"
        className={`artifact-readable ${editing ? "editing" : ""}`}
        data-artifact-id={artifact.artifact_id}
        onKeyUp={editing ? undefined : emitReadableSelection}
        onMouseUp={editing ? undefined : emitReadableSelection}
      >
        <ArtifactPayloadView
          artifact={artifact}
          editing={editing}
          onChange={setDraftPayload}
          originalPayload={readObject(artifact.payload)}
          payload={editing ? draftPayload : readObject(artifact.payload)}
          previousArtifact={previousArtifact}
        />
      </div>
      {error ? <p className="form-error">{error}</p> : null}
    </article>
  );
}

function ArtifactPayloadView({
  artifact,
  editing,
  onChange,
  originalPayload,
  payload,
  previousArtifact,
}: {
  artifact: Artifact;
  editing: boolean;
  onChange: (value: Record<string, unknown>) => void;
  originalPayload: Record<string, unknown>;
  payload: Record<string, unknown>;
  previousArtifact?: Artifact;
}) {
  const entries = orderEntries(artifact.artifact_type, presentationEntries(payload));
  const [query, setQuery] = useState("");
  const [onlyContent, setOnlyContent] = useState(true);
  const [outlineOpen, setOutlineOpen] = useState(false);
  const sectionListRef = useRef<HTMLDivElement | null>(null);
  const [expandedSections, setExpandedSections] = useState<Set<string>>(
    () => new Set(entries.filter(([, value]) => !isEmptyValue(value)).slice(0, 3).map(([key]) => key)),
  );
  if (!entries.length) return <span className="muted">{text.emptyObject}</span>;
  const normalizedQuery = query.trim().toLowerCase();
  const visibleEntries = entries.filter(([key, value]) => (!onlyContent || !isEmptyValue(value)) && (!normalizedQuery || `${labelKey(key)} ${key} ${JSON.stringify(value)}`.toLowerCase().includes(normalizedQuery)));
  const changedKeys = new Set(entries.filter(([key, value]) => editing
    ? !sameValue(value, originalPayload[key])
    : previousArtifact && !sameValue(value, previousArtifact.payload[key])).map(([key]) => key));

  return (
    <div className="artifact-reader-layout">
      <button aria-controls={`artifact-outline-${artifact.artifact_id}`} aria-expanded={outlineOpen} className="artifact-outline-toggle" onClick={() => setOutlineOpen((current) => !current)} type="button">
        <ListTree size={15} />
        <span>产物大纲</span>
        <ChevronDown size={15} />
      </button>
      <nav className={`artifact-reader-outline ${outlineOpen ? "open" : ""}`} id={`artifact-outline-${artifact.artifact_id}`} aria-label="产物大纲">
        <label className="artifact-search"><Search size={14} /><input aria-label="搜索产物内容" onChange={(event) => setQuery(event.currentTarget.value)} placeholder="搜索内容" value={query} /></label>
        <label className="artifact-content-filter"><input checked={onlyContent} onChange={(event) => setOnlyContent(event.currentTarget.checked)} type="checkbox" />仅看有内容</label>
        {visibleEntries.map(([key]) => <button className={changedKeys.has(key) ? "changed" : ""} key={key} onClick={() => {
          const section = document.getElementById(`artifact-section-${artifact.artifact_id}-${key}`);
          if (section && sectionListRef.current) sectionListRef.current.scrollTo({ top: section.offsetTop, behavior: "smooth" });
          setOutlineOpen(false);
        }} type="button"><span>{labelKey(key)}</span>{changedKeys.has(key) ? <small>{editing ? "已修改" : "已更新"}</small> : null}</button>)}
      </nav>
      <div className="artifact-section-list" ref={sectionListRef}>
      {visibleEntries.map(([key, value]) => (
        <details
          className={`artifact-section ${editing ? "editing" : ""} ${changedKeys.has(key) ? "modified" : ""} ${isEmptyValue(value) ? "empty" : ""}`}
          id={`artifact-section-${artifact.artifact_id}-${key}`}
          key={key}
          onToggle={(event) => {
            const open = event.currentTarget.open;
            setExpandedSections((current) => {
              const next = new Set(current);
              if (open) next.add(key);
              else next.delete(key);
              return next;
            });
          }}
          open={expandedSections.has(key)}
        >
          <summary data-field-path={key}>
            <div>
              <h3>{labelKey(key)}</h3>
              <p>{sectionHint(key, value)}</p>
            </div>
            <ChevronDown className="artifact-section-chevron" size={17} />
          </summary>
          <div className="artifact-section-content">
            {editing ? (
              <EditableValue
                fieldKey={key}
                value={value}
                onChange={(nextValue) => onChange({ ...payload, [key]: nextValue })}
              />
            ) : <RecursiveValue fieldKey={key} fieldPath={key} value={value} />}
          </div>
        </details>
      ))}
      {!visibleEntries.length ? <div className="payload-empty-box">没有匹配的内容</div> : null}
      </div>
    </div>
  );
}

function RecursiveValue({ value, fieldKey, fieldPath }: { value: unknown; fieldKey?: string; fieldPath?: string }): ReactNode {
  if (value === null || value === undefined || value === "") return <span className="muted">-</span>;
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
    return (
      <span className="payload-scalar" data-field-path={fieldPath}>
        {formatScalar(value)}
      </span>
    );
  }
  if (Array.isArray(value)) {
    if (!value.length) return <div className="payload-empty-box">{text.emptyArray}</div>;
    if (value.every((item) => item === null || ["string", "number", "boolean"].includes(typeof item))) {
      return (
        <ul className="artifact-bullet-list" data-field-path={fieldPath}>
          {value.map((item, index) => (
            <li data-field-path={fieldPath ? `${fieldPath}[${index}]` : `[${index}]`} key={index}>
              {formatScalar(item)}
            </li>
          ))}
        </ul>
      );
    }
    return (
      <div className="artifact-item-list">
        {value.map((item, index) => (
          <section className="artifact-item" data-field-path={fieldPath ? `${fieldPath}[${index}]` : `[${index}]`} key={index}>
            <h3>{arrayItemTitle(fieldKey, item, index)}</h3>
            <RecursiveValue fieldPath={fieldPath ? `${fieldPath}[${index}]` : `[${index}]`} value={item} />
          </section>
        ))}
      </div>
    );
  }

  const object = readObject(value);
  const entries = presentationEntries(object);
  if (!entries.length) return <span className="muted">{text.emptyObject}</span>;

  return (
    <dl className="kv-list">
      {entries.map(([key, entryValue]) => (
        <div data-field-path={fieldPath ? `${fieldPath}.${key}` : key} key={key}>
          <dt>{labelKey(key)}</dt>
          <dd>
            <RecursiveValue fieldKey={key} fieldPath={fieldPath ? `${fieldPath}.${key}` : key} value={entryValue} />
          </dd>
        </div>
      ))}
    </dl>
  );
}

function EditableValue({ value, fieldKey, onChange }: { value: unknown; fieldKey?: string; onChange: (value: unknown) => void }): ReactNode {
  const control = manifestControlFor(value);
  if (control === "toggle") {
    return (
      <label className="editable-check">
        <input checked={Boolean(value)} onChange={(event) => onChange(event.currentTarget.checked)} type="checkbox" />
        <span>{value ? "是" : "否"}</span>
      </label>
    );
  }
  if (control === "number") {
    return <input className="editable-input" onChange={(event) => onChange(numberDraftValue(event.currentTarget.value, Number(value)))} type="number" value={String(value)} />;
  }
  if (control === "text") {
    return <textarea className="editable-textarea" onChange={(event) => onChange(event.currentTarget.value)} value={readString(value)} />;
  }
  if (control === "list" && Array.isArray(value)) {
    if (!value.length) {
      return (
        <div className="editable-array-empty">
          <span>{text.emptyArray}</span>
          <button onClick={() => onChange([newArrayItem(fieldKey)])} type="button"><Plus size={14} />新增一项</button>
        </div>
      );
    }
    return (
      <div className="editable-array">
        {value.map((item, index) => (
          <section className="editable-array-item" key={arrayItemKey(item, index)}>
            <header className="editable-array-item-head">
              <h4>{arrayItemTitle(fieldKey, item, index)}</h4>
              <div className="editable-array-actions">
                <button aria-label="上移" disabled={index === 0} onClick={() => onChange(moveArrayItem(value, index, index - 1))} title="上移" type="button"><ArrowUp size={14} /></button>
                <button aria-label="下移" disabled={index === value.length - 1} onClick={() => onChange(moveArrayItem(value, index, index + 1))} title="下移" type="button"><ArrowDown size={14} /></button>
                <button aria-label="删除" onClick={() => onChange(value.filter((_, itemIndex) => itemIndex !== index))} title="删除" type="button"><Trash2 size={14} /></button>
              </div>
            </header>
            <EditableValue
              fieldKey={fieldKey}
              value={item}
              onChange={(nextItem) => {
                const nextArray = value.slice();
                nextArray[index] = nextItem;
                onChange(nextArray);
              }}
            />
          </section>
        ))}
        <button className="editable-array-add" onClick={() => onChange([...value, newArrayItem(fieldKey, value[0], value.length)])} type="button"><Plus size={14} />新增一项</button>
      </div>
    );
  }

  const object = readObject(value);
  const entries = presentationEntries(object);
  if (!entries.length) return <span className="muted">{text.emptyObject}</span>;

  return (
    <div className="editable-object">
      {entries.map(([key, entryValue]) => (
        <div className="editable-field" key={key}>
          <span>{labelKey(key)}</span>
          <EditableValue
            fieldKey={key}
            value={entryValue}
            onChange={(nextEntryValue) => {
              onChange({ ...object, [key]: nextEntryValue });
            }}
          />
        </div>
      ))}
    </div>
  );
}

function moveArrayItem(items: unknown[], from: number, to: number) {
  const next = items.slice();
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item);
  return next;
}

function arrayItemKey(value: unknown, index: number) {
  const item = readObject(value);
  return readString(item.line_id || item.scene_id || item.episode_id || item.character_id || item.id || item.name) || `item-${index}`;
}

function latestPreviousArtifact(artifacts: Artifact[], current: Artifact) {
  return artifacts
    .filter((candidate) => candidate.artifact_type === current.artifact_type && candidate.artifact_id !== current.artifact_id && candidate.version < current.version)
    .filter((candidate) => current.artifact_type !== "script_unit" || readString(candidate.payload.episode_id) === readString(current.payload.episode_id))
    .sort((a, b) => b.version - a.version)[0];
}

function newArrayItem(fieldKey?: string, example?: unknown, index = 0): unknown {
  if (example !== undefined) return blankLike(example, index);
  switch (fieldKey) {
    case "characters": case "main_characters": return { name: "", role: "", goal: "" };
    case "relationships": return { from: "", to: "", relationship: "" };
    case "episodes": return { episode_id: index + 1, title: "", summary: "" };
    case "scenes": case "scene_outline": return { scene_id: `client_tmp_scene_${Date.now()}`, heading: "", blocks: [] };
    case "blocks": case "lines": return { line_id: `client_tmp_line_${Date.now()}`, block_type: "action", text: "" };
    case "source_refs": return { source_unit_id: "", source_range: "", start_offset: 0, end_offset: 0 };
    case "phase_plan": return { phase_id: "", phase_function: "" };
    case "conflict_materials": return { id: "", summary: "" };
    default: return "";
  }
}

function blankLike(value: unknown, index: number): unknown {
  if (typeof value === "string") return "";
  if (typeof value === "number") return 0;
  if (typeof value === "boolean") return false;
  if (Array.isArray(value)) return [];
  const object = readObject(value);
  const out: Record<string, unknown> = {};
  for (const [key, item] of Object.entries(object)) {
    if (key === "episode_id" || key === "episode_no" || key === "scene_no") out[key] = index + 1;
    else if (key === "line_id") out[key] = `client_tmp_line_${Date.now()}`;
    else if (key === "scene_id") out[key] = `client_tmp_scene_${Date.now()}`;
    else out[key] = blankLike(item, index);
  }
  return out;
}

function EventsPanel({ events }: { events: RunEvent[] }) {
  const [showAll, setShowAll] = useState(false);
  const orderedEvents = useMemo(() => events.slice().sort((a, b) => String(a.created_at || "").localeCompare(String(b.created_at || ""))), [events]);
  const primaryEvents = useMemo(() => orderedEvents.filter(isPrimaryRunEvent), [orderedEvents]);
  const visibleEvents = showAll ? orderedEvents : primaryEvents;
  if (!orderedEvents.length) return <EmptyState>{text.noEvents}</EmptyState>;

  return (
    <section className="process-log">
      <header className="process-log-head">
        <div>
          <strong>过程记录</strong>
          <span>{showAll ? `含系统步骤，共 ${orderedEvents.length} 条` : `关键节点，共 ${primaryEvents.length} 条`}</span>
        </div>
        <div aria-label="记录显示范围" className="process-log-mode" role="tablist">
          <button aria-selected={!showAll} onClick={() => setShowAll(false)} role="tab" type="button">关键记录</button>
          <button aria-selected={showAll} onClick={() => setShowAll(true)} role="tab" type="button">全部记录</button>
        </div>
      </header>
      <div className="event-list">
      {visibleEvents.map((event) => (
        <article className="event-item" key={event.event_id}>
          <span className={`event-marker ${event.type}`} aria-hidden />
          <div className="event-content">
            <div className="event-heading">
              <strong className="event-type">{labelEventType(event.type)}</strong>
              {event.created_at ? <time dateTime={event.created_at}>{formatEventTime(event.created_at)}</time> : null}
            </div>
            <div className="event-message">{labelEventMessage(event)}</div>
          </div>
        </article>
      ))}
      </div>
    </section>
  );
}

function formatEventTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(date);
}

function labelKey(key: string) {
  return fieldLabels[key] || fallbackLabel(key);
}

function fallbackLabel(key: string) {
  return manifestFallbackLabel(key);
}

function statusClass(status?: string) {
  if (status === "failed" || status === "invalidated") return "danger";
  if (status === "pending_approval" || status === "stale") return "warning";
  if (status === "draft") return "neutral";
  return "success";
}

function orderEntries(type: ArtifactType, entries: Array<[string, unknown]>) {
  const order = preferredOrder[type] || [];
  return entries.sort(([left], [right]) => {
    const leftIndex = order.indexOf(left);
    const rightIndex = order.indexOf(right);
    if (leftIndex >= 0 && rightIndex >= 0) return leftIndex - rightIndex;
    if (leftIndex >= 0) return -1;
    if (rightIndex >= 0) return 1;
    return labelKey(left).localeCompare(labelKey(right), "zh-Hans-CN");
  });
}

function presentationEntries(value: Record<string, unknown>) {
  return Object.entries(value).filter(([key]) => !hiddenPresentationFields.has(key));
}

function formatArtifactText(artifact: Artifact) {
  const title = artifactLabels[artifact.artifact_type] || "过程产物";
  const lines = [title, ""];
  for (const [key, value] of orderEntries(artifact.artifact_type, presentationEntries(readObject(artifact.payload)))) {
    lines.push(labelKey(key));
    lines.push(...formatExportValue(value, 0, key));
    lines.push("");
  }
  return lines.join("\n").trimEnd() + "\n";
}

function formatExportValue(value: unknown, depth: number, fieldKey?: string): string[] {
  const indent = "  ".repeat(depth);
  if (Array.isArray(value)) {
    if (!value.length) return [`${indent}暂无内容`];
    return value.flatMap((item, index) => {
      if (item && typeof item === "object") {
        return [`${indent}${index + 1}. ${arrayItemTitle(fieldKey, item, index)}`, ...formatExportValue(item, depth + 1)];
      }
      return [`${indent}${index + 1}. ${formatScalar(item)}`];
    });
  }
  if (value && typeof value === "object") {
    const entries = presentationEntries(readObject(value));
    if (!entries.length) return [`${indent}暂无内容`];
    return entries.flatMap(([key, child]) => [
      `${indent}${labelKey(key)}：`,
      ...formatExportValue(child, depth + 1, key),
    ]);
  }
  const scalar = formatScalar(value);
  return String(scalar).split(/\r?\n/).map((line) => `${indent}${line}`);
}

function sectionHint(key: string, value: unknown) {
  if (Array.isArray(value)) return value.length ? `${value.length} 项内容` : text.emptyArray;
  if (key === "generation_config") return "生成前确认的集数、时长和拆分参数。";
  if (value && typeof value === "object") return `${presentationEntries(readObject(value)).length} 个字段`;
  return text.sectionHint;
}

function isEmptyValue(value: unknown) {
  if (value === null || value === undefined || value === "") return true;
  if (Array.isArray(value)) return value.length === 0;
  if (typeof value === "object") return presentationEntries(readObject(value)).length === 0;
  return false;
}

function formatScalar(value: unknown) {
  if (typeof value === "boolean") return value ? "是" : "否";
  if (value === null || value === undefined || value === "") return "-";
  const labels: Record<string, string> = {
    mini_short_drama: "竖屏短剧",
    novel: "小说",
    non_novel: "非小说",
    high: "高",
    medium: "中",
    low: "低",
    none: "无",
    decision: "决策钩子",
    source_fact: "原文事实",
    script_generate: "生成剧本",
    compress_scene: "压缩场景",
    sharpen_existing_conflict: "强化现有冲突",
    conflict: "冲突",
    secret: "秘密",
    danger: "危险",
    emotional_turn: "情绪转折",
    reveal: "揭示",
    source_supported_preview: "原文支撑的预告",
    adaptation_suggestion: "改编建议",
    weak_source_boundary: "原文边界偏弱",
    low_information_density: "信息密度偏低",
    likely_padding: "可能存在注水",
    source_too_short: "原文过短",
    preserve: "保留",
    ignore: "忽略",
    user_confirm_required: "需要用户确认",
    enough: "充足",
    weak: "偏弱",
    insufficient: "不足",
    source_supported: "有原文支撑",
    inferred: "模型推断",
    action: "动作",
    dialogue: "对白",
    transition: "转场",
    scene_note: "场景说明",
    INT: "内景",
    EXT: "外景",
    "INT/EXT": "内外景",
  };
  if (typeof value === "string" && labels[value]) return labels[value];
  return String(value);
}

function arrayItemTitle(fieldKey: string | undefined, value: unknown, index: number) {
  const object = readObject(value);
  const episodeID = readString(object.episode_id || object.episode_no || object.episode);
  const sceneNo = readString(object.scene_no);
  const sceneID = readString(object.scene_id);
  const name = readString(object.name || object.title || object.heading || object.label);
  const role = readString(object.role);
  const sourceUnitID = readString(object.source_unit_id);

  if (fieldKey === "episodes" && episodeID) return name ? `第 ${episodeID} 集 / ${name}` : `第 ${episodeID} 集`;
  if (fieldKey === "scenes" && (sceneNo || sceneID)) return name ? `场景 ${sceneNo || sceneID} / ${name}` : `场景 ${sceneNo || sceneID}`;
  if (fieldKey === "characters" && name) return role ? `${name} / ${role}` : name;
  if (sourceUnitID) return `${labelKey(fieldKey || "item")} / ${sourceUnitID}`;
  if (name) return name;
  return `${labelKey(fieldKey || "item")} ${index + 1}`;
}

function readObject(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : {};
}

function readArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function readString(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return JSON.stringify(value);
}

function clonePayload(value: unknown): Record<string, unknown> {
  try {
    return JSON.parse(JSON.stringify(readObject(value))) as Record<string, unknown>;
  } catch {
    return {};
  }
}

function sameValue(left: unknown, right: unknown) {
  try {
    return JSON.stringify(left) === JSON.stringify(right);
  } catch {
    return left === right;
  }
}

function numberDraftValue(value: string, fallback: number) {
  if (value.trim() === "") return 0;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function sourceTextFromFiles(files: unknown[]) {
  return files
    .map((item, index) => {
      const file = readObject(item);
      const preview = readString(file.text_preview) || readString(file.text_content);
      if (!preview.trim()) return "";
      const fileName = readString(file.file_name) || readString(file.name) || `文件 ${index + 1}`;
      return `# ${fileName}\n\n${preview.trim()}`;
    })
    .filter(Boolean)
    .join("\n\n");
}
