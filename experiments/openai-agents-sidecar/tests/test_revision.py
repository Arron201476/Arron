import asyncio

from agents import ApplyPatchOperation

from content_agent_sidecar.contracts import ArtifactRevisionRequest
from content_agent_sidecar.revision import (
    ArtifactPatchPlan,
    ArtifactPatchReplacement,
    ArtifactPatchEditor,
    allows_text_mirror_sync,
    allowed_change_prefixes,
    changed_paths,
    exact_replacement_diff,
    field_path_prefix,
    field_path_value,
    narrow_patch_plan_to_single_line,
    normalize_patch_hunks,
    path_replacement_diff,
    path_matches_prefix,
    structural_replacement_diff,
)


def test_artifact_patch_plan_requires_diff_and_summary() -> None:
    plan = ArtifactPatchPlan(old_text="旧结尾", new_text="新结尾", summary="已修改结尾。")
    assert plan.old_text == "旧结尾"
    assert plan.new_text == "新结尾"
    assert plan.summary == "已修改结尾。"


def test_artifact_patch_plan_supports_no_change() -> None:
    plan = ArtifactPatchPlan(action="no_change", summary="保持当前版本。")
    assert plan.action == "no_change"
    assert plan.old_text == ""
    assert plan.new_text == ""


def test_artifact_patch_plan_supports_multiple_minimal_replacements() -> None:
    plan = ArtifactPatchPlan(
        replacements=[
            ArtifactPatchReplacement(old_text="旧一", new_text="新一"),
            ArtifactPatchReplacement(old_text="旧二", new_text="新二"),
        ],
        summary="同步修改两集。",
    )

    assert [(item.old_text, item.new_text) for item in plan.replacements] == [
        ("旧一", "新一"),
        ("旧二", "新二"),
    ]


def test_path_replacement_diff_updates_only_explicit_field() -> None:
    payload = {
        "episodes": [
            {"episode_no": 1, "summary": "旧一", "hook": "高"},
            {"episode_no": 2, "summary": "旧二", "hook": "高"},
        ]
    }
    editor = ArtifactPatchEditor(payload)
    diff = path_replacement_diff(payload, "/episodes/1/hook", "高", "低")

    result = asyncio.run(
        editor.update_file(
            ApplyPatchOperation(type="update_file", path="artifact.json", diff=diff)
        )
    )

    assert result.status == "completed"
    assert editor.payload()["episodes"][0]["hook"] == "高"
    assert editor.payload()["episodes"][1]["hook"] == "低"


def test_path_replacement_diff_supports_structured_values() -> None:
    payload = {"episodes": [{"episode_no": 1, "beats": ["旧节拍"]}]}
    diff = path_replacement_diff(
        payload,
        "/episodes/0/beats",
        '["旧节拍"]',
        '["新节拍", "新钩子"]',
    )
    editor = ArtifactPatchEditor(payload)
    result = asyncio.run(
        editor.update_file(
            ApplyPatchOperation(type="update_file", path="artifact.json", diff=diff)
        )
    )

    assert result.status == "completed"
    assert editor.payload()["episodes"][0]["beats"] == ["新节拍", "新钩子"]


def test_path_replacement_diff_rejects_stale_old_value() -> None:
    payload = {"episodes": [{"episode_no": 1, "hook": "高"}]}

    try:
        path_replacement_diff(payload, "/episodes/0/hook", "中", "低")
    except Exception as exc:
        assert "old value does not match" in str(exc)
    else:
        raise AssertionError("stale old value must be rejected")


def test_patch_plan_is_narrowed_from_context_lines_to_one_changed_line() -> None:
    editor = ArtifactPatchEditor(
        {
            "scenes": [{"blocks": [{"text": "△旧结尾"}]}],
            "script_text": "上一句\n△旧结尾",
        }
    )
    plan = ArtifactPatchPlan(
        old_text="上一句\n△旧结尾",
        new_text="上一句\n△新结尾",
        summary="已修改结尾。",
    )

    narrowed = narrow_patch_plan_to_single_line(editor._text, plan)

    assert narrowed.old_text == "△旧结尾"
    assert narrowed.new_text == "△新结尾"


