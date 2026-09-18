from __future__ import annotations

import asyncio
from copy import deepcopy
import difflib
import json
import re
from typing import Any, Literal

from pydantic import BaseModel, Field
from jsonschema import Draft202012Validator
from jsonschema.exceptions import SchemaError, ValidationError
from referencing import Registry
from referencing.exceptions import NoSuchResource, Unresolvable

from agents import (
    Agent,
    ApplyPatchOperation,
    ApplyPatchResult,
    ApplyPatchTool,
    ModelSettings,
    Runner,
)
from agents.apply_diff import apply_diff

from .contracts import ArtifactRevisionRequest, ArtifactRevisionResponse, ArtifactValidationContract
from .observability import privacy_safe_run_config
from .runtime import OpenAIAgentsRuntime, RuntimeCompatibilityError, normalize_provider_error


ARTIFACT_PATH = "artifact.json"
# The compatible company gateway cannot execute the hosted ApplyPatchTool shape.
# Keep patching inside the SDK Runner through a function tool backed by apply_diff.
_native_apply_patch_supported: bool | None = False


class ArtifactPatchReplacement(BaseModel):
    path: str = Field(
        default="",
        description="Exact JSON Pointer path of the value to replace, for example /episodes/0/summary",
    )
    old_text: str = Field(description="Exact existing text or JSON subtree to replace")
    new_text: str = Field(description="Replacement text or JSON subtree")


class ArtifactPatchPlan(BaseModel):
    action: Literal["patch", "no_change"] = Field(
        default="patch",
        description="patch when content must change; no_change when the instruction requests preserving the current content",
    )
    old_text: str = Field(default="", description="Exact existing text substring to replace once")
    new_text: str = Field(default="", description="Replacement text substring")
    replacements: list[ArtifactPatchReplacement] = Field(
        default_factory=list,
        description="All independent minimal replacements needed for a multi-target request",
    )
    summary: str = Field(description="One short Chinese summary of the change")


def normalize_patch_hunks(diff: str) -> str:
    lines = diff.strip().splitlines()
    while lines and lines[0].strip().startswith("```"):
        lines.pop(0)
    while lines and lines[-1].strip() == "```":
        lines.pop()
    for index, line in enumerate(lines):
        if line.startswith("@@"):
            return "\n".join(lines[index:]).rstrip() + "\n"
    return diff


def exact_replacement_diff(
    text: str, old_text: str, new_text: str, *, allow_synchronized_copies: bool = False
) -> str:
    occurrence_count = text.count(old_text) if old_text else 0
    if occurrence_count != 1 and not (
        allow_synchronized_copies and 1 < occurrence_count <= 4
    ):
        raise RuntimeCompatibilityError(
            "Agents SDK revision old_text must match exactly once "
            f"(matches={occurrence_count}, synchronized={allow_synchronized_copies})"
        )
    updated = text.replace(
        old_text,
        new_text,
        occurrence_count if allow_synchronized_copies else 1,
    )
    diff = "".join(
        difflib.unified_diff(
            text.splitlines(keepends=True),
            updated.splitlines(keepends=True),
            fromfile=ARTIFACT_PATH,
            tofile=ARTIFACT_PATH,
            n=3,
        )
    )
    return normalize_patch_hunks(diff)


def structural_replacement_diff(
    payload: dict[str, Any], old_text: str, new_text: str
) -> str | None:
    """Replace one JSON subtree when provider formatting prevents text matching."""
    try:
        old_value = json.loads(old_text)
        new_value = json.loads(new_text)
    except json.JSONDecodeError:
        return None

    matches: list[tuple[str | int, ...]] = []

    def visit(value: Any, path: tuple[str | int, ...] = ()) -> None:
        if value == old_value:
            matches.append(path)
        if isinstance(value, dict):
            for key, child in value.items():
                visit(child, path + (key,))
        elif isinstance(value, list):
            for index, child in enumerate(value):
                visit(child, path + (index,))

    visit(payload)
    if len(matches) != 1 or not matches[0]:
        return None

    updated = deepcopy(payload)
    parent: Any = updated
    for token in matches[0][:-1]:
        parent = parent[token]
    parent[matches[0][-1]] = new_value
    before = json.dumps(payload, ensure_ascii=False, indent=2) + "\n"
    after = json.dumps(updated, ensure_ascii=False, indent=2) + "\n"
    if before == after:
        return None
    diff = "".join(
        difflib.unified_diff(
            before.splitlines(keepends=True),
            after.splitlines(keepends=True),
            fromfile=ARTIFACT_PATH,
            tofile=ARTIFACT_PATH,
            n=3,
        )
    )
    return normalize_patch_hunks(diff)


