import { Bot, FileText, Loader2, Pause, Plus, Play, RotateCcw, Send, User } from "lucide-react";
import { useState } from "react";
import type { RefObject } from "react";
import type { ApprovalAction, ApprovalRequest, ChatMessage, ExecutionStep, FileAttachment, GenerationConfig, GenerationConfigPrompt, RunStatus, SourceMode } from "../../api/types";
import type { SelectionContext } from "../../api/types";
import { Activity, CheckCircle2, ChevronDown, ChevronUp, Clapperboard, PanelRightClose, X } from "lucide-react";
import { artifactLabels, formatFileSize } from "../../lib/workbenchLabels";
import { Button } from "../ui/Button";

interface AgentPanelProps {
  isThinking: boolean;
  isSendDisabled: boolean;
  runStatus?: RunStatus;
  activeApprovalID?: string | null;
  messages: ChatMessage[];
  composerText: string;
  attachments: FileAttachment[];
  selectionContext?: SelectionContext | null;
  showGenerateShortcut?: boolean;
  sourceMode: SourceMode;
  timelineRef: RefObject<HTMLDivElement | null>;
  fileInputRef: RefObject<HTMLInputElement | null>;
  onComposerChange: (value: string) => void;
  onSourceModeChange: (value: SourceMode) => void;
  onSubmitGenerationConfig: (messageID: string, originalMessage: string, config: GenerationConfig, sourceMode: SourceMode, fileIDs: string[]) => void;
  onSend: () => void;
  onGenerateShortcut: () => void;
  onApprove: () => void;
  onKeepDownstream: () => void;
  onPause: () => void;
  onStop: () => void;
  onResume: () => void;
  onRegenerateDownstream: () => void;
  onReset: () => void;
  onOpenEvents: () => void;
  onAddFiles: (files: FileList | null) => void;
  onRemoveAttachment: (index: number) => void;
  onClearSelection: () => void;
  onCollapse: () => void;
}

const copy = {
  agentTitle: "\u0041\u0067\u0065\u006e\u0074 \u52a9\u624b",
  ready: "\u5c31\u7eea",
  thinking: "\u6b63\u5728\u601d\u8003",
  waiting: "\u7b49\u5f85\u786e\u8ba4",
  placeholder: "\u7ed9 Agent \u53d1\u6d88\u606f\uff0c\u6216\u7c98\u8d34\u5c0f\u8bf4\u539f\u6587 / \u6897\u6982 / \u77ed\u5267\u7075\u611f\u3002",
  sourceMode: "\u7d20\u6750\u7c7b\u578b",
  auto: "\u81ea\u52a8\u8bc6\u522b",
  novel: "\u5c0f\u8bf4",
  nonNovel: "\u975e\u5c0f\u8bf4",
  configTitle: "\u786e\u8ba4\u751f\u6210\u914d\u7f6e",
  configReason: "\u542f\u52a8\u5267\u672c\u751f\u6210\u524d\uff0c\u9700\u8981\u5148\u786e\u8ba4\u76ee\u6807\u96c6\u6570\u548c\u5355\u96c6\u65f6\u957f\u3002",
  episodes: "\u96c6\u6570",
  minutes: "\u5206\u949f/\u96c6",
  preserve: "\u4fdd\u7559\u539f\u5206\u96c6",
  confirmConfig: "\u786e\u8ba4\u5e76\u5f00\u59cb\u751f\u6210",
  addFile: "\u6dfb\u52a0\u6587\u4ef6",
  reset: "\u91cd\u7f6e",
  send: "\u53d1\u9001",
  you: "\u4f60",
  done: "\u5df2\u5b8c\u6210",
  events: "\u67e5\u770b\u8fc7\u7a0b\u8bb0\u5f55",
  inactiveApproval: "\u5386\u53f2\u786e\u8ba4\u70b9\uff0c\u5f53\u524d\u4e0d\u53ef\u64cd\u4f5c\u3002",
  approveTitle: "\u786e\u8ba4\u5f53\u524d\u4ea7\u7269\u540e\u7ee7\u7eed",
  approveReason: "\u786e\u8ba4\u540e Agent \u4f1a\u7ee7\u7eed\u4e0b\u4e00\u6b65\uff1b\u5982\u679c\u8981\u4fee\u6539\uff0c\u8bf7\u76f4\u63a5\u8865\u5145\u8981\u6c42\u3002",
  approve: "\u786e\u8ba4\u7ee7\u7eed",
  pause: "\u6682\u505c",
  keepDownstream: "保留现有后续内容",
  regenerateDownstream: "重新生成受影响内容",
  affectedDownstream: "可能受影响：",
  removeFile: "\u79fb\u9664\u9644\u4ef6",
  selection: "\u5df2\u5f15\u7528\u9009\u533a",
  clearSelection: "\u79fb\u9664\u9009\u533a\u5f15\u7528",
};

