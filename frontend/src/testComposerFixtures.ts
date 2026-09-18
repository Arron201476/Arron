import type { ComposerRegistryEntry } from "./types";

type EntryOverrides = Partial<Omit<ComposerRegistryEntry, "capability_id" | "label" | "config" | "entry_policy">> & {
  capability_id: string;
  label: string;
  config?: Partial<ComposerRegistryEntry["config"]>;
  entry_policy?: Partial<ComposerRegistryEntry["entry_policy"]>;
};

export function composerEntry(overrides: EntryOverrides): ComposerRegistryEntry {
  return {
    capability_id: overrides.capability_id,
    version: overrides.version ?? "1.0.0",
    label: overrides.label,
    description: overrides.description ?? `${overrides.label} test fixture`,
    status: overrides.status ?? "available",
    reason_code: overrides.reason_code,
    user_message: overrides.user_message,
    execution_mode: overrides.execution_mode ?? "stateful_workflow",
    creates_run: overrides.creates_run ?? true,
    view_key: overrides.view_key ?? "source_materials",
    icon_key: overrides.icon_key ?? "sparkles",
    menu_order: overrides.menu_order ?? 100,
    default_prompt: overrides.default_prompt,
    accepted_asset_kinds: overrides.accepted_asset_kinds ?? ["text", "document"],
    input_binding: overrides.input_binding ?? { source_type: "story", asset_role: "primary_source" },
    entry_policy: {
      explicit_invocation: true,
      auto_route: true,
      requires_user_confirmation: true,
      input_collection_modes: ["fixed"],
      ...overrides.entry_policy,
    },
    config: {
      view_key: "json_schema",
      default_ref: "default",
      options: ["default"],
      ...overrides.config,
    },
    skill: overrides.skill ?? null,
  };
}

export const legacyComposerEntries: ComposerRegistryEntry[] = [
  composerEntry({
    capability_id: "novel_to_script",
    version: "1.4.0",
    label: "小说转剧本",
    description: "小说材料改编",
    menu_order: 10,
    default_prompt: "请将我提供的小说改编成短剧剧本。",
    input_binding: { source_type: "novel", asset_role: "primary_source" },
    config: { view_key: "script_generation", default_ref: "creation", options: ["creation"] },
  }),
  composerEntry({
    capability_id: "non_novel_to_script",
    version: "1.3.0",
    label: "非小说文本转剧本",
    description: "大纲和集纲改编",
    menu_order: 20,
    default_prompt: "请将我提供的故事大纲、集纲或其他非小说文本改编成短剧剧本。",
    input_binding: { source_type: "non_novel", asset_role: "primary_source" },
    config: { view_key: "script_generation", default_ref: "creation", options: ["creation"] },
  }),
  composerEntry({
    capability_id: "video_reference_creation",
    version: "1.2.0",
    label: "视频参考创作",
    description: "参考视频解析与改编",
    menu_order: 30,
    default_prompt: "请解析我提供的参考视频，并基于解析结果改编生成新的短剧剧本。",
    view_key: "video_asset_set",
    accepted_asset_kinds: ["video"],
    input_binding: { source_type: "video_reference", asset_role: "primary_source", asset_set_purpose: "video_reference_source" },
    config: { view_key: "video_extraction", default_ref: "extraction", options: ["extraction"] },
  }),
  composerEntry({
    capability_id: "script_continuation",
    label: "剧本续写",
    description: "先选方向再续写",
    menu_order: 40,
    default_prompt: "请分析我提供的已有剧本，先给出 5 个续写方向供我选择，再按选定方向续写完整剧本。",
    input_binding: { source_type: "script", asset_role: "primary_source" },
    config: { view_key: "script_continuation", default_ref: "continuation", options: ["continuation"] },
  }),
];

export const dynamicStoryEntry = composerEntry({
  capability_id: "dynamic_story_skill",
  label: "动态故事评审",
  description: "由安装包动态注册的故事评审流程",
  menu_order: 900,
  default_prompt: "请评审这个故事，并输出结构化建议。",
  config: { view_key: "json_schema", default_ref: "strict_review", options: ["strict_review", "quick_review"] },
});
