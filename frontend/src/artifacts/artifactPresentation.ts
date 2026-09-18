import type { ArtifactPresentation } from "../types";

const internalArtifactTypes = new Set(["structured_run_state"]);

export function presentationFor(items: ArtifactPresentation[], type: string): ArtifactPresentation {
  const registered = items.find((item) => item.artifact_type === type);
  if (registered) {
    if (!internalArtifactTypes.has(type)) return registered;
    return {
      ...registered,
      editable: false,
      navigation: { ...registered.navigation, visibility: "internal" },
      available_actions: registered.available_actions.filter((action) => action === "inspect"),
    };
  }
  return {
    artifact_type: type,
    label: humanizeArtifactType(type),
    description: "当前客户端尚未注册该产物视图，以下内容仅供检查。",
    renderer: "document",
    editable: false,
    preferred_fields: [],
    navigation: { order: Number.MAX_SAFE_INTEGER, group_mode: "single", visibility: internalArtifactTypes.has(type) ? "internal" : "user" },
    available_actions: internalArtifactTypes.has(type) ? [] : ["inspect"],
  };
}

function humanizeArtifactType(type: string): string {
  const value = type.trim().replace(/[_-]+/g, " ");
  return value || "通用产物";
}

export const fieldLabels: Record<string, string> = {
  adaptation_goal: "改编目标", adaptation_risks: "改编风险", actual_episode_count: "实际集数", allowed_additions: "允许补充",
  assets: "来源文件", basic_information: "基础信息", central_conflict: "核心冲突", character_and_relationship_adjustments: "人物与关系调整",
  character_progression: "人物成长", character_state: "人物状态", characters: "人物", characters_and_relationships: "人物与关系",
  climax_map: "高潮分布", collection_state: "素材批次状态", completeness: "完整度", compliance_boundary: "合规边界",
  conflict_materials: "冲突素材", content_to_replace: "需要替换的内容", continuity_delta: "连续性变化", continuity_rules: "连续性规则",
  continuity_state: "连续性状态", core_premise: "核心前提", covered_source_unit_ids: "已覆盖来源", development_notes: "发展说明",
  differentiation: "差异化", discard_or_later: "弃用或后置", duplicate_episode_nos: "重复集数", emotional_drives: "情绪驱动力",
  episode_count: "集数", episode_count_reason: "集数依据", episode_duration_minutes: "每集时长", episode_execution_mode: "生成节奏", episode_no: "集数",
  episode_order: "集数顺序", episodes: "分集", extraction_completeness: "提取完整度", failed_episode_nos: "失败集数",
  final_conclusion: "最终结论", first_major_climax_plan: "首个大高潮", fit_risks: "体量适配风险", foreshadowing_and_payoff: "伏笔与回收",
  forbidden_changes: "禁止修改", forbidden_copying: "禁止照搬", gaps_and_questions: "缺口与问题", genre_style_worldview_direction: "类型、风格与世界观方向",
  genre: "题材类型", genre_tags: "类型标签", global_continuity_state: "全局连续性", global_risks: "全局风险", hook_candidates: "钩子候选",
  hook_distribution: "钩子分布", hook_engine: "钩子机制", hook_system: "钩子系统", inferred_candidates: "推断候选",
  input_type_tags: "材料类型", logline: "一句话故事", main_characters: "主要人物", main_plotline: "主线",
  major_plotline: "主要情节线", marketing_material_directions: "营销素材方向", material_completeness: "材料完整度", missing_episode_nos: "缺失集数",
  missing_or_unread_scope: "缺失或未读取范围", most_promising_direction: "最有潜力方向", must_follow_facts: "必须遵守的事实", must_keep_facts: "必须保留的事实",
  new_elements: "新增元素", next_episode_must_address: "下集必须承接", opening_strategy: "开场策略", optimization_actions: "优化动作",
  options: "改编选项", overall_judgment: "整体判断", pacing_density_plan: "节奏密度", patterns_to_keep: "保留模式",
  payoff_and_emotion: "爽点与情绪", payoff_candidates: "爽点候选", payoff_chain: "爽点链路", payoff_distribution: "爽点分布",
  phase_plan: "阶段规划", plot_and_emotion_curve: "剧情与情绪曲线", plot_summary: "剧情概要", protagonist: "主角",
  quality_flags: "质量提示", recommended_episode_count: "建议集数", relationship_engine: "关系驱动", relationship_progression: "关系推进",
  relationship_state: "关系状态", relationships: "人物关系", resolved_episode_count: "确认集数", review_focus: "审核重点",
  risks: "风险", scenes: "场次", script_text: "剧本正文", selected_methods: "已选方法", selection_instructions: "选择说明",
  self_check: "自检", series_promise: "系列承诺", short_drama_assets: "短剧资产", source_availability: "来源可用性",
  source_file_name: "来源文件", source_kind: "来源类型", source_material: "来源材料", source_structure: "原文结构",
  split_strategy: "拆集策略", story_overview: "故事概览", style_constraints: "风格约束", target_episode_count: "目标集数",
  title: "标题", transferable_methods: "可迁移方法", truncation_risk: "截断风险", uncertainty_flags: "不确定项",
  units: "来源单元", unresolved_questions: "待确认问题", user_notes: "用户说明", user_requirements: "用户要求",
  user_supplied_facts: "用户提供事实", video_script_unit: "视频还原剧本", visual_scene_candidates: "视觉场景候选",
  volume_fit_notes: "体量适配", volume_plan_notes: "体量规划", world_rules: "世界规则", critical_presentation_constraints: "关键呈现约束",
  audience: "目标观众", basis: "依据", block_type: "内容类型", blocks: "剧本段落", can_support_target: "能否支撑目标体量",
  card_point_function: "本集卡点作用", category: "分类", change: "调整方式", character: "人物", character_state_changes: "人物状态变化",
  character_turn: "人物转变", character_and_relationship_changes: "人物与关系变化", claim: "结论", claims: "结论依据", confidence: "可信度",
  content: "内容", conversion_point: "转化点", continuity_risks: "连续性风险", density_note: "密度说明", delivery: "表达方式",
  direction: "方向", duration_minutes: "时长（分钟）", emotional_payoff: "情绪回报", end_ms: "结束时间", ending_hook: "结尾钩子",
  episode_card: "分集卡", episode_card_delta: "分集卡变化", episode_function: "本集作用", episode_index: "分集索引", episode_range: "集数范围",
  escalation_path: "升级路径", event_chain_changes: "事件链调整", event_summary: "事件概要", evidence_excerpt: "依据摘录", expected_effect: "预期效果",
  expansion_policy: "扩写策略", expansion_strategy: "扩写方法", final_payoff_direction: "最终回报方向", flashback_or_memory_use: "闪回与回忆使用",
  format: "形式", format_risks: "格式风险", from_series_blueprint: "来自系列蓝图", from_story_seed: "来自故事种子", from_user_material: "来自用户材料",
  function: "作用", generated_additions: "生成补充", goal: "目标", grounded: "是否有材料依据", heading: "场景标题", hook_strength: "钩子强度",
  hook_strategy: "钩子策略", hook_text: "钩子内容", hook_type: "钩子类型", hooks_opened: "新增悬念", hooks_resolved: "已回收悬念",
  impact: "影响", inferred: "是否推断", information_density: "信息密度", inner_need: "内在需求", inner_outer: "内外景",
  is_suggestion: "是否为建议", items: "分析项", keep: "保留内容", key_conflict: "关键冲突", key_events: "关键事件", key_visual_moments: "关键视觉时刻",
  known_gaps: "已知缺口", limitations: "局限", location: "地点", locked: "是否锁定", main_conflict: "主要冲突", material_type: "材料类型",
  must_not_expand: "禁止扩写", name_or_role: "姓名或身份", new_element_suggestions: "新元素建议", new_facts: "新增事实", one_sentence_story: "一句话故事",
  one_sentence_strategy: "一句话策略", opening_situation: "开场处境", opening_state: "开场状态", order: "顺序", pacing_hook_transfer: "节奏与钩子迁移",
  pacing_plan: "节奏规划", pacing_risk: "节奏风险", padding_risks: "注水风险", payoff_focus: "爽点重点", payoff_or_reversal: "爽点或反转",
  personality: "性格", phase_function: "阶段作用", position: "位置", preserve_existing_episode_marks: "保留原分集标记", pressure: "外部压力",
  priority: "优先级", problem: "问题", progression: "推进方式", recommendation: "建议", recommended_episode_range: "建议集数范围",
  relationship_changes: "关系变化", relationship_type: "关系类型", review_notes: "审核说明", risk: "风险", scene_function: "场次作用",
  scene_no: "场次", scene_outline: "场次概要", sound_or_object_triggers: "声音或道具触发", source: "来源", source_basis: "来源依据",
  source_labels: "来源标记", source_type: "来源类型", special_mechanism_tags: "特殊机制标签", speaker: "说话人", start_ms: "开始时间",
  statement: "陈述", status: "状态", strengthen: "加强内容", suitable_when: "适用情况", summary: "概要", text: "正文", time: "时间",
  time_of_day: "时段", time_range: "时间范围", type: "类型", uncertainty: "不确定内容", visual_strategy: "视觉策略", weak_episode_handling: "弱集处理",
  worldview: "世界观", worldview_changes: "世界观调整", worldview_tags: "世界观标签",
  age: "年龄", core: "核心内容", core_goal: "核心目标", core_target: "核心目标", event: "事件", identity: "身份", name: "名称", traits: "人物特征",
  continue_with_incomplete_material: "按不完整材料继续", continue_with_incomplete_source: "按不完整来源继续",
  adaptation_added: "改编新增内容", adaptation_freedom: "改编自由度", adaptation_suggestions: "改编建议", affected_episode_nos: "受影响集数",
  base_style: "基础语言风格", blocker: "阻断问题", boundary_check: "边界检查", boundary_reason: "边界依据",
  character_changes: "人物变化", compliance_mode: "合规模式", compliance_status: "合规状态", conflict_stage: "冲突阶段",
  continuity_risk: "连续性风险", core_conflict: "核心冲突", core_event: "核心事件", coverage_check: "覆盖检查",
  covered_ranges: "已覆盖范围", cut_after_anchor: "后切分锚点", cut_before_anchor: "前切分锚点", declared_material_types: "已声明材料类型",
  description: "说明", diagnostic_scores: "诊断评分", dialogue_lines: "对白", difference_requirements: "差异化要求",
  duplicated_ranges: "重复范围", duration_ms: "时长（毫秒）", episode_cards: "分集卡", episode_end: "结束集数",
  episode_nos: "涉及集数", episode_start: "起始集数", episode_summaries: "分集概要", events: "事件",
  evidence: "依据", fidelity_level: "还原程度", file_name: "文件名", high: "高风险数量",
  hook_or_suspense_potential: "钩子或悬念潜力", hook_source: "钩子来源", interior_exterior: "内外景", issue_counts: "问题数量",
  issues: "问题", kind: "类别", known_characters: "已知人物", locations: "地点", low: "低风险数量",
  main_emotional_drive: "主要情绪驱动", major_turn: "关键转折", manual_review_required: "是否需要人工检查", material_bank: "素材库",
  material_sufficiency: "材料充足度", medium: "中风险数量", message: "说明", missing_ranges: "缺失范围", mode: "模式",
  must_keep_dialogue_or_moments: "必须保留的对白或情节", must_preserve: "必须保留", next_episode_start: "下集开场", notes: "备注",
  one_sentence_logline: "一句话梗概", order_issues: "顺序问题", parent_label: "上级名称", policy_pack_version: "规则包版本",
  previous_episode_end: "上集结尾", proposed_change: "建议修改", props: "道具", questions: "待确认问题",
  range_label: "范围说明", reason: "原因", recommended_route: "建议处理路径", regional_speech: "方言或地域表达",
  relationship_position: "关系定位", requested_by: "请求来源", requires_confirmation: "需要确认", requires_user_attention: "需要用户关注",
  retention_profile: "保留策略", review_version: "审核版本", revision_target: "修改目标", risk_notes: "风险说明", role: "角色作用",
  scope: "范围", selling_points: "卖点", severity: "严重程度", source_fidelity_risks: "原作忠实度风险", source_range: "原文范围",
  source_summary: "来源概要", speech_profile: "语言特征", split_confidence: "拆集可信度", timecode_precision: "时间码精度",
  uncertain_content_policy: "不确定内容处理规则", unit_kind: "单元类型", usage_boundary: "使用边界", user_confirmed_facts: "用户确认事实",
  weak_episode_reason: "弱集原因", character_pair: "关联人物", compression_candidates: "可压缩内容", core_hook_refinement: "核心钩子优化",
  delete_or_deemphasize_candidates: "可删除或弱化内容", emotional_drive_frontload: "情绪驱动前置", first_major_climax_candidate: "首个大高潮候选",
  foreshadowing: "伏笔", frontload_candidates: "可前置内容", high_value_conflicts: "高价值冲突", major_climax_candidates: "主要高潮候选",
  merge_or_compress_candidates: "可合并或压缩内容", payoff: "回收", plot_node: "情节节点", preserve_candidates: "建议保留内容",
  psychology_to_scene_candidates: "心理活动场景化", recommended_position_note: "建议位置", relation_type: "关系类型", risk_flags: "风险提示",
  visualization_candidates: "视觉化候选", why_it_can_hold_first_card: "首个卡点成立原因",
};