def test_normalize_patch_hunks_removes_fences_and_file_headers() -> None:
    value = "```diff\n--- artifact.json\n+++ artifact.json\n@@\n-old\n+new\n```"
    assert normalize_patch_hunks(value) == "@@\n-old\n+new\n"


def test_exact_replacement_diff_is_accepted_by_patch_editor() -> None:
    editor = ArtifactPatchEditor({"content": "旧结尾"})
    diff = exact_replacement_diff(editor._text, "旧结尾", "新结尾")
    result = asyncio.run(
        editor.update_file(
            ApplyPatchOperation(type="update_file", path="artifact.json", diff=diff)
        )
    )
    assert result.status == "completed"
    assert editor.payload()["content"] == "新结尾"


def test_exact_replacement_diff_updates_synchronized_text_copies() -> None:
    editor = ArtifactPatchEditor(
        {
            "scenes": [{"blocks": [{"text": "旧结尾"}]}],
            "script_text": "开场\n旧结尾",
        }
    )
    diff = exact_replacement_diff(
        editor._text,
        "旧结尾",
        "新结尾",
        allow_synchronized_copies=True,
    )
    result = asyncio.run(
        editor.update_file(
            ApplyPatchOperation(type="update_file", path="artifact.json", diff=diff)
        )
    )

    assert result.status == "completed"
    assert editor.payload()["scenes"][0]["blocks"][0]["text"] == "新结尾"
    assert editor.payload()["script_text"].endswith("新结尾")


def test_structural_replacement_diff_accepts_compact_json_subtree() -> None:
    payload = {
        "episodes": [
            {"episode_id": 1, "summary": "旧一"},
            {"episode_id": 2, "summary": "旧二"},
        ],
        "coverage": {"complete": True},
    }
    editor = ArtifactPatchEditor(payload)
    diff = structural_replacement_diff(
        payload,
        '[{"episode_id":1,"summary":"旧一"},{"episode_id":2,"summary":"旧二"}]',
        '[{"episode_id":1,"summary":"新一"},{"episode_id":2,"summary":"新二"}]',
    )

    assert diff is not None
    result = asyncio.run(
        editor.update_file(
            ApplyPatchOperation(type="update_file", path="artifact.json", diff=diff)
        )
    )
    assert result.status == "completed"
    assert [item["summary"] for item in editor.payload()["episodes"]] == ["新一", "新二"]
    assert editor.payload()["coverage"] == {"complete": True}


def test_text_field_path_allows_only_verified_script_text_mirror() -> None:
    request = ArtifactRevisionRequest(
        revision_request_id="rr_1",
        revision_attempt_id="rra_1",
        instruction="只修改结尾动作",
        target={"field_path": "scenes[0].blocks[0].text"},
        artifact_schema={},
        artifact_payload={
            "scenes": [{"blocks": [{"text": "旧结尾"}]}],
            "script_text": "开场\n旧结尾",
        },
    )

    assert allows_text_mirror_sync(request, "旧结尾") is True
    assert allows_text_mirror_sync(request, "开场") is False


def test_script_text_field_path_allows_verified_structured_mirror() -> None:
    request = ArtifactRevisionRequest(
        revision_request_id="rr_1",
        revision_attempt_id="rra_1",
        instruction="把这句改成新的说法",
        target={"field_path": "script_text"},
        artifact_schema={},
        artifact_payload={
            "scenes": [{"blocks": [{"text": "旧结尾"}]}],
            "script_text": "开场\n旧结尾",
        },
    )
    assert allows_text_mirror_sync(request, "旧结尾") is True