def path_replacement_diff(
    payload: dict[str, Any], path: str, old_text: str, new_text: str
) -> str:
    """Replace one value at an explicit path after verifying its current value."""
    tokens = field_path_tokens(path, payload)
    if not tokens:
        raise RuntimeCompatibilityError("Agents SDK revision replacement path is empty")

    current = field_path_value(payload, path)
    if current is None:
        raise RuntimeCompatibilityError(
            f"Agents SDK revision replacement path does not exist: {path}"
        )

    try:
        expected = json.loads(old_text)
    except json.JSONDecodeError:
        expected = old_text
    if current != expected:
        raise RuntimeCompatibilityError(
            f"Agents SDK revision replacement old value does not match path: {path}"
        )

    try:
        replacement = json.loads(new_text)
    except json.JSONDecodeError:
        replacement = new_text

    updated = deepcopy(payload)
    parent: Any = updated
    for token in tokens[:-1]:
        if isinstance(parent, dict):
            parent = parent[token]
        elif isinstance(parent, list) and token.isdigit():
            parent = parent[int(token)]
        else:
            raise RuntimeCompatibilityError(
                f"Agents SDK revision replacement path is invalid: {path}"
            )
    final = tokens[-1]
    if isinstance(parent, dict):
        parent[final] = replacement
    elif isinstance(parent, list) and final.isdigit() and int(final) < len(parent):
        parent[int(final)] = replacement
    else:
        raise RuntimeCompatibilityError(
            f"Agents SDK revision replacement path is invalid: {path}"
        )

    before = json.dumps(payload, ensure_ascii=False, indent=2) + "\n"
    after = json.dumps(updated, ensure_ascii=False, indent=2) + "\n"
    if before == after:
        raise RuntimeCompatibilityError("Agents SDK revision replacement made no change")
    diff = "".join(
        difflib.unified_diff(
            before.splitlines(keepends=True),
            after.splitlines(keepends=True),
            fromfile=ARTIFACT_PATH,
            tofile=ARTIFACT_PATH,
            n=3,
        )
    )
    return normalize_patch_hunks(diff)


class ArtifactPatchEditor:
    """In-memory SDK apply_patch editor for one immutable Artifact base version."""

    def __init__(self, payload: dict[str, Any]) -> None:
        self._base = payload
        self._text = json.dumps(payload, ensure_ascii=False, indent=2) + "\n"
        self.operations: list[ApplyPatchOperation] = []

    async def create_file(self, operation: ApplyPatchOperation) -> ApplyPatchResult:
        return ApplyPatchResult(status="failed", output="Creating Artifact files is not allowed")

    async def delete_file(self, operation: ApplyPatchOperation) -> ApplyPatchResult:
        return ApplyPatchResult(status="failed", output="Deleting Artifact files is not allowed")

    async def update_file(self, operation: ApplyPatchOperation) -> ApplyPatchResult:
        if operation.path != ARTIFACT_PATH or operation.move_to is not None:
            return ApplyPatchResult(status="failed", output="Only artifact.json may be updated")
        if not operation.diff:
            return ApplyPatchResult(status="failed", output="The patch diff is empty")
        try:
            normalized_diff = normalize_patch_hunks(operation.diff)
            updated = apply_diff(self._text, normalized_diff, mode="default")
            decoded = json.loads(updated)
        except (ValueError, json.JSONDecodeError) as exc:
            return ApplyPatchResult(status="failed", output=f"Invalid Artifact patch: {exc}")
        if not isinstance(decoded, dict):
            return ApplyPatchResult(status="failed", output="Artifact payload must remain a JSON object")
        self._text = json.dumps(decoded, ensure_ascii=False, indent=2) + "\n"
        self.operations.append(
            operation.model_copy(update={"diff": normalized_diff})
            if hasattr(operation, "model_copy")
            else operation
        )
        return ApplyPatchResult(status="completed", output="Updated artifact.json")

    def payload(self) -> dict[str, Any]:
        value = json.loads(self._text)
        if not isinstance(value, dict):
            raise RuntimeCompatibilityError("patched Artifact is not a JSON object")
        return value