export function fieldLabel(key: string): string {
  if (fieldLabels[key]) return fieldLabels[key];
  return "其他信息";
}

export const scalarLabels: Record<string, string> = {
  action: "动作", active: "进行中", approved: "已通过", artifact_path: "产物路径", asset_text_range: "素材文本范围",
  blocker: "阻断", borderline: "临界", chapter_block: "章节片段", character: "人物", collecting: "收集中", complete: "完整",
  complete_first_pass: "已完成首轮", CONFIRMED_CHANGE: "已确认修改", creative: "创作型", deleted: "已删除", dialogue: "对白",
  document_section: "文档章节", episode_plan: "分集规划", event: "事件", expired: "已过期", FACT: "事实", format_only: "仅调整格式",
  full_script: "完整剧本", high: "高", impacted_scope: "受影响范围", incomplete: "不完整", incomplete_confirmed: "已确认材料不完整",
  INFERENCE: "推断", information: "信息", insufficient: "不足", issues_found: "发现问题", low: "低", medium: "中", model: "模型",
  NEEDS_POLICY: "需要规则判断", needs_review: "需要审核", non_novel: "非小说文本", none: "无", not_requested: "未要求", novel: "小说",
  other: "其他", overlapping_speech: "多人声音重叠", P0: "P0", P1: "P1", P2: "P2", partial: "部分完整", passed: "已通过",
  pending: "待处理", primary_source: "主要来源", prop: "道具", PROPOSAL: "建议", reference_source: "参考来源", rejected: "未通过",
  relationship: "人物关系", scene_note: "场次说明", script_generation: "剧本生成", sealed: "已确认", source_analysis: "来源分析",
  speaker_unknown: "说话人不明", story_bible: "故事圣经", story_unit: "故事单元", subtitle_occluded: "字幕被遮挡", sufficient: "充足",
  supporting_source: "辅助来源", transition: "过渡", unclear_audio: "音频不清", unclear_visual: "画面不清", unconfirmed: "未确认",
  unknown: "未知", UNKNOWN: "未知", user: "用户", user_message: "用户消息", video: "视频", video_reference: "视频参考",
  video_time_range: "视频时间范围", world_rule: "世界规则", 内: "内", 外: "外", 内外: "内外", available: "可用", confirmed: "已确认",
  continuous: "自动生成全部", review_each: "逐集确认",
};

