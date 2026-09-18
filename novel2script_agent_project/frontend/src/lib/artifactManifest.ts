export type ArtifactFieldControl = "toggle" | "number" | "text" | "list" | "object";

const tokenLabels: Record<string, string> = {
  action: "动作", actual: "实际", addition: "补充", arc: "成长线", basis: "依据", boundary: "边界",
  card: "分集卡", character: "人物", check: "检查", conflict: "冲突", continuity: "连续性", count: "数量",
  dialogue: "对白", distribution: "分布", duration: "时长", emotional: "情绪", episode: "分集", evidence: "证据",
  event: "事件", fact: "事实", flag: "标记", global: "全局", goal: "目标", hook: "钩子", id: "标识",
  information: "信息", line: "台词行", map: "映射", material: "素材", minutes: "分钟", note: "备注",
  payoff: "回报", plan: "规划", profile: "特征", relationship: "关系", ref: "引用", risk: "风险",
  scene: "场景", source: "来源", state: "状态", strategy: "策略", summary: "摘要", target: "目标",
  task: "任务", text: "文本", trace: "追踪", type: "类型", unit: "单元", visual: "视觉",
  adaptation: "改编", additions: "新增内容", allowed: "允许", anchor: "锚点", candidate: "候选",
  candidates: "候选", climax: "高潮", compression: "压缩", core: "核心", detected: "识别结果",
  effective: "有效", ending: "结尾", engine: "驱动机制", existing: "现有", fit: "适配",
  forbidden: "禁止", frontload: "前置", generated: "模型新增", handling: "处理方式", inference: "推断",
  main: "主要", marker: "标记", markers: "标记", opening: "开场", pacing: "节奏", policy: "策略",
  position: "位置", preserve: "保留", promising: "潜力", range: "范围", reason: "原因",
  recommended: "建议", refs: "引用", resolved: "最终", rules: "规则", speech: "语言风格",
  structure: "结构", supplied: "用户提供", supporting: "支撑", usage: "使用", volume: "体量", weak: "薄弱",
};

export function manifestFallbackLabel(key: string) {
  const words = key.replace(/([a-z0-9])([A-Z])/g, "$1_$2").toLowerCase().split(/[_\s-]+/).filter(Boolean);
  const translated = words.map((word) => tokenLabels[word] || word).join(" · ");
  return translated || "其他内容";
}

export function manifestControlFor(value: unknown): ArtifactFieldControl {
  if (typeof value === "boolean") return "toggle";
  if (typeof value === "number") return "number";
  if (Array.isArray(value)) return "list";
  if (value && typeof value === "object") return "object";
  return "text";
}