def changed_paths(before: Any, after: Any, prefix: str = "") -> list[str]:
    if type(before) is not type(after):
        return [prefix or "/"]
    if isinstance(before, dict):
        result: list[str] = []
        for key in sorted(set(before) | set(after)):
            child = f"{prefix}/{str(key).replace('~', '~0').replace('/', '~1')}"
            if key not in before or key not in after:
                result.append(child)
            else:
                result.extend(changed_paths(before[key], after[key], child))
        return result
    if isinstance(before, list):
        result = []
        for index in range(max(len(before), len(after))):
            child = f"{prefix}/{index}"
            if index >= len(before) or index >= len(after):
                result.append(child)
            else:
                result.extend(changed_paths(before[index], after[index], child))
        return result
    return [] if before == after else [prefix or "/"]


def field_path_tokens(field_path: str, payload: Any | None = None) -> list[str]:
    if field_path.startswith("/"):
        tokens = [
            token.replace("~1", "/").replace("~0", "~")
            for token in field_path.split("/")[1:]
        ]
    else:
        tokens = re.findall(r"[^.\[\]]+", field_path)
    if (
        tokens
        and tokens[0].strip() == "payload"
        and isinstance(payload, dict)
        and "payload" not in payload
    ):
        tokens = tokens[1:]
    return [token.strip() for token in tokens if token.strip()]


def field_path_prefix(field_path: str, payload: Any | None = None) -> str:
    tokens = field_path_tokens(field_path, payload)
    parts: list[str] = []
    for token in tokens:
        parts.append(token.replace("~", "~0").replace("/", "~1"))
    return "/" + "/".join(parts) if parts else ""


def field_path_value(payload: Any, field_path: str) -> Any:
    value = payload
    tokens = field_path_tokens(field_path, payload)
    for token in tokens:
        if isinstance(value, dict):
            if token not in value:
                return None
            value = value[token]
        elif isinstance(value, list) and token.isdigit():
            index = int(token)
            if index >= len(value):
                return None
            value = value[index]
        else:
            return None
    return value


def allows_text_mirror_sync(request: ArtifactRevisionRequest, old_text: str) -> bool:
    field_path = str(request.target.get("field_path") or "").strip()
    if not field_path:
        return False
    target_value = field_path_value(request.artifact_payload, field_path)
    script_text = request.artifact_payload.get("script_text")
    if target_value is None or not isinstance(script_text, str):
        return False
    structured_payload = {
        key: value
        for key, value in request.artifact_payload.items()
        if key != "script_text"
    }
    return (
        bool(old_text)
        and _string_occurrence_count(target_value, old_text) == 1
        and script_text.count(old_text) == 1
        and _string_occurrence_count(structured_payload, old_text) == 1
        and ArtifactPatchEditor(request.artifact_payload)._text.count(old_text) == 2
    )


def _string_occurrence_count(value: Any, needle: str) -> int:
    if isinstance(value, str):
        return value.count(needle)
    if isinstance(value, dict):
        return sum(_string_occurrence_count(item, needle) for item in value.values())
    if isinstance(value, list):
        return sum(_string_occurrence_count(item, needle) for item in value)
    return 0