def test_json_pointer_collection_allows_verified_structured_mirror() -> None:
    request = ArtifactRevisionRequest(
        revision_request_id="rr_1",
        revision_attempt_id="rra_1",
        instruction="只修改场景中的这一句",
        target={"field_path": "/scenes"},
        artifact_schema={},
        artifact_payload={
            "scenes": [{"blocks": [{"text": "旧结尾"}]}],
            "script_text": "开场\n旧结尾",
        },
    )

    assert field_path_prefix("/scenes", request.artifact_payload) == "/scenes"
    assert field_path_value(request.artifact_payload, "/scenes") == request.artifact_payload["scenes"]
    assert allows_text_mirror_sync(request, "旧结尾") is True


def test_artifact_patch_editor_applies_minimal_update() -> None:
    editor = ArtifactPatchEditor({"episodes": [{"episode_no": 1, "hook": "high"}, {"episode_no": 3, "hook": "high"}]})
    result = asyncio.run(
        editor.update_file(
            ApplyPatchOperation(
                type="update_file",
                path="artifact.json",
                diff='@@\n       "episode_no": 3,\n-      "hook": "high"\n+      "hook": "medium"\n',
            )
        )
    )
    assert result.status == "completed"
    assert editor.payload()["episodes"][0]["hook"] == "high"
    assert editor.payload()["episodes"][1]["hook"] == "medium"


def test_revision_scope_uses_selected_field_path() -> None:
    request = ArtifactRevisionRequest(
        revision_request_id="rr_1",
        revision_attempt_id="rra_1",
        instruction="改为中",
        target={"field_path": "episodes[1].hook"},
        artifact_schema={},
        artifact_payload={"episodes": [{"hook": "high"}, {"hook": "high"}]},
    )
    assert allowed_change_prefixes(request) == ["/episodes/1/hook"]


def test_revision_scope_normalizes_payload_wrapper_from_target_path() -> None:
    request = ArtifactRevisionRequest(
        revision_request_id="rr_1",
        revision_attempt_id="rra_1",
        instruction="修改正文",
        target={"field_path": "payload.content"},
        artifact_schema={},
        artifact_payload={"artifact_label": "工作稿", "content": "原正文"},
    )

    assert field_path_prefix("payload.content", request.artifact_payload) == "/content"
    assert field_path_value(request.artifact_payload, "payload.content") == "原正文"
    assert allowed_change_prefixes(request) == ["/content"]


def test_revision_scope_keeps_real_payload_property() -> None:
    payload = {"payload": {"content": "原正文"}}
    assert field_path_prefix("payload.content", payload) == "/payload/content"
    assert field_path_value(payload, "payload.content") == "原正文"


def test_revision_scope_infers_episode_from_instruction() -> None:
    payload = {"episodes": [{"episode_no": 1, "hook": "high"}, {"episode_no": 3, "hook": "high"}]}
    request = ArtifactRevisionRequest(
        revision_request_id="rr_1",
        revision_attempt_id="rra_1",
        instruction="把第3集钩子改成中",
        target={},
        artifact_schema={},
        artifact_payload=payload,
    )
    assert allowed_change_prefixes(request) == ["/episodes/1"]
    changed = {"episodes": [{"episode_no": 1, "hook": "high"}, {"episode_no": 3, "hook": "medium"}]}
    assert changed_paths(payload, changed) == ["/episodes/1/hook"]


def test_revision_scope_infers_all_episodes_from_instruction() -> None:
    payload = {
        "episodes": [
            {"episode_no": 1, "hook": "high"},
            {"episode_no": 2, "hook": "high"},
            {"episode_no": 3, "hook": "high"},
        ]
    }
    request = ArtifactRevisionRequest(
        revision_request_id="rr_1",
        revision_attempt_id="rra_1",
        instruction="同步调整第1集和第2集的边界",
        target={},
        artifact_schema={},
        artifact_payload=payload,
    )

    assert allowed_change_prefixes(request) == ["/episodes/0", "/episodes/1"]


def test_root_scope_prefix_allows_artifact_child_paths() -> None:
    assert path_matches_prefix("/scenes/2/blocks/10/text", "/") is True