export function AgentPanel(props: AgentPanelProps) {
  const stateLabel = props.isThinking ? copy.thinking : props.runStatus === "waiting_approval" ? copy.waiting : copy.ready;
  const latestAgentMessageID = [...props.messages].reverse().find((message) => message.role === "agent")?.id;
  const activeRunVisible = props.runStatus === "running";
  const composerLocked = props.isThinking || props.runStatus === "running";

  return (
    <aside className="agent-pane" id="agent-panel">
      <div className="pane-head">
        <h1>{copy.agentTitle}</h1>
        <div className="agent-head-actions">
          <span className={`agent-state ${props.isThinking ? "running" : props.runStatus === "waiting_approval" ? "waiting" : ""}`}>{stateLabel}</span>
          {props.runStatus === "running" ? <button aria-label="暂停当前生成" className="agent-run-control" onClick={props.onStop} title="暂停当前生成" type="button"><Pause size={16} /></button> : null}
          {props.runStatus === "paused" ? <button aria-label="继续当前生成" className="agent-run-control" onClick={props.onResume} title="继续当前生成" type="button"><Play size={16} /></button> : null}
          <button aria-controls="agent-panel" aria-expanded="true" aria-label="关闭 Agent 面板" className="panel-collapse-button" onClick={props.onCollapse} title="关闭 Agent 面板" type="button">
            <PanelRightClose size={16} />
          </button>
        </div>
      </div>
      <div className="timeline" ref={props.timelineRef}>
        {props.messages.map((message) => (
          <MessageBubble
            activeApprovalID={props.activeApprovalID}
            key={message.id}
            message={message}
            onApprove={props.onApprove}
            onKeepDownstream={props.onKeepDownstream}
            onPause={props.onPause}
            onRegenerateDownstream={props.onRegenerateDownstream}
            onSubmitGenerationConfig={props.onSubmitGenerationConfig}
            openEvents={props.onOpenEvents}
            runStatus={props.runStatus}
            showActiveStep={activeRunVisible && message.id === latestAgentMessageID}
            isBusy={props.isThinking || props.runStatus === "running"}
          />
        ))}
        {props.isThinking ? (
          <div className="agent-thinking">
            <Loader2 className="spin" size={15} />
            <span>{copy.thinking}</span>
          </div>
        ) : null}
      </div>
      <div className="chat-box">
        <SelectionReference selection={props.selectionContext} onClear={props.onClearSelection} />
        {props.showGenerateShortcut ? (
          <button
            className="composer-shortcut"
            onClick={props.onGenerateShortcut}
            type="button"
          >
            <Clapperboard size={14} />
            <span>生成剧本</span>
          </button>
        ) : null}
        <textarea
          aria-label={copy.placeholder}
          autoComplete="off"
          name="agent-message"
          readOnly={composerLocked}
          value={props.composerText}
          onChange={(event) => props.onComposerChange(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
              event.preventDefault();
              if (!props.isSendDisabled) props.onSend();
            }
          }}
          placeholder={copy.placeholder}
          spellCheck={false}
        />
        <AttachmentTray attachments={props.attachments} remove={props.onRemoveAttachment} />
        <div className="composer-tools">
          <select value={props.sourceMode} onChange={(event) => props.onSourceModeChange(event.target.value as SourceMode)} aria-label={copy.sourceMode} name="source-mode">
            <option value="auto">{copy.auto}</option>
            <option value="novel">{copy.novel}</option>
            <option value="non_novel">{copy.nonNovel}</option>
          </select>
          <div className="composer-actions">
            <input
              accept=".txt,.md,.json,.csv,.log,.html,.xml,.yaml,.yml,.docx,text/*,application/vnd.openxmlformats-officedocument.wordprocessingml.document"
              className="hidden"
              multiple
              onChange={(event) => props.onAddFiles(event.currentTarget.files)}
              ref={props.fileInputRef}
              type="file"
            />
            <Button disabled={composerLocked} onClick={() => props.fileInputRef.current?.click()} size="icon" title={copy.addFile} aria-label={copy.addFile}>
              <Plus size={17} />
            </Button>
            <Button onClick={props.onReset} size="icon" title={copy.reset} aria-label={copy.reset}>
              <RotateCcw size={16} />
            </Button>
            <Button disabled={props.isSendDisabled} onClick={props.onSend} title={copy.send} variant="primary">
              <Send size={16} />
              <span>{copy.send}</span>
            </Button>
          </div>
        </div>
      </div>
    </aside>
  );
}