export function scalarLabel(value: unknown): string {
  if (typeof value === "boolean") return value ? "是" : "否";
  return scalarLabels[String(value)] ?? String(value);
}

const technical = new Set([
  "asset_set_id", "asset_set_version_id", "confirmed_strategy_refs", "created_at", "generation_config", "input_snapshot_id",
  "manifest_id", "manifest_version", "project_id", "run_input_snapshot_version_id", "runtime_check", "sealed_input_snapshot_id",
  "source_refs", "source_trace", "unit_refs", "user_request_message_id", "script_artifact_version_id", "json_pointer", "refs",
]);

export function isTechnicalField(key: string): boolean {
  return technical.has(key) || key.endsWith("_id") || key.endsWith("_ids") || key.endsWith("_ref") || key.endsWith("_refs") || key.endsWith("_hash");
}

export function orderedEntries(value: Record<string, unknown>, preferredFields: string[] = []) {
  const order = preferredFields;
  return Object.entries(value).sort(([left], [right]) => {
    const leftIndex = order.indexOf(left);
    const rightIndex = order.indexOf(right);
    if (leftIndex >= 0 && rightIndex >= 0) return leftIndex - rightIndex;
    if (leftIndex >= 0) return -1;
    if (rightIndex >= 0) return 1;
    return fieldLabel(left).localeCompare(fieldLabel(right), "zh-Hans-CN");
  });
}