def narrow_patch_plan_to_single_line(
    text: str, plan: ArtifactPatchPlan
) -> ArtifactPatchPlan:
    if text.count(plan.old_text) > 0:
        return plan
    old_lines = plan.old_text.splitlines()
    new_lines = plan.new_text.splitlines()
    while old_lines and new_lines and old_lines[0] == new_lines[0]:
        old_lines.pop(0)
        new_lines.pop(0)
    while old_lines and new_lines and old_lines[-1] == new_lines[-1]:
        old_lines.pop()
        new_lines.pop()
    if (
        len(old_lines) == 1
        and len(new_lines) == 1
        and text.count(old_lines[0]) > 0
    ):
        return plan.model_copy(
            update={"old_text": old_lines[0], "new_text": new_lines[0]}
        )
    return plan


def episode_prefixes(payload: Any, episode_no: int, prefix: str = "") -> list[str]:
    result: list[str] = []
    if isinstance(payload, dict):
        if payload.get("episode_no") == episode_no or payload.get("episode_number") == episode_no:
            result.append(prefix or "/")
        for key, value in payload.items():
            child = f"{prefix}/{str(key).replace('~', '~0').replace('/', '~1')}"
            result.extend(episode_prefixes(value, episode_no, child))
    elif isinstance(payload, list):
        for index, value in enumerate(payload):
            result.extend(episode_prefixes(value, episode_no, f"{prefix}/{index}"))
    return result


def allowed_change_prefixes(request: ArtifactRevisionRequest) -> list[str]:
    field_path = str(request.target.get("field_path") or "").strip()
    if field_path:
        prefix = field_path_prefix(field_path, request.artifact_payload)
        if prefix:
            return [prefix]
    episode_numbers = {
        int(value)
        for value in re.findall(r"(?:第\s*)?(\d+)\s*集", request.instruction)
    }
    if episode_numbers:
        prefixes: list[str] = []
        for episode_no in sorted(episode_numbers):
            prefixes.extend(episode_prefixes(request.artifact_payload, episode_no))
        return list(dict.fromkeys(prefixes))
    return []


def path_matches_prefix(path: str, prefix: str) -> bool:
    return prefix == "/" or path == prefix or path.startswith(prefix + "/")


def _deny_revision_schema_resource(uri: str) -> Any:
    raise NoSuchResource(ref=uri)


def revision_output_validator(contract: ArtifactValidationContract | None) -> Draft202012Validator | None:
    if contract is None:
        return None
    if bool(contract.output_key) != bool(contract.output_bundle) or (
        contract.output_key and contract.output_key not in contract.output_bundle
    ):
        raise RuntimeCompatibilityError("Revision output bundle does not contain the edited output")
    try:
        Draft202012Validator.check_schema(contract.response_schema)
    except SchemaError as exc:
        raise RuntimeCompatibilityError("Revision output schema is invalid") from exc
    return Draft202012Validator(
        contract.response_schema, registry=Registry(retrieve=_deny_revision_schema_resource)
    )


def validate_revision_proposal(
    validator: Draft202012Validator | None,
    contract: ArtifactValidationContract | None,
    proposal: dict[str, Any],
) -> None:
    if validator is None or contract is None:
        return
    candidate = proposal
    if contract.output_key:
        candidate = deepcopy(contract.output_bundle)
        candidate[contract.output_key] = proposal
    try:
        validator.validate(candidate)
    except (ValidationError, Unresolvable) as exc:
        raise RuntimeCompatibilityError("Revision proposal violates the frozen output contract") from exc