function SelectionReference({ selection, onClear }: { selection?: SelectionContext | null; onClear: () => void }) {
  if (!selection?.selected_text) return null;
  return (
    <div className="selection-reference">
      <div className="selection-reference-head">
        <span>{copy.selection}</span>
        <button aria-label={copy.clearSelection} onClick={onClear} title={copy.clearSelection} type="button">
          <X size={13} />
        </button>
      </div>
      <div className="selection-reference-meta">{selectionLabel(selection)}</div>
      <p>{clipText(selection.selected_text, 82)}</p>
    </div>
  );
}

function selectionLabel(selection: SelectionContext) {
  const parts: string[] = [];
  if (selection.artifact_type === "script_unit" || selection.artifact_type === "scripts") {
    if (selection.episode_id !== undefined && selection.episode_id !== "") parts.push(`第 ${selection.episode_id} 集`);
    if (selection.selection_scope === "range") {
      if (selection.line_ids?.length) parts.push(`${selection.line_ids.length} 行`);
      if (selection.start_scene_id && selection.end_scene_id && selection.start_scene_id !== selection.end_scene_id) parts.push("跨场景");
    } else {
      if (selection.scene_id) parts.push(selection.scene_id);
      if (selection.block_type) parts.push(selection.block_type);
    }
  } else {
    parts.push(artifactTypeLabel(selection.artifact_type));
    if (selection.field_path) parts.push(selection.field_path);
  }
  return parts.length ? parts.join(" / ") : artifactTypeLabel(selection.artifact_type);
}

function artifactTypeLabel(type: SelectionContext["artifact_type"]) {
  const labels: Partial<Record<SelectionContext["artifact_type"], string>> = {
    source_input: "输入材料",
    story_bible: "故事圣经",
    episode_split: "原文拆集",
    material_bank: "素材库",
    story_seed: "故事种子",
    series_blueprint: "剧集蓝图",
    episode_cards: "分集卡",
    script_context: "剧本上下文",
    script_unit: "剧本",
    scripts: "剧本",
  };
  return labels[type] || type;
}

function clipText(text: string, limit: number) {
  const normalized = text.replace(/\s+/g, " ").trim();
  if (normalized.length <= limit) return normalized;
  return `${normalized.slice(0, limit)}...`;
}

function numberOrUndefined(value: string) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}

