import asyncio
from copy import deepcopy
import hashlib
import io
import json
import tarfile

import pytest
from agents import Agent, RunState

from content_agent_sidecar.agent_tools import AgentToolProvider
from content_agent_sidecar.guardrails import SDKGuardrailPolicy
from content_agent_sidecar.managed_instructions import bind_managed_instructions
from content_agent_sidecar.native_publication import prepare_workspace_publication, publish_workspace_files
from content_agent_sidecar.native_runner import bind_native_agent
from content_agent_sidecar.native_snapshot import SnapshotReference
from content_agent_sidecar.project_skills import install_workspace_skill, validate_workspace_skill
from content_agent_sidecar.runtime import load_skill_instructions, read_skill_resource
from test_native_manifest import SKILL
from test_native_patch_tool import response as patch_response
from test_native_publication import publication_setup
from test_native_workflow_boundaries import rebuilt_context, run_boundary
from test_stateful_execution import StreamingSequence, final_item, tool_item


FILES = {
    "SKILL.md": "---\nname: outline-review\ndescription: Review an outline for unsupported character decisions.\n---\nRead references/checklist.md and report evidence before suggestions.",
    "references/checklist.md": "Check each character decision against an established motive.",
    "assets/example.txt": "A reversal needs a prior causal condition.",
}
SELECTION = [{"source_path": "authored/" + path, "path": "draft/outline-review/" + path} for path in FILES]
TOOLS = [prepare_workspace_publication, publish_workspace_files, validate_workspace_skill,
         install_workspace_skill, load_skill_instructions, read_skill_resource]


class AuthorModel(StreamingSequence):
    def __init__(self):
        super().__init__([])
        self.results = {}

    async def stream_response(self, *args, **kwargs):
        for item in args[1]:
            if item.get("type") == "function_call_output":
                self.results[item["call_id"]] = json.loads(item["output"])
        step = len(self.inputs)
        if step == 0:
            patch = "*** Begin Patch\n" + "".join(
                f"*** Add File: authored/{path}\n" + "".join("+" + line + "\n" for line in body.splitlines())
                for path, body in FILES.items()) + "*** End Patch\n"
            output = patch_response(patch).output
        elif step == 1:
            output = [tool_item("prepare_workspace_publication", {"files": SELECTION}, "prepare")]
        elif step == 2:
            output = [tool_item("publish_workspace_files", self.results["prepare"]["publish_arguments"], "publish")]
        elif step == 3:
            assert self.results["publish"]["installed_as_skill"] is False
            output = [tool_item("validate_workspace_skill", {"root_path": "draft/outline-review"}, "validate")]
        elif step == 4:
            assert self.results["validate"]["status"] == "valid"
            output = [tool_item("install_workspace_skill", {
                "root_path": "draft/outline-review", "snapshot_hash": self.results["validate"]["snapshot_hash"],
                "scope": "project", "installation_id": "", "expected_active_version_id": ""}, "install")]
        elif step == 5:
            assert self.results["install"]["installed_as_skill"] and self.results["install"]["ready_in_current_turn"]
            output = [tool_item("load_skill_instructions", {"capability_id": "outline_review"}, "load")]
        elif step == 6:
            assert self.results["load"]["version"] == "1.0.0"
            output = [tool_item("read_skill_resource", {
                "capability_id": "outline_review", "path": "references/checklist.md", "offset": 0, "limit": 16000}, "resource")]
        else:
            assert step == 7 and self.results["resource"]["data"]["content"] == FILES["references/checklist.md"]
            output = [final_item({"title": "Created and invoked outline-review", "evidence": self.results["resource"]["data"]["content"]})]
        self.responses = [output]
        async for event in super().stream_response(*args, **kwargs):
            yield event