async def execute_artifact_revision(
    runtime: OpenAIAgentsRuntime, request: ArtifactRevisionRequest
) -> ArtifactRevisionResponse:
    global _native_apply_patch_supported
    output_validator = revision_output_validator(request.artifact_validation)
    editor = ArtifactPatchEditor(request.artifact_payload)
    synchronized_replacement = False
    applied_plan: ArtifactPatchPlan | None = None
    instructions = (
        "你只修改 artifact.json 中 target 指向的范围。"
        "必须调用局部 patch 工具产生最小 unified diff，禁止重写无关字段、调整顺序或生成新设定。"
        "不得创建、删除或移动文件。工具成功后，只输出一句简短中文修改摘要。"
        "若提供 artifact_validation，修改后的完整结果必须满足 response_schema。"
        "output_key 非空时，只替换 output_bundle 中该键的产物后校验整组；其他产物只读。"
        "多处修改可以分步完成，但最终结果不得缺失必填字段或违反类型和关联约束。"
    )
    prompt = json.dumps(
        {
            "instruction": request.instruction,
            "target": request.target,
            "artifact_schema": request.artifact_schema,
            "artifact_validation": request.artifact_validation.model_dump() if request.artifact_validation else None,
            "file": ARTIFACT_PATH,
            "artifact_json": request.artifact_payload,
        },
        ensure_ascii=False,
    )
    result: Any | None = None
    if _native_apply_patch_supported is not False:
        native_agent = Agent(
            name="Artifact 局部修改 Agent",
            instructions=instructions,
            model=runtime.model,
            tools=[ApplyPatchTool(editor=editor)],
            model_settings=ModelSettings(
                max_tokens=runtime.orchestration_max_tokens,
                tool_choice="required",
            ),
        )
        try:
            result = await asyncio.wait_for(
                Runner.run(
                    native_agent,
                    prompt,
                    max_turns=4,
                    run_config=privacy_safe_run_config(
                        workflow_name="content-agent-artifact-revision",
                        tracing_enabled=runtime.settings.tracing_enabled,
                        release_id=runtime.settings.release_id,
                    ),
                ),
                timeout=runtime.settings.run_timeout_seconds,
            )
            _native_apply_patch_supported = True
        except TimeoutError as exc:
            raise RuntimeCompatibilityError("Agents SDK revision timed out") from exc
        except Exception as exc:
            normalized = normalize_provider_error(exc)
            if "Tool choice 'required' must be specified with 'tools'" not in normalized:
                raise RuntimeCompatibilityError(normalized) from exc
            _native_apply_patch_supported = False
            editor = ArtifactPatchEditor(request.artifact_payload)

    if not editor.operations:
        plan_agent = Agent(
            name="Artifact 局部补丁规划 Agent",
            instructions=(
                instructions
                + " 当前兼容网关无法可靠发出函数工具调用。"
                "先判断用户是否真的要求改变内容。若用户要求保持原样、不作任何修改，"
                "action 必须为 no_change，old_text 和 new_text 留空；不得伪造修改。"
                "只有确实要求改变内容时 action 才为 patch，"
                "单点修改可返回 old_text 和 new_text；涉及多个字段、多个实体或多个集数时，"
                "必须在 replacements 中返回全部独立的最小替换，不得只改第一处。"
                "每条 replacement 必须优先提供精确 JSON Pointer path（例如 /episodes/0/summary），"
                "old_text 是该路径当前值，new_text 是修改后的值；对象或数组值必须用 JSON 表示。"
                "每条 old_text 必须是 artifact_json 中原样存在且只出现一次的最小字符串片段，"
                "也可以是与 artifact_json 中唯一子树结构相同的 JSON；"
                "不要包含 JSON 字段名、Markdown 代码围栏或 diff 标记。"
                "Sidecar 会机械生成 unified diff，并使用 Agents SDK apply_diff 执行。"
            ),
            model=runtime.model,
            output_type=ArtifactPatchPlan,
            model_settings=ModelSettings(max_tokens=runtime.orchestration_max_tokens),
        )
        try:
            result = await asyncio.wait_for(
                Runner.run(
                    plan_agent,
                    prompt,
                    max_turns=2,
                    run_config=privacy_safe_run_config(
                        workflow_name="content-agent-artifact-revision-plan",
                        tracing_enabled=runtime.settings.tracing_enabled,
                        release_id=runtime.settings.release_id,
                    ),
                ),
                timeout=runtime.settings.run_timeout_seconds,
            )
        except TimeoutError as exc:
            raise RuntimeCompatibilityError("Agents SDK revision planning timed out") from exc
        except Exception as exc:
            raise RuntimeCompatibilityError(normalize_provider_error(exc)) from exc
        plan = result.final_output
        if not isinstance(plan, ArtifactPatchPlan):
            raise RuntimeCompatibilityError("Agents SDK revision did not return a patch plan")
        if plan.action == "no_change":
            return ArtifactRevisionResponse(
                outcome="no_change",
                proposal_payload=request.artifact_payload,
                proposal_summary=plan.summary.strip() or "当前内容无需修改。",
                provider_id=runtime.settings.model_name,
                trace_ref="",
                changed_paths=[],
            )
        applied_plan = plan
        field_path = str(request.target.get("field_path") or "").strip()
        replacements = plan.replacements or [
            ArtifactPatchReplacement(old_text=plan.old_text, new_text=plan.new_text)
        ]
        for replacement in replacements:
            if replacement.path.strip():
                replacement_diff = path_replacement_diff(
                    editor.payload(),
                    replacement.path.strip(),
                    replacement.old_text,
                    replacement.new_text,
                )
                patch_result = await editor.update_file(
                    ApplyPatchOperation(
                        type="update_file", path=ARTIFACT_PATH, diff=replacement_diff
                    )
                )
                if patch_result.status == "failed":
                    raise RuntimeCompatibilityError(
                        patch_result.output or "Agents SDK revision path patch failed"
                    )
                continue
            current_plan = narrow_patch_plan_to_single_line(
                editor._text,
                ArtifactPatchPlan(
                    old_text=replacement.old_text,
                    new_text=replacement.new_text,
                    summary=plan.summary,
                ),
            )
            mirror_sync = allows_text_mirror_sync(request, current_plan.old_text)
            synchronized_replacement = synchronized_replacement or (
                editor._text.count(current_plan.old_text) > 1
            )
            try:
                replacement_diff = exact_replacement_diff(
                    editor._text,
                    current_plan.old_text,
                    current_plan.new_text,
                    allow_synchronized_copies=mirror_sync,
                )
            except RuntimeCompatibilityError:
                replacement_diff = structural_replacement_diff(
                    editor.payload(), current_plan.old_text, current_plan.new_text
                )
                if replacement_diff is None:
                    raise
            patch_result = await editor.update_file(
                ApplyPatchOperation(
                    type="update_file", path=ARTIFACT_PATH, diff=replacement_diff
                )
            )
            if patch_result.status == "failed":
                raise RuntimeCompatibilityError(
                    patch_result.output or "Agents SDK revision patch plan failed"
                )
    if not editor.operations:
        raise RuntimeCompatibilityError("Agents SDK revision did not apply a patch")
    proposal = editor.payload()
    validate_revision_proposal(output_validator, request.artifact_validation, proposal)
    paths = changed_paths(request.artifact_payload, proposal)
    if not paths:
        raise RuntimeCompatibilityError("Agents SDK revision produced no changes")
    allowed = allowed_change_prefixes(request)
    if synchronized_replacement:
        allowed.append("/script_text")
    if allowed and any(
        not any(path_matches_prefix(path, prefix) for prefix in allowed)
        for path in paths
    ):
        raise RuntimeCompatibilityError(
            "Agents SDK revision changed content outside the resolved target"
        )
    if synchronized_replacement and any(
        path != "/script_text" and not path.endswith("/text") for path in paths
    ):
        raise RuntimeCompatibilityError(
            "Agents SDK synchronized revision changed non-text mirror fields"
        )
    if applied_plan is not None:
        summary = applied_plan.summary
    else:
        summary = result.final_output if isinstance(result.final_output, str) else ""
    trace_ref = ""
    raw_responses = getattr(result, "raw_responses", None) or []
    if raw_responses:
        trace_ref = str(getattr(raw_responses[-1], "response_id", "") or "")
    return ArtifactRevisionResponse(
        outcome="proposed",
        proposal_payload=proposal,
        proposal_summary=summary.strip() or "已按要求生成局部修改稿。",
        provider_id=runtime.settings.model_name,
        trace_ref=trace_ref,
        changed_paths=paths,
    )