function MessageBubble({
  message,
  onApprove,
  onKeepDownstream,
  onPause,
  onRegenerateDownstream,
  onSubmitGenerationConfig,
  runStatus,
  activeApprovalID,
  openEvents,
  showActiveStep,
  isBusy,
}: {
  message: ChatMessage;
  onApprove: () => void;
  onKeepDownstream: () => void;
  onPause: () => void;
  onRegenerateDownstream: () => void;
  onSubmitGenerationConfig: (messageID: string, originalMessage: string, config: GenerationConfig, sourceMode: SourceMode, fileIDs: string[]) => void;
  runStatus?: RunStatus;
  activeApprovalID?: string | null;
  openEvents: () => void;
  showActiveStep: boolean;
  isBusy: boolean;
}) {
  const [textExpanded, setTextExpanded] = useState(false);
  const approvalIsActive =
    Boolean(message.approval?.approval_request_id) &&
    message.approval?.approval_request_id === activeApprovalID &&
    runStatus === "waiting_approval";
  const textLimit = message.role === "user" ? 240 : 420;
  const hasLongText = message.text.length > textLimit;
  const visibleText = hasLongText && !textExpanded ? `${message.text.slice(0, textLimit).trimEnd()}...` : message.text;

  return (
    <article className={`chat-message ${message.role}`}>
      <div className="chat-role">
        {message.role === "user" ? <User size={13} /> : <Bot size={13} />}
        <span>{message.role === "user" ? copy.you : "Agent"}</span>
      </div>
      {message.attachments?.length ? (
        <div className="message-attachments">
          {message.attachments.map((attachment) => (
            <span className="message-file-chip" key={attachment.file_name}>
              <FileText size={13} />
              <span>{attachment.file_name}</span>
            </span>
          ))}
        </div>
      ) : null}
      {message.role === "user" && message.selectionContext?.selected_text ? (
        <SentSelectionReference selection={message.selectionContext} />
      ) : null}
      <div className={`chat-text ${hasLongText && !textExpanded ? "collapsed" : ""}`}>{visibleText}</div>
      {hasLongText ? (
        <button className="message-expand-button" onClick={() => setTextExpanded((current) => !current)} type="button">
          {textExpanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
          <span>{textExpanded ? "收起长消息" : "展开完整内容"}</span>
        </button>
      ) : null}
      {message.steps?.length ? <StepList activeStep={showActiveStep ? message.activeStep : undefined} openEvents={openEvents} steps={message.steps} /> : null}
      {message.generationConfigPrompt ? (
        <GenerationConfigCard
          isBusy={isBusy}
          messageID={message.id}
          onSubmit={onSubmitGenerationConfig}
          prompt={message.generationConfigPrompt}
        />
      ) : null}
      {message.approval ? <ApprovalCard approval={message.approval} isActive={approvalIsActive} isBusy={isBusy} onApprove={onApprove} onKeepDownstream={onKeepDownstream} onPause={onPause} onRegenerateDownstream={onRegenerateDownstream} /> : null}
    </article>
  );
}

function SentSelectionReference({ selection }: { selection: SelectionContext }) {
  return (
    <div className="selection-reference sent" aria-label="已发送的选区引用">
      <div className="selection-reference-head">
        <span>引用选区</span>
        <span className="selection-reference-version">v{selection.version}</span>
      </div>
      <div className="selection-reference-meta">{selectionLabel(selection)}</div>
      <p>{clipText(selection.selected_text, 120)}</p>
    </div>
  );
}

function GenerationConfigCard({
  isBusy,
  messageID,
  prompt,
  onSubmit,
}: {
  isBusy: boolean;
  messageID: string;
  prompt: GenerationConfigPrompt;
  onSubmit: (messageID: string, originalMessage: string, config: GenerationConfig, sourceMode: SourceMode, fileIDs: string[]) => void;
}) {
  const [episodeCount, setEpisodeCount] = useState(prompt.defaults?.target_episode_count?.toString() || "");
  const [episodeMinutes, setEpisodeMinutes] = useState(prompt.defaults?.episode_duration_minutes?.toString() || "");
  const [preserveMarks, setPreserveMarks] = useState(Boolean(prompt.defaults?.preserve_existing_episode_marks));
  const isNovel = prompt.source_mode === "novel";
  const detectedEpisodeCount = prompt.defaults?.detected_episode_count || 0;
  const canPreserveMarks = isNovel && Boolean(prompt.defaults?.existing_episode_markers_detected) && detectedEpisodeCount > 0;

  return (
    <section className="generation-config-card">
      <div>
        <h2>{prompt.title || copy.configTitle}</h2>
        <p>{prompt.reason || copy.configReason}</p>
      </div>
      <div className="generation-config-grid">
        {preserveMarks && canPreserveMarks ? (
          <label>
            <span>原文分集</span>
            <input aria-label="已识别原文分集数" disabled type="text" value={`${detectedEpisodeCount} 集（已识别）`} />
          </label>
        ) : (
          <label>
            <span>{copy.episodes}</span>
            <input
              autoComplete="off"
              inputMode="numeric"
              min={1}
              name="target_episode_count"
              onChange={(event) => setEpisodeCount(event.currentTarget.value)}
              placeholder="请输入"
              type="number"
              value={episodeCount}
            />
          </label>
        )}
        <label>
          <span>{copy.minutes}</span>
          <input
            autoComplete="off"
            inputMode="decimal"
            min={0.5}
            name="episode_duration_minutes"
            onChange={(event) => setEpisodeMinutes(event.currentTarget.value)}
            placeholder="请输入"
            step={0.5}
            type="number"
            value={episodeMinutes}
          />
        </label>
      </div>
      {canPreserveMarks ? (
        <label className="config-check">
          <input checked={preserveMarks} onChange={(event) => setPreserveMarks(event.currentTarget.checked)} type="checkbox" />
          <span>{copy.preserve}</span>
        </label>
      ) : null}
      <div className="generation-config-actions">
        <Button
          disabled={isBusy}
          onClick={() =>
            onSubmit(messageID, prompt.original_message, {
              target_episode_count: preserveMarks && canPreserveMarks ? detectedEpisodeCount : numberOrUndefined(episodeCount),
              episode_duration_minutes: numberOrUndefined(episodeMinutes),
              preserve_existing_episode_marks: canPreserveMarks ? preserveMarks : undefined,
              existing_episode_markers_detected: canPreserveMarks || undefined,
              detected_episode_count: canPreserveMarks ? detectedEpisodeCount : undefined,
            }, prompt.source_mode, prompt.file_ids || [])
          }
          variant="primary"
        >
          <Play size={15} />
          {copy.confirmConfig}
        </Button>
      </div>
    </section>
  );
}

function StepList({ steps, activeStep, openEvents }: { steps: ExecutionStep[]; activeStep?: string; openEvents: () => void }) {
  const latestStep = steps[steps.length - 1];
  return (
    <div className={`agent-progress-summary ${activeStep ? "running" : "completed"}`}>
      <span className="agent-progress-icon" aria-hidden>
        {activeStep ? <Loader2 className="spin" size={14} /> : <CheckCircle2 size={15} />}
      </span>
      <span className="agent-progress-copy">
        <strong>{activeStep || `已完成 ${steps.length} 步`}</strong>
        <small>{activeStep ? `此前已完成 ${steps.length} 步` : latestStep ? `最后完成：${latestStep.label}` : ""}</small>
      </span>
      <button className="process-record-button" onClick={openEvents} type="button">
        <Activity size={14} />
        <span>{copy.events}</span>
      </button>
    </div>
  );
}

function ApprovalCard({
  approval,
  isActive,
  isBusy,
  onApprove,
  onKeepDownstream,
  onPause,
  onRegenerateDownstream,
}: {
  approval: ApprovalRequest;
  isActive: boolean;
  isBusy: boolean;
  onApprove: () => void;
  onKeepDownstream: () => void;
  onPause: () => void;
  onRegenerateDownstream: () => void;
}) {
  const isDisabled = !isActive || isBusy;
  const optionTypes = (approval.options || []).map((option) => typeof option === "string" ? option : option.type) as ApprovalAction[];
  const reviewsDownstream = optionTypes.includes("keep_downstream") || optionTypes.includes("regenerate_downstream");
  const affectedLabels = (approval.affected_artifacts || []).map((item) => {
    const artifactType = typeof item === "string" ? item : item.artifact_type;
    return artifactLabels[artifactType as keyof typeof artifactLabels] || artifactType;
  }).filter(Boolean);
  return (
    <section className={`inline-approval ${isActive && !isBusy ? "" : "inactive"}`}>
      <h2>{approval.title || copy.approveTitle}</h2>
      <p>{approval.reason || copy.approveReason}</p>
      {reviewsDownstream && affectedLabels.length ? <p className="approval-impact">{copy.affectedDownstream}{affectedLabels.join("、")}</p> : null}
      {isDisabled ? <p className="approval-history-note">{copy.inactiveApproval}</p> : null}
      <div className="approval-actions">
        {reviewsDownstream ? (
          <>
            <Button disabled={isDisabled} onClick={onKeepDownstream} variant="primary">
              <Play size={15} />
              {copy.keepDownstream}
            </Button>
            <Button disabled={isDisabled} onClick={onRegenerateDownstream}>
              <RotateCcw size={15} />
              {copy.regenerateDownstream}
            </Button>
          </>
        ) : (
          <Button disabled={isDisabled} onClick={onApprove} variant="primary">
            <Play size={15} />
            {copy.approve}
          </Button>
        )}
        <Button disabled={isDisabled} onClick={onPause}>
          <Pause size={15} />
          {copy.pause}
        </Button>
      </div>
    </section>
  );
}

function AttachmentTray({ attachments, remove }: { attachments: FileAttachment[]; remove: (index: number) => void }) {
  if (!attachments.length) return null;
  return (
    <div className="attachment-list">
      {attachments.map((attachment, index) => (
        <div className="attachment-item" key={`${attachment.file_name}:${index}`}>
          <FileText size={13} />
          <span className="attachment-name">{attachment.file_name}</span>
          <span className="attachment-meta">{formatFileSize(attachment.size)}</span>
          <Button className="attachment-remove" onClick={() => remove(index)} aria-label={copy.removeFile} size="icon" variant="ghost">
            <span aria-hidden>×</span>
          </Button>
        </div>
      ))}
    </div>
  );
}