@pytest.mark.parametrize("mode", ["main", "background", "stateful"])
def test_sdk_authors_publishes_installs_and_loads_multifile_skill_across_approvals(tmp_path, monkeypatch, mode):
    async def scenario():
        disk, backend, context, config = publication_setup(tmp_path, monkeypatch, mode)
        model = AuthorModel()
        for tool in TOOLS[2:]:
            descriptor = deepcopy(backend.catalog["tools"][0])
            install = tool.name == "install_workspace_skill"
            descriptor.update(id="runtime:" + tool.name, name=tool.name,
                              access="write" if install else "read", approval="always" if install else "never")
            backend.catalog["tools"].append(descriptor)
        published_bytes, installations, resources = {}, [], []
        publish = disk.publish_files

        async def publish_exact(call, sdk, arguments):
            receipt = await publish(call, sdk, arguments)
            with tarfile.open(fileobj=io.BytesIO(await disk.read_snapshot(disk.lease.session_id,
                    SnapshotReference.model_validate(arguments["snapshot"]))), mode="r:") as archive:
                for item in SELECTION:
                    published_bytes[item["path"]] = archive.extractfile(item["source_path"]).read()
            return receipt

        disk.publish_files = publish_exact
        digest = hashlib.sha256(json.dumps(FILES, sort_keys=True).encode()).hexdigest()
        detail = {"status": "available", "capability_id": "outline_review", "version": "1.0.0", "execution_mode": "inline",
                  "skill": {"instructions": FILES["SKILL.md"], "content_hash": "sha256:" + digest,
                            "scope": "project", "scope_ref": "project", "manifest": {"dependencies": [], "scripts": []}}}

        async def preview(project, root):
            # Registry validation is a backend fixture, not a claim that Go ran.
            assert (project, root) == ("project", "draft/outline-review")
            assert published_bytes == {"draft/outline-review/" + path: body.encode() for path, body in FILES.items()}
            return {"project_id": project, "root_path": root, "status": "valid", "snapshot_hash": digest,
                    "files": [{"path": path, "version": 1} for path in published_bytes]}

        async def install(call, sdk, arguments):
            assert not installations and backend.calls[sdk]["status"] == "running"
            assert arguments == {"root_path": "draft/outline-review", "snapshot_hash": digest,
                                 "scope": "project", "installation_id": "", "expected_active_version_id": ""}
            await preview("project", arguments["root_path"])
            installations.append((call, sdk, deepcopy(arguments)))
            return {"receipt": {"receipt_id": "installed", "project_id": "project", "skill_installation_id": "installation", "skill_version_id": "version"},
                    "installation": {"skill_installation_id": "installation", "capability_id": "outline_review", "skill_name": "outline-review",
                        "scope": "project", "scope_ref": "project", "enabled": True, "registry_status": "available", "active_version_id": "version",
                        "versions": [{"skill_version_id": "version", "version": "1.0.0", "content_hash": "sha256:" + digest,
                                      "execution_mode": "inline", "manifest": {"dependencies": [], "scripts": []}}]}}

        async def capability(key, project, version):
            assert installations and (key, project, version) == ("outline_review", "project", "1.0.0")
            return {"data": deepcopy(detail)}

        async def resource(project, key, version, *, path, offset, limit):
            assert installations and (project, key, version) == ("project", "outline_review", "1.0.0")
            assert path == "references/checklist.md" and offset == 0
            resources.append((key, version, path))
            return {"data": {"path": path, "content": published_bytes["draft/outline-review/" + path].decode()}}

        backend.preview_workspace_skill = preview
        backend.install_workspace_skill = install
        backend.get_capability = capability
        backend.get_skill_resources = resource
        saved, approved = None, []
        for stage in range(4):
            await bind_managed_instructions(context)
            prepared = await AgentToolProvider(backend, TOOLS, guardrail_policy=SDKGuardrailPolicy()).prepare(context, [], set())
            prepared.capabilities = {SKILL.capability_id: {"capability_id": SKILL.capability_id,
                "version": SKILL.version, "execution_mode": "inline", "status": "available", "skill": {"content_hash": SKILL.content_hash}}}
            prepared.selected_ids = {SKILL.capability_id}
            async with prepared:
                agent = await bind_native_agent(context, config, Agent(name="Skill author", model=model, tools=prepared.tools))
                runner_input = "Original"
                if saved is not None:
                    runner_input = await RunState.from_json(agent, saved, context_override=context)
                    pending = runner_input.get_interruptions()
                    assert len(pending) == 1
                    sdk = ("patch-call", "publish", "install")[stage - 1]
                    runner_input.approve(pending[0])
                    backend.calls[sdk]["status"] = "approved"
                    approved.append(sdk)
                result, saved = await run_boundary(mode, agent, runner_input, context, config)
            if stage < 3:
                assert result is None or result.interruptions
                assert len(installations) == 0 and not disk.closes
                context = rebuilt_context(context, saved)
                disk.lease = None
            else:
                assert result.final_output and not result.interruptions
        assert approved == ["patch-call", "publish", "install"]
        assert len(installations) == len(backend.published) == 1
        assert resources == [("outline_review", "1.0.0", "references/checklist.md")]
        assert len(model.inputs) == 8 and not backend.failed
        assert disk.creates == disk.closes == 1 and not disk.restores and not disk.commands
        assert context.loaded_capabilities["outline_review"] == detail
        for path, body in FILES.items():
            assert (disk.root / "authored" / path).read_bytes() == body.encode()
            writes = [op for _, op, _ in disk.file_calls if op.operation == "write" and op.path == "authored/" + path]
            assert len(writes) == 1
        # Installation uses the registry-backed resource tool immediately; it
        # does not pretend the initial frozen sandbox manifest was rewritten.
        assert not (disk.root / ".skills" / "outline-review").exists()
    asyncio.run(scenario())
