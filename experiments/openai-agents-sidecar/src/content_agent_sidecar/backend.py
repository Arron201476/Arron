from __future__ import annotations

import asyncio
import base64
import hashlib
from http.client import HTTPException
from contextlib import contextmanager
from contextvars import ContextVar
import json
from pathlib import Path
import re
import subprocess
import tempfile
from typing import Any, Iterator
from uuid import uuid4
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener, urlopen


_MAX_VIDEO_ATTACHMENT_BYTES = 500 * 1024 * 1024
_activity_headers: ContextVar[dict[str, str] | None] = ContextVar("backend_activity_headers", default=None)


@contextmanager
def backend_activity(
    project_id: str, *, agent_turn_id: str = "", task_attempt_id: str = "", execution_attempt_id: str = "", attempt_token: str = "",
) -> Iterator[None]:
    if not project_id or sum(bool(value) for value in (agent_turn_id, task_attempt_id, execution_attempt_id)) != 1 or bool(task_attempt_id or execution_attempt_id) != bool(attempt_token):
        raise ValueError("backend activity requires one durable execution identity")
    headers = {"X-Agent-Project-ID": project_id}
    if agent_turn_id:
        headers["X-Agent-Turn-ID"] = agent_turn_id
    elif execution_attempt_id:
        headers.update({"X-Agent-Execution-Attempt-ID": execution_attempt_id, "X-Agent-Attempt-Token": attempt_token})
    else:
        headers.update({"X-Agent-Task-Attempt-ID": task_attempt_id, "X-Agent-Attempt-Token": attempt_token})
    token = _activity_headers.set(headers)
    try:
        yield
    finally:
        _activity_headers.reset(token)


@contextmanager
def backend_memory_activity(project_id: str, generation_id: str, attempt: int, attempt_token: str) -> Iterator[None]:
    if _activity_headers.get() is not None or type(attempt) is not int or not 1 <= attempt <= 2**31 - 1:
        raise ValueError("memory activity requires an independent worker attempt")
    if any(not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,256}", value)
           for value in (project_id, generation_id)) or not isinstance(attempt_token, str) or not re.fullmatch(r"[A-Za-z0-9._~-]{1,512}", attempt_token):
        raise ValueError("memory activity identity is invalid")
    token = _activity_headers.set({"X-Agent-Project-ID": project_id, "X-Agent-Memory-Generation-ID": generation_id,
                                   "X-Agent-Memory-Generation-Attempt": str(attempt), "X-Agent-Attempt-Token": attempt_token})
    try:
        yield
    finally:
        _activity_headers.reset(token)


class BackendError(RuntimeError):
    def __init__(self, message: str, *, code: str = "", status_code: int | None = None):
        super().__init__(message)
        self.code = code
        self.status_code = status_code

    @classmethod
    def from_http_response(cls, status_code: int, detail: str) -> BackendError:
        code = ""
        try:
            payload = json.loads(detail)
            error = payload.get("error") if isinstance(payload, dict) else None
            candidate = error.get("code") if isinstance(error, dict) else None
            if isinstance(candidate, str) and re.fullmatch(r"[A-Z][A-Z0-9_]{0,127}", candidate):
                code = candidate
        except ValueError:
            pass
        return cls(f"backend returned HTTP {status_code}: {detail}", code=code, status_code=status_code)


class _NoWorkspaceRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _native_command_path(method: str, path: str) -> bool:
    return method == "POST" and re.fullmatch(
        r"/internal/v1/native-workspaces/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}/commands", path
    ) is not None


def _native_pty_path(method: str, path: str, suffix: str = "") -> bool:
    return method == "POST" and re.fullmatch(
        r"/internal/v1/native-workspaces/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}/pty" + suffix, path
    ) is not None


def _native_publication_path(method: str, path: str) -> bool:
    return method == "POST" and re.fullmatch(
        r"/internal/v1/native-workspaces/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}/(?:publications|memory-publications)", path
    ) is not None


def _native_file_path(method: str, path: str) -> bool:
    return method == "POST" and re.fullmatch(
        r"/internal/v1/native-workspaces/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}/files", path
    ) is not None


def _native_manifest_path(method: str, path: str, suffix: str = "") -> bool:
    return method == "POST" and re.fullmatch(
        r"/internal/v1/native-workspaces/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}/manifest" + suffix, path
    ) is not None


class BackendClient:
    def __init__(
        self,
        base_url: str,
        timeout_seconds: float = 15.0,
        internal_token: str = "",
        ffmpeg_command: str = "ffmpeg",
        temp_dir: str = "",
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._timeout_seconds = timeout_seconds
        self._internal_token = internal_token.strip()
        self._ffmpeg_command = ffmpeg_command.strip() or "ffmpeg"
        self._temp_dir = Path(temp_dir).expanduser().resolve() if temp_dir.strip() else None

    async def get_capabilities(self, project_id: str = "") -> dict[str, Any]:
        if project_id:
            project = quote(project_id, safe="")
            return await self._get_json(f"/api/v1/projects/{project}/capabilities")
        return await self._get_json("/api/v1/capabilities")

    async def get_capability(
        self, capability_id: str, project_id: str = "", version: str = ""
    ) -> dict[str, Any]:
        capability = quote(capability_id, safe="")
        query = f"?{urlencode({'version': version})}" if version else ""
        if project_id:
            project = quote(project_id, safe="")
            return await self._get_json(
                f"/api/v1/projects/{project}/capabilities/{capability}{query}"
            )
        return await self._get_json(f"/api/v1/capabilities/{capability}{query}")

    async def get_skill_resources(
        self, project_id: str, capability_id: str, version: str,
        *, path: str = "", offset: int = 0, limit: int = 16000,
    ) -> dict[str, Any]:
        project = quote(project_id, safe="")
        capability = quote(capability_id, safe="")
        query = urlencode({"version": version, "path": path, "offset": offset, "limit": limit})
        return await self._get_json(
            f"/api/v1/projects/{project}/capabilities/{capability}/skill-resources?{query}"
        )

    async def get_agent_tool_catalog(self) -> dict[str, Any]:
        if (_activity_headers.get() or {}).get("X-Agent-Memory-Generation-ID"):
            self.native_workspace_activity()
            base = urlsplit(self._base_url)
            if base.scheme not in {"http", "https"} or not base.hostname or base.username or base.password or base.query or base.fragment:
                raise BackendError("Memory tool catalog requires a trusted backend origin")
            headers = {**self._internal_headers(), "Accept": "application/json", "Accept-Encoding": "identity"}
            result = await asyncio.to_thread(self._memory_rollout_request_sync, "/internal/v1/agent-tools/catalog",
                                            None, headers, 256 << 10, method="GET")
            allowed = {"runtime:exec_command": "always", "runtime:apply_patch": "always", "runtime:write_stdin": "always",
                       "runtime:prepare_agent_memory_publication": "never", "runtime:publish_agent_memory": "always"}
            values = result.get("tools")
            if (set(result) - {"schema_version", "tools", "mcp_servers", "hosted_tools"}
                    or result.get("mcp_servers") not in (None, []) or result.get("hosted_tools") not in (None, [])
                    or not isinstance(values, list) or len(values) > len(allowed)):
                raise BackendError("Memory tool catalog contains non-private services")
            seen = set()
            for tool in values:
                if (not isinstance(tool, dict) or not isinstance(tool.get("id"), str) or tool["id"] not in allowed
                        or tool["id"] in seen or tool.get("kind") != "runtime_function" or tool.get("approval") != allowed[tool["id"]]
                        or tool.get("name") != tool["id"].removeprefix("runtime:") or tool.get("server_id")):
                    raise BackendError("Memory tool catalog contains an unadmitted tool")
                if tool["id"] in {"runtime:prepare_agent_memory_publication", "runtime:publish_agent_memory"}:
                    expected_access = "read" if tool["id"] == "runtime:prepare_agent_memory_publication" else "write"
                    if tool.get("access") != expected_access:
                        raise BackendError("Memory publication catalog has an invalid access policy")
                seen.add(tool["id"])
            return result
        result = await self._get_json_with_headers(
            "/internal/v1/agent-tools/catalog", self._internal_headers()
        )
        return self._required_data_object(result, "Agent tool catalog")

    async def resolve_mcp_credentials(self, server_id: str, credential_binding: str) -> dict[str, Any]:
        result = await self._post_json(
            "/internal/v1/mcp-connections/resolve",
            {"server_id": server_id, "credential_binding": credential_binding},
            headers=self._internal_headers(),
        )
        return self._required_data_object(result, "MCP connection credentials")

    async def resolve_agent_instruction_snapshot(self, project_id: str, activity_key: str) -> dict[str, Any]:
        result = await self._post_json(
            "/internal/v1/agent-instructions/snapshot",
            {"project_id": project_id, "activity_key": activity_key}, headers=self._internal_headers(),
        )
        return self._required_data_object(result, "Agent instruction snapshot")

    async def resolve_agent_memory_snapshot(self, project_id: str, activity_key: str) -> dict[str, Any]:
        result = await self._post_json(
            "/internal/v1/agent-memory/snapshot",
            {"project_id": project_id, "activity_key": activity_key}, headers=self._internal_headers(),
        )
        if not isinstance(result, dict) or not isinstance(result.get("data"), dict) or not result["data"]:
            raise BackendError("Agent memory snapshot response is missing its data object")
        return self._required_data_object(result, "Agent memory snapshot")

    async def memory_rollout_request(self, operation: str, payload: dict[str, Any]) -> dict[str, Any]:
        self.native_workspace_activity()
        if operation not in {"save", "read", "archive-policy", "archive", "archive-receipt"}:
            raise BackendError("Invalid memory source operation")
        base = urlsplit(self._base_url)
        if base.scheme not in {"http", "https"} or not base.hostname or base.username or base.password or base.query or base.fragment:
            raise BackendError("Memory source requires a trusted backend origin")
        body = json.dumps(payload, allow_nan=False).encode("utf-8")
        limit = (8 << 20) + 4096 if operation in {"save", "archive"} else 4096
        if len(body) > limit:
            raise BackendError("Memory source request exceeds its bound")
        path = "/internal/v1/agent-memory/rollouts" + ("/read" if operation == "read" else "")
        if operation in {"archive-policy", "archive", "archive-receipt"}:
            path = "/internal/v1/agent-memory/" + operation
        headers = {**self._internal_headers(), "Content-Type": "application/json", "Accept": "application/json", "Accept-Encoding": "identity"}
        return await asyncio.to_thread(self._memory_rollout_request_sync, path, body, headers,
                                       (8 << 20) + 4096 if operation == "read" else 4096)

    async def recover_memory_archive(self, payload: dict[str, Any]) -> dict[str, Any]:
        if _activity_headers.get():
            raise BackendError("Memory archive recovery requires an independent worker context")
        base = urlsplit(self._base_url)
        if base.scheme not in {"http", "https"} or not base.hostname or base.username or base.password or base.query or base.fragment:
            raise BackendError("Memory archive recovery requires a trusted backend origin")
        body = json.dumps(payload, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode("utf-8")
        if len(body) > (8 << 20) + 4096:
            raise BackendError("Memory archive recovery request exceeds its bound")
        headers = {**self._internal_headers(), "Content-Type": "application/json", "Accept": "application/json", "Accept-Encoding": "identity"}
        return await asyncio.to_thread(self._memory_rollout_request_sync,
                                       "/internal/v1/agent-memory/archive-recover", body, headers, 4096)

    async def memory_generation_request(self, operation: str, payload: dict[str, Any]) -> dict[str, Any]:
        if _activity_headers.get():
            raise BackendError("Memory generation requires an independent worker context")
        if operation not in {"claim", "start", "renew", "checkpoint", "pause", "extraction", "inputs", "complete"}:
            raise BackendError("Invalid memory generation operation")
        base = urlsplit(self._base_url)
        if base.scheme not in {"http", "https"} or not base.hostname or base.username or base.password or base.query or base.fragment:
            raise BackendError("Memory generation requires a trusted backend origin")
        body = json.dumps(payload, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode("utf-8")
        limit = (4 << 20) + 4096 if operation in {"checkpoint", "pause", "extraction"} else 4096
        if operation == "inputs":
            limit += 16 << 20
        if len(body) > limit:
            raise BackendError("Memory generation request exceeds its bound")
        headers = {**self._internal_headers(), "Content-Type": "application/json", "Accept": "application/json", "Accept-Encoding": "identity"}
        result = await asyncio.to_thread(self._memory_rollout_request_sync,
                                        "/internal/v1/agent-memory/generations/" + operation,
                                        body, headers, (16 << 20) + 16384 if operation == "claim" else 8192)
        key = "claim" if operation == "claim" else "receipt" if operation in {"extraction", "inputs"} else "job"
        if set(result) != {key} or not (isinstance(result[key], dict) and result[key] or operation == "claim" and result[key] is None):
            raise BackendError("Memory generation response did not confirm its operation")
        return result

    def _memory_rollout_request_sync(self, path, body, headers, limit, *, method="POST"):
        opener = build_opener(ProxyHandler({}), _NoWorkspaceRedirect())
        request = Request(self._base_url + path, data=body, method=method, headers=headers)
        try:
            with opener.open(request, timeout=self._timeout_seconds) as response:
                content = response.read(limit + 1)
                metadata = response.headers
                if any(len(metadata.get_all(key, [])) > 1 for key in ("Content-Type", "Content-Length", "Content-Encoding")):
                    raise ValueError("duplicate response headers")
                length = metadata.get("Content-Length")
                if (response.status != 200 or response.geturl() != request.full_url or len(content) > limit
                        or metadata.get_content_type() != "application/json" or metadata.get("Content-Encoding")
                        or length is not None and (not length.isdecimal() or int(length) != len(content))):
                    raise ValueError("response envelope")
                result = json.loads(content)
                if not isinstance(result, dict) or set(result) != {"data"} or not isinstance(result["data"], dict) or not result["data"]:
                    raise ValueError("response data")
                return result["data"]
        except HTTPError as exc:
            status = exc.code
            code = "MEMORY_SOURCE_TRANSPORT_UNCONFIRMED"
            try:
                error = json.loads(exc.read(4096)).get("error", {})
                value = error.get("code") if isinstance(error, dict) else None
                if isinstance(value, str) and re.fullmatch(r"[A-Z][A-Z0-9_]{0,127}", value):
                    code = value
            except (ValueError, AttributeError, OSError):
                pass
            finally:
                exc.close()
            raise BackendError("Private memory source request was not confirmed", code=code, status_code=status) from None
        except (ValueError, OSError, URLError, HTTPException):
            raise BackendError("Private memory source response was not confirmed", code="MEMORY_SOURCE_TRANSPORT_UNCONFIRMED") from None

    async def get_saved_instructions(self, project_id: str) -> dict[str, Any]:
        result = await self._get_json(f"/api/v1/agent-instructions?{urlencode({'project_id': project_id})}")
        return self._required_data_object(result, "Saved Agent instructions")

    async def update_saved_instructions(self, call_id: str, sdk_id: str, arguments: dict[str, Any]) -> dict[str, Any]:
        result = await self._post_json(f"/internal/v1/agent-tool-calls/{quote(call_id, safe='')}/instructions",
            {"sdk_tool_call_id": sdk_id, "arguments": arguments}, headers=self._internal_headers())
        return self._required_data_object(result, "Saved Agent instruction receipt")

    async def get_artifact_delivery(self, artifact_version_id: str) -> dict[str, Any]:
        result = await self._get_json(f"/api/v1/artifact-versions/{quote(artifact_version_id, safe='')}/delivery")
        return self._required_data_object(result, "Artifact downloads")

    async def preview_workspace_skill(self, project_id: str, root_path: str) -> dict[str, Any]:
        result = await self._get_json(f"/api/v1/projects/{quote(project_id, safe='')}/skill-drafts/preview?{urlencode({'root_path': root_path})}")
        return self._required_data_object(result, "Skill draft preview")

    async def list_execution_targets(self, project_id: str, target_type: str, after_id: str = "") -> dict[str, Any]:
        query = urlencode({"target_type": target_type, "after_id": after_id})
        return self._data(await self._get_json(f"/api/v1/projects/{quote(project_id, safe='')}/execution-targets?{query}"))

    async def inspect_execution_controls(self, project_id: str, target_type: str, target_id: str) -> dict[str, Any]:
        query = urlencode({"target_type": target_type, "target_id": target_id})
        return self._data(await self._get_json(f"/api/v1/projects/{quote(project_id, safe='')}/execution-controls?{query}"))

    async def control_execution(self, call_id: str, sdk_call_id: str, arguments: dict[str, Any]) -> dict[str, Any]:
        return self._data(await self._post_json(f"/internal/v1/agent-tool-calls/{quote(call_id, safe='')}/execution-control",
            {"sdk_tool_call_id": sdk_call_id, "arguments": arguments}))

    async def install_workspace_skill(self, call_id: str, sdk_call_id: str, arguments: dict[str, Any]) -> dict[str, Any]:
        result = await self._post_json(
            f"/internal/v1/agent-tool-calls/{quote(call_id, safe='')}/skill-installations",
            {"sdk_tool_call_id": sdk_call_id, "arguments": arguments}, headers=self._internal_headers(),
        )
        return self._required_data_object(result, "Installed Skill draft")

    async def list_workspace_files(self, project_id: str) -> list[dict[str, Any]]:
        result = await self._get_json(f"/api/v1/projects/{quote(project_id, safe='')}/files")
        files = result.get("data")
        if not isinstance(files, list) or any(not isinstance(file, dict) for file in files):
            raise BackendError("Workspace file catalog is invalid")
        return files

    async def read_workspace_file(self, project_id: str, path: str, version: int, offset: int, limit: int) -> dict[str, Any]:
        query = urlencode({"path": path, "version": version, "offset": offset, "limit": limit})
        result = await self._get_json(f"/api/v1/projects/{quote(project_id, safe='')}/files/read?{query}")
        return self._required_data_object(result, "Workspace file")

    async def apply_workspace_patch(self, call_id: str, sdk_call_id: str, patch: dict[str, Any], content: str) -> dict[str, Any]:
        result = await self._post_json(
            f"/internal/v1/agent-tool-calls/{quote(call_id, safe='')}/project-file-patches",
            {"sdk_tool_call_id": sdk_call_id, "patch": patch, "content": content},
            headers=self._internal_headers(),
        )
        return self._required_data_object(result, "Saved workspace file")

    async def begin_agent_tool_call(
        self,
        *,
        project_id: str,
        conversation_id: str,
        agent_turn_id: str,
        sdk_tool_call_id: str,
        tool_id: str,
        arguments: dict[str, Any],
        skill_invocation_id: str = "",
        agent_task_attempt_id: str = "",
        execution_attempt_id: str = "",
        memory_generation_id: str = "",
        memory_generation_attempt: int = 0,
        attempt_token: str = "",
        skill_snapshot: dict[str, str] | None = None,
        configuration_hash: str = "",
        program_call_id: str = "",
    ) -> dict[str, Any]:
        result = await self._post_json(
            "/internal/v1/agent-tool-calls",
            {
                "project_id": project_id,
                "conversation_id": conversation_id,
                "skill_invocation_id": skill_invocation_id,
                "agent_turn_id": agent_turn_id,
                "sdk_tool_call_id": sdk_tool_call_id,
                "tool_id": tool_id,
                "arguments": arguments,
                "agent_task_attempt_id": agent_task_attempt_id,
                **({"execution_attempt_id": execution_attempt_id} if execution_attempt_id else {}),
                **({"memory_generation_id": memory_generation_id, "memory_generation_attempt": memory_generation_attempt}
                   if memory_generation_id or memory_generation_attempt else {}),
                "attempt_token": attempt_token,
                **({"skill_snapshot": skill_snapshot} if skill_snapshot is not None else {}),
                **({"configuration_hash": configuration_hash} if configuration_hash else {}),
                **({"program_call_id": program_call_id} if program_call_id else {}),
            },
            headers=self._internal_headers(),
        )
        return self._required_data_object(result, "Agent tool call")

    async def start_agent_tool_call(
        self, agent_tool_call_id: str, sdk_tool_call_id: str, *, configuration_hash: str = ""
    ) -> dict[str, Any]:
        call_id = quote(agent_tool_call_id, safe="")
        result = await self._post_json(
            f"/internal/v1/agent-tool-calls/{call_id}/start",
            {"expected_sdk_tool_call_id": sdk_tool_call_id,
             **({"configuration_hash": configuration_hash} if configuration_hash else {})},
            headers=self._internal_headers(),
        )
        return self._required_data_object(result, "started Agent tool call")

    async def execute_skill_script(
        self,
        agent_tool_call_id: str,
        sdk_tool_call_id: str,
        arguments: dict[str, Any],
    ) -> dict[str, Any]:
        call_id = quote(agent_tool_call_id, safe="")
        result = await self._post_json(
            f"/internal/v1/agent-tool-calls/{call_id}/skill-script-executions",
            {
                "expected_sdk_tool_call_id": sdk_tool_call_id,
                "arguments": arguments,
            },
            headers=self._internal_headers(),
        )
        return self._required_data_object(result, "Skill script execution")

    async def complete_agent_tool_call(
        self,
        agent_tool_call_id: str,
        *,
        result: Any,
        result_size_bytes: int,
        trace_ref: str = "",
    ) -> dict[str, Any]:
        call_id = quote(agent_tool_call_id, safe="")
        payload = await self._post_json(
            f"/internal/v1/agent-tool-calls/{call_id}/complete",
            {
                "result": result,
                "result_size_bytes": result_size_bytes,
                "trace_ref": trace_ref,
            },
            headers=self._internal_headers(),
        )
        return self._required_data_object(payload, "completed Agent tool call")

    async def store_agent_tool_output(self, call_id: str, sdk_call_id: str, filename: str, content: bytes) -> dict[str, Any]:
        query = urlencode({"sdk_tool_call_id": sdk_call_id, "filename": filename})
        path = f"/internal/v1/agent-tool-calls/{quote(call_id, safe='')}/outputs?{query}"

        def upload() -> dict[str, Any]:
            request = Request(self._base_url + path, data=content, method="POST", headers={**self._internal_headers(), "Content-Type": "application/octet-stream"})
            try:
                with urlopen(request, timeout=self._timeout_seconds) as response:
                    result = json.load(response)
            except (HTTPError, URLError) as exc:
                raise BackendError("Agent tool output could not be persisted") from exc
            return self._required_data_object(result, "Agent tool output")

        return await asyncio.to_thread(upload)

    async def fail_agent_tool_call(
        self,
        agent_tool_call_id: str,
        *,
        error_code: str,
        error_message: str,
        trace_ref: str = "",
    ) -> dict[str, Any]:
        call_id = quote(agent_tool_call_id, safe="")
        payload = await self._post_json(
            f"/internal/v1/agent-tool-calls/{call_id}/failures",
            {
                "error_code": error_code,
                "error_message": error_message,
                "trace_ref": trace_ref,
            },
            headers=self._internal_headers(),
        )
        return self._required_data_object(payload, "failed Agent tool call")

    async def cancel_agent_tool_call(
        self, agent_tool_call_id: str, reason: str
    ) -> dict[str, Any]:
        call_id = quote(agent_tool_call_id, safe="")
        payload = await self._post_json(
            f"/internal/v1/agent-tool-calls/{call_id}/cancel",
            {"reason": reason},
            headers=self._internal_headers(),
        )
        return self._required_data_object(payload, "cancelled Agent tool call")

    async def get_project_snapshot(self, project_id: str) -> dict[str, Any]:
        project = quote(project_id, safe="")
        return await self._get_json(f"/api/v1/projects/{project}/snapshot")

    async def get_project(self, project_id: str) -> dict[str, Any]:
        project = quote(project_id, safe="")
        return await self._get_json(f"/api/v1/projects/{project}")

    async def get_project_assets(self, project_id: str) -> dict[str, Any]:
        project = quote(project_id, safe="")
        return await self._get_json(f"/api/v1/projects/{project}/assets")

    async def get_asset(self, asset_id: str) -> dict[str, Any]:
        asset = quote(asset_id, safe="")
        return await self._get_json(f"/api/v1/assets/{asset}")

    async def get_parsed_asset_text(
        self, asset_id: str, asset_snapshot_id: str
    ) -> dict[str, Any]:
        asset = quote(asset_id, safe="")
        query = urlencode({"asset_snapshot_id": asset_snapshot_id})
        return await self._get_json(
            f"/api/v1/assets/{asset}/parsed-text?{query}"
        )

    async def get_image_data_url(
        self, asset_id: str, asset_snapshot_id: str
    ) -> str | None:
        asset = quote(asset_id, safe="")
        result = await self._get_json(f"/api/v1/assets/{asset}")
        metadata = self._data(result)
        if not isinstance(metadata, dict):
            raise BackendError("backend asset response is invalid")
        if metadata.get("kind") != "image":
            return None
        content, content_type = await self._read_snapshot_content(
            metadata, asset_id, asset_snapshot_id, max_bytes=10 * 1024 * 1024,
            accept="image/*", label="image attachment",
        )
        if not content_type.startswith("image/"):
            raise BackendError("asset content is not an image")
        encoded = base64.b64encode(content).decode("ascii")
        return f"data:{content_type};base64,{encoded}"

    async def get_hosted_input_content(
        self, project_id: str, asset_id: str, asset_snapshot_id: str, *, max_bytes: int,
    ) -> tuple[dict[str, Any], bytes]:
        metadata = self._data(await self.get_asset(asset_id))
        if not isinstance(metadata, dict) or metadata.get("asset_id") != asset_id or metadata.get("project_id") != project_id:
            raise BackendError("Hosted input does not belong to the current project")
        content, _ = await self._read_snapshot_content(
            metadata, asset_id, asset_snapshot_id, max_bytes=max_bytes,
            accept="application/octet-stream", label="Hosted input",
        )
        return metadata, content

    async def _read_snapshot_content(
        self, metadata: dict[str, Any], asset_id: str, asset_snapshot_id: str, *,
        max_bytes: int, accept: str, label: str,
    ) -> tuple[bytes, str]:
        if metadata.get("asset_id") != asset_id:
            raise BackendError(f"{label} asset identity changed")
        if not asset_snapshot_id or metadata.get("current_snapshot_id") != asset_snapshot_id:
            raise BackendError(f"{label} snapshot is stale")
        if metadata.get("status") != "available" or metadata.get("deleted_at"):
            raise BackendError(f"{label} is not available")
        size = metadata.get("size_bytes")
        if not isinstance(size, int) or isinstance(size, bool) or not 0 < size <= max_bytes:
            raise BackendError(f"{label} is empty or exceeds the input limit")
        checksum = metadata.get("checksum")
        if metadata.get("checksum_algorithm") != "sha256" or not isinstance(checksum, str) or not re.fullmatch(r"[a-f0-9]{64}", checksum):
            raise BackendError(f"{label} has no valid content checksum")
        path = f"/api/v1/assets/{quote(asset_id, safe='')}/content?{urlencode({'asset_snapshot_id': asset_snapshot_id})}"
        content, content_type = await asyncio.to_thread(self._get_bytes_sync, path, accept, max_bytes, label)
        if len(content) != size or hashlib.sha256(content).hexdigest() != checksum:
            raise BackendError(f"{label} content does not match its snapshot")
        return content, content_type

    async def get_video_frame_data_urls(
        self, asset_id: str, asset_snapshot_id: str
    ) -> list[str]:
        asset = quote(asset_id, safe="")
        result = await self._get_json(f"/api/v1/assets/{asset}")
        metadata = self._data(result)
        if not isinstance(metadata, dict):
            raise BackendError("backend asset response is invalid")
        if metadata.get("kind") != "video":
            return []
        content, content_type = await self._read_snapshot_content(
            metadata, asset_id, asset_snapshot_id, max_bytes=_MAX_VIDEO_ATTACHMENT_BYTES,
            accept="video/*", label="video attachment",
        )
        if not content_type.startswith("video/"):
            raise BackendError("asset content is not a video")
        return await asyncio.to_thread(self._extract_video_frames_sync, content)

    async def get_video_content(
        self, asset_id: str, asset_snapshot_id: str
    ) -> tuple[bytes, str]:
        asset = quote(asset_id, safe="")
        result = await self._get_json(f"/api/v1/assets/{asset}")
        metadata = self._data(result)
        if not isinstance(metadata, dict) or metadata.get("kind") != "video":
            raise BackendError("asset is not a video")
        content, content_type = await self._read_snapshot_content(
            metadata, asset_id, asset_snapshot_id, max_bytes=_MAX_VIDEO_ATTACHMENT_BYTES,
            accept="video/*", label="video attachment",
        )
        if not content_type.startswith("video/"):
            raise BackendError("asset content is not a video")
        return content, content_type

    def _extract_video_frames_sync(self, content: bytes) -> list[str]:
        ffmpeg = Path(self._ffmpeg_command)
        ffprobe = ffmpeg.with_name("ffprobe.exe" if ffmpeg.suffix.lower() == ".exe" else "ffprobe")
        ffprobe_command = str(ffprobe) if ffprobe.exists() else "ffprobe"
        if self._temp_dir is not None:
            self._temp_dir.mkdir(parents=True, exist_ok=True)
            prefix = f"content-agent-video-{uuid4().hex}"
            source = self._temp_dir / f"{prefix}-input.mp4"
            frame_pattern = self._temp_dir / f"{prefix}-frame-%02d.jpg"
            frame_glob = f"{prefix}-frame-*.jpg"
            cleanup_root: Path | None = self._temp_dir
            temporary_directory = None
        else:
            temporary_directory = tempfile.TemporaryDirectory(
                prefix="content-agent-video-"
            )
            root = Path(temporary_directory.name)
            source = root / "input.mp4"
            frame_pattern = root / "frame-%02d.jpg"
            frame_glob = "frame-*.jpg"
            cleanup_root = None
        try:
            source.write_bytes(content)
            try:
                probe = subprocess.run(
                    [
                        ffprobe_command,
                        "-v",
                        "error",
                        "-show_entries",
                        "format=duration",
                        "-of",
                        "default=noprint_wrappers=1:nokey=1",
                        str(source),
                    ],
                    capture_output=True,
                    check=True,
                    text=True,
                    timeout=15,
                )
                duration = max(float(probe.stdout.strip()), 0.1)
                interval = max(duration / 6.0, 0.5)
                subprocess.run(
                    [
                        self._ffmpeg_command,
                        "-hide_banner",
                        "-loglevel",
                        "error",
                        "-i",
                        str(source),
                        "-vf",
                        f"fps=1/{interval:.6f},scale=480:-2",
                        "-frames:v",
                        "6",
                        str(frame_pattern),
                    ],
                    capture_output=True,
                    check=True,
                    timeout=30,
                )
            except (OSError, subprocess.SubprocessError, ValueError) as exc:
                raise BackendError(f"video frame extraction failed: {exc}") from exc
            frames = sorted(frame_pattern.parent.glob(frame_glob))
            if not frames:
                raise BackendError("video frame extraction produced no frames")
            return [
                "data:image/jpeg;base64,"
                + base64.b64encode(frame.read_bytes()).decode("ascii")
                for frame in frames
            ]
        finally:
            if cleanup_root is not None:
                source.unlink(missing_ok=True)
                for frame in cleanup_root.glob(frame_glob):
                    frame.unlink(missing_ok=True)
            if temporary_directory is not None:
                temporary_directory.cleanup()

    async def get_project_goal(self, project_id: str) -> dict[str, Any]:
        project = quote(project_id, safe="")
        return await self._get_json(f"/api/v1/projects/{project}/goal")

    async def get_project_overview(self, project_id: str) -> dict[str, Any]:
        project = quote(project_id, safe="")
        project_result, artifacts_result, approvals_result, revisions_result = (
            await asyncio.gather(
                self._get_json(f"/api/v1/projects/{project}"),
                self._get_json(f"/api/v1/projects/{project}/artifacts"),
                self._get_json(f"/api/v1/projects/{project}/approvals"),
                self._get_json(f"/api/v1/projects/{project}/revision-requests"),
            )
        )
        artifacts = self._items(artifacts_result)
        return {
            "project": self._data(project_result),
            "artifact_count": len(artifacts),
            "artifact_types": sorted(
                {
                    str(item.get("artifact_type", ""))
                    for item in artifacts
                    if isinstance(item, dict) and item.get("artifact_type")
                }
            ),
            "pending_approvals": self._items(approvals_result),
            "revision_requests": self._items(revisions_result),
        }

    async def get_pending_proposed_actions(self, project_id: str) -> list[dict[str, Any]]:
        project = quote(project_id, safe="")
        result = await self._get_json(f"/api/v1/projects/{project}/proposed-actions")
        return [item for item in self._items(result) if isinstance(item, dict)]

    async def search_artifacts(
        self,
        project_id: str,
        query: str = "",
        artifact_type: str = "",
        limit: int = 20,
    ) -> dict[str, Any]:
        project = quote(project_id, safe="")
        result = await self._get_json(f"/api/v1/projects/{project}/artifacts")
        normalized_query = query.strip().casefold()
        normalized_type = artifact_type.strip().casefold()
        matches: list[dict[str, Any]] = []
        for item in self._items(result):
            if not isinstance(item, dict):
                continue
            if normalized_type and str(item.get("artifact_type", "")).casefold() != normalized_type:
                continue
            haystack = " ".join(
                str(item.get(key, ""))
                for key in ("title", "artifact_type", "scope_key", "capability_id")
            ).casefold()
            if normalized_query and normalized_query not in haystack:
                continue
            matches.append(
                {
                    key: item.get(key)
                    for key in (
                        "artifact_id",
                        "current_version_id",
                        "title",
                        "artifact_type",
                        "scope_key",
                        "capability_id",
                        "run_id",
                        "status",
                        "updated_at",
                    )
                }
            )
            if len(matches) >= max(1, min(limit, 50)):
                break
        return {"items": matches, "count": len(matches)}

    async def get_artifact(self, artifact_id: str) -> dict[str, Any]:
        artifact = quote(artifact_id, safe="")
        return await self._get_json(f"/api/v1/artifacts/{artifact}")

    async def get_current_artifact_version(self, artifact_id: str) -> dict[str, Any]:
        artifact_result = await self.get_artifact(artifact_id)
        artifact = self._data(artifact_result)
        if not isinstance(artifact, dict):
            raise BackendError("backend artifact response is invalid")
        version_id = str(artifact.get("current_version_id", "")).strip()
        if not version_id:
            raise BackendError("artifact has no current version")
        version_result = await self.get_artifact_version(version_id)
        return {"artifact": artifact, "version": self._data(version_result)}

    async def get_artifact_version(self, artifact_version_id: str) -> dict[str, Any]:
        version = quote(artifact_version_id, safe="")
        return await self._get_json(f"/api/v1/artifact-versions/{version}")

    async def get_run_snapshot(self, run_id: str) -> dict[str, Any]:
        run = quote(run_id, safe="")
        return await self._get_json(f"/api/v1/runs/{run}/snapshot")

    async def set_episode_execution_mode(
        self, run_id: str, mode: str
    ) -> dict[str, Any]:
        run = quote(run_id, safe="")
        return await asyncio.to_thread(
            self._write_json_sync,
            "PUT",
            f"/api/v1/runs/{run}/episode-execution-mode",
            {"mode": mode},
            {
                "Idempotency-Key": str(uuid4()),
                "X-Client-Instance-ID": "openai-agents-sidecar",
            },
        )

    async def get_conversation_messages(
        self, conversation_id: str, limit: int = 20
    ) -> dict[str, Any]:
        conversation = quote(conversation_id, safe="")
        path = f"/api/v1/conversations/{conversation}/messages"
        if limit > 0:
            path += f"?limit={min(limit, 100)}"
        result = await self._get_json(path)
        items = self._items(result)
        return {"items": items, "count": len(items)}

    async def search_conversation_messages(
        self,
        conversation_id: str,
        query: str,
        limit: int = 20,
    ) -> dict[str, Any]:
        conversation = quote(conversation_id, safe="")
        normalized_query = query.strip()
        if not normalized_query:
            return {"items": [], "count": 0}
        bounded_limit = max(1, min(limit, 100))
        encoded_query = quote(normalized_query, safe="")
        result = await self._get_json(
            f"/api/v1/conversations/{conversation}/messages"
            f"?q={encoded_query}&limit={bounded_limit}"
        )
        items = self._items(result)
        if not items:
            history = await self.get_conversation_messages(conversation_id, limit=0)
            items = _retrieve_conversation_messages(
                history.get("items", []), normalized_query, bounded_limit
            )
        return {"items": items, "count": len(items)}

    async def commit_agent_turn(
        self,
        project_id: str,
        conversation_id: str,
        request: dict[str, Any],
        decision: dict[str, Any],
        idempotency_key: str,
        agent_turn_id: str = "",
    ) -> dict[str, Any]:
        if not self._internal_token:
            raise BackendError("backend write tools are disabled")
        return await self._post_json(
            "/internal/v1/agent/turn-commits",
            {
                "project_id": project_id,
                "conversation_id": conversation_id,
                "request": request,
                "decision": decision,
                **({"agent_turn_id": agent_turn_id} if agent_turn_id else {}),
            },
            headers={
                "Authorization": "Bearer " + self._internal_token,
                "Idempotency-Key": idempotency_key,
            },
        )

    async def claim_execution_task(
        self,
        *,
        worker_id: str,
        executor_ids: list[str],
        provider_id: str,
        model_id: str,
        lease_seconds: int,
    ) -> dict[str, Any] | None:
        return await asyncio.to_thread(
            self._post_json_optional_sync,
            "/internal/v1/executor/task-claims",
            {
                "worker_id": worker_id,
                "executor_ids": executor_ids,
                "provider_id": provider_id,
                "model_id": model_id,
                "lease_seconds": lease_seconds,
            },
            self._internal_headers(),
        )

    async def pause_execution_for_approval(
        self, claim: dict[str, Any], *, run_state: dict[str, Any], schema_version: str,
        pending_sdk_tool_call_ids: list[str], worker_state: dict[str, Any],
    ) -> dict[str, Any]:
        return await self._save_execution_checkpoint(claim, "approval-checkpoint", run_state, schema_version, pending_sdk_tool_call_ids, worker_state)

    async def pause_execution_at_native_boundary(
        self, claim: dict[str, Any], *, run_state: dict[str, Any], schema_version: str,
        pending_sdk_tool_call_ids: list[str], worker_state: dict[str, Any], recovery_reason: str = "",
    ) -> dict[str, Any]:
        return await self._save_execution_checkpoint(claim, "pause-checkpoint", run_state, schema_version, pending_sdk_tool_call_ids, worker_state, recovery_reason)

    async def _save_execution_checkpoint(self, claim, endpoint, run_state, schema_version, pending_sdk_tool_call_ids, worker_state, recovery_reason=""):
        attempt = claim["attempt"]
        attempt_id = quote(str(attempt["attempt_id"]), safe="")
        return await self._post_json(
            f"/internal/v1/executor/attempts/{attempt_id}/{endpoint}",
            {"input_snapshot_hash": attempt["input_snapshot_hash"], "run_state": run_state,
             "schema_version": schema_version, "pending_sdk_tool_call_ids": pending_sdk_tool_call_ids,
             "worker_state": worker_state, **({"recovery_reason": recovery_reason} if recovery_reason else {})},
            headers={"X-Attempt-Token": str(claim["attempt_token"])},
        )

    async def heartbeat_execution_attempt(
        self, claim: dict[str, Any], *, lease_seconds: int, program_status: str = "",
    ) -> dict[str, Any]:
        attempt = claim["attempt"]
        attempt_id = quote(str(attempt["attempt_id"]), safe="")
        return await self._post_json(
            f"/internal/v1/executor/attempts/{attempt_id}/heartbeat",
            {"input_snapshot_hash": attempt["input_snapshot_hash"], "lease_seconds": lease_seconds,
             **({"program_status": program_status} if program_status else {})},
            headers={"X-Attempt-Token": str(claim["attempt_token"])},
        )

    async def record_execution_inputs_included(self, attempt_id: str, attempt_token: str, input_snapshot_hash: str, input_ids: list[str]) -> None:
        result = self._data(await self._post_json(
            f"/internal/v1/executor/attempts/{quote(attempt_id, safe='')}/inputs/included",
            {"input_snapshot_hash": input_snapshot_hash, "included_input_ids": input_ids},
            headers={"X-Attempt-Token": attempt_token},
        ))
        if result.get("attempt_id") != attempt_id or result.get("included_input_ids") != input_ids:
            raise BackendError("Execution input receipt does not match the current attempt")

    async def submit_execution_result(
        self,
        claim: dict[str, Any],
        response_payload: dict[str, Any],
        usage: dict[str, Any],
        trace_ref: str,
    ) -> dict[str, Any]:
        attempt = claim["attempt"]
        attempt_id = quote(str(attempt["attempt_id"]), safe="")
        return await self._post_json(
            f"/internal/v1/executor/attempts/{attempt_id}/results",
            {
                "input_snapshot_hash": attempt["input_snapshot_hash"],
                "response_payload": response_payload,
                "usage": usage,
                "trace_ref": trace_ref,
            },
            headers={"X-Attempt-Token": str(claim["attempt_token"])},
        )

    async def fail_execution_attempt(
        self,
        claim: dict[str, Any],
        error_code: str,
        failure_detail: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        attempt = claim["attempt"]
        attempt_id = quote(str(attempt["attempt_id"]), safe="")
        return await self._post_json(
            f"/internal/v1/executor/attempts/{attempt_id}/failures",
            {
                "input_snapshot_hash": attempt["input_snapshot_hash"],
                "error_code": error_code,
                "failure_detail": failure_detail,
            },
            headers={"X-Attempt-Token": str(claim["attempt_token"])},
        )

    async def commit_execution_result(
        self, claim: dict[str, Any], response_hash: str
    ) -> dict[str, Any]:
        attempt_id = quote(str(claim["attempt"]["attempt_id"]), safe="")
        return await self._post_json(
            f"/internal/v1/executor/attempts/{attempt_id}/commit",
            {"expected_response_hash": response_hash, "repair_output": claim.get("executor_id") != "workflow.video_script_extract"},
            headers={"X-Attempt-Token": str(claim["attempt_token"])},
        )

    async def claim_agent_task(
        self,
        *,
        worker_id: str,
        provider_id: str,
        model_id: str,
        lease_seconds: int,
    ) -> dict[str, Any] | None:
        return await asyncio.to_thread(
            self._post_json_optional_sync,
            "/internal/v1/agent-tasks/claims",
            {
                "worker_id": worker_id,
                "provider_id": provider_id,
                "model_id": model_id,
                "lease_seconds": lease_seconds,
            },
            self._internal_headers(),
        )

    async def update_agent_task_progress(
        self,
        claim: dict[str, Any],
        *,
        current: int,
        total: int,
        message: str,
        lease_seconds: int = 0,
    ) -> dict[str, Any]:
        attempt_id = quote(
            str(claim["attempt"]["agent_task_attempt_id"]), safe=""
        )
        return await self._post_json(
            f"/internal/v1/agent-task-attempts/{attempt_id}/progress",
            {"current": current, "total": total, "message": message, "lease_seconds": lease_seconds},
            headers={"X-Attempt-Token": str(claim["attempt_token"])},
        )

    async def complete_agent_task(
        self,
        claim: dict[str, Any],
        *,
        result: dict[str, Any],
        artifact_draft: dict[str, Any] | None,
        usage: dict[str, Any],
        trace_ref: str,
        included_input_ids: list[str] | None = None,
    ) -> dict[str, Any]:
        attempt_id = quote(
            str(claim["attempt"]["agent_task_attempt_id"]), safe=""
        )

        return await self._post_json(
            f"/internal/v1/agent-task-attempts/{attempt_id}/complete",
            {
                "result": result,
                "artifact_draft": artifact_draft,
                "usage": usage,
                "trace_ref": trace_ref,
                **({"included_input_ids": included_input_ids} if included_input_ids else {}),
            },
            headers={"X-Attempt-Token": str(claim["attempt_token"])},
        )

    async def pause_agent_task_for_approval(self, claim: dict[str, Any], *, run_state: dict[str, Any], schema_version: str, pending_sdk_tool_call_ids: list[str], included_input_ids: list[str] | None = None) -> dict[str, Any]:
        attempt_id = quote(str(claim["attempt"]["agent_task_attempt_id"]), safe="")
        return await self._post_json(
            f"/internal/v1/agent-task-attempts/{attempt_id}/pause-for-approval",
            {"run_state": run_state, "schema_version": schema_version, "pending_sdk_tool_call_ids": pending_sdk_tool_call_ids, **({"included_input_ids": included_input_ids} if included_input_ids else {})},
            headers={"X-Attempt-Token": str(claim["attempt_token"])},
        )

    async def complete_agent_task_pause(self, claim: dict[str, Any], *, run_state: dict[str, Any], schema_version: str, pending_sdk_tool_call_ids: list[str], included_input_ids: list[str] | None = None, recovery_reason: str = "") -> dict[str, Any]:
        attempt_id = quote(str(claim["attempt"]["agent_task_attempt_id"]), safe="")
        return await self._post_json(
            f"/internal/v1/agent-task-attempts/{attempt_id}/pause",
            {"run_state": run_state, "schema_version": schema_version, "pending_sdk_tool_call_ids": pending_sdk_tool_call_ids, **({"included_input_ids": included_input_ids} if included_input_ids else {}), **({"recovery_reason": recovery_reason} if recovery_reason else {})},
            headers={"X-Attempt-Token": str(claim["attempt_token"])},
        )

    async def fail_agent_task(
        self,
        claim: dict[str, Any],
        *,
        error_code: str,
        error_message: str,
        retryable: bool,
        included_input_ids: list[str] | None = None,
    ) -> dict[str, Any]:
        attempt_id = quote(
            str(claim["attempt"]["agent_task_attempt_id"]), safe=""
        )
        return await self._post_json(
            f"/internal/v1/agent-task-attempts/{attempt_id}/failures",
            {
                "error_code": error_code,
                "error_message": error_message,
                "retryable": retryable,
                **({"included_input_ids": included_input_ids} if included_input_ids else {}),
            },
            headers={"X-Attempt-Token": str(claim["attempt_token"])},
        )

    async def _get_json(self, path: str) -> dict[str, Any]:
        return await self._get_json_with_headers(path, self._internal_headers())

    async def _get_json_with_headers(
        self, path: str, headers: dict[str, str]
    ) -> dict[str, Any]:
        return await asyncio.to_thread(self._get_json_sync, path, headers)

    async def _post_json(
        self,
        path: str,
        payload: dict[str, Any],
        headers: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        merged_headers = dict(headers or {})
        if path.startswith("/internal/"):
            merged_headers = {**self._internal_headers(), **merged_headers}
        return await asyncio.to_thread(self._post_json_sync, path, payload, merged_headers)

    def _get_json_sync(
        self, path: str, headers: dict[str, str] | None = None
    ) -> dict[str, Any]:
        request = Request(
            self._base_url + path,
            method="GET",
            headers={"Accept": "application/json", **(headers or {})},
        )
        try:
            with urlopen(request, timeout=self._timeout_seconds) as response:
                payload = json.load(response)
        except HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            raise BackendError.from_http_response(exc.code, detail) from exc
        except (URLError, TimeoutError) as exc:
            raise BackendError(f"backend request failed: {exc}") from exc

        if not isinstance(payload, dict):
            raise BackendError("backend response is not a JSON object")
        return payload

    def _internal_headers(self) -> dict[str, str]:
        if not self._internal_token:
            raise BackendError("backend internal Agent tool channel is disabled")
        return {"Authorization": "Bearer " + self._internal_token, **(_activity_headers.get() or {})}

    def native_workspace_activity(self) -> dict[str, str]:
        values = dict(_activity_headers.get() or {})
        if not values.get("X-Agent-Project-ID") or sum(bool(values.get(key)) for key in (
                "X-Agent-Turn-ID", "X-Agent-Task-Attempt-ID", "X-Agent-Execution-Attempt-ID", "X-Agent-Memory-Generation-ID")) != 1:
            raise BackendError("Native workspace transport requires a current execution identity")
        return values

    async def claim_memory_generation(self, worker_id: str, model_id: str, lease_seconds: int, *, policy_hash: str):
        from .memory_generation import decode_memory_generation_claim

        if (not isinstance(worker_id, str) or not worker_id.strip() or len(worker_id) > 256
                or not isinstance(model_id, str) or not model_id.strip() or len(model_id) > 256
                or type(lease_seconds) is not int or not 30 <= lease_seconds <= 1800
                or not isinstance(policy_hash, str) or not re.fullmatch(r"[a-f0-9]{64}", policy_hash)):
            raise BackendError("Memory worker claim configuration is invalid")
        result = await self.memory_generation_request("claim", {"worker_id": worker_id, "model_id": model_id, "lease_seconds": lease_seconds})
        if result["claim"] is None:
            return None
        return decode_memory_generation_claim(result["claim"], model_id=model_id, policy_hash=policy_hash)

    async def complete_memory_generation(self, claim, worker_id: str):
        from .memory_generation import validate_memory_completion

        if (not isinstance(worker_id, str) or not worker_id.strip() or len(worker_id) > 256
                or claim.job.phase != "consolidation" or claim.job.status != "running"):
            raise BackendError("Memory completion requires an admitted consolidation worker")
        result = await self.memory_generation_request("complete", {"generation_id": claim.job.generation_id,
            "worker_id": worker_id, "attempt_token": claim.attempt_token, "attempt": claim.job.attempt})
        return validate_memory_completion(result["job"], claim.job)

    async def start_memory_generation(self, claim, worker_id: str):
        from .memory_generation import validate_memory_job_transition

        if not isinstance(worker_id, str) or not worker_id.strip() or len(worker_id) > 256:
            raise BackendError("Memory worker identity is invalid")
        result = await self.memory_generation_request("start", {"generation_id": claim.job.generation_id,
            "worker_id": worker_id, "attempt_token": claim.attempt_token, "attempt": claim.job.attempt})
        return validate_memory_job_transition(result["job"], claim.job, operation="start")

    async def renew_memory_generation(self, claim, worker_id: str, job, lease_seconds: int):
        from .memory_generation import validate_memory_job_transition

        if (not isinstance(worker_id, str) or not worker_id.strip() or len(worker_id) > 256
                or type(lease_seconds) is not int or not 30 <= lease_seconds <= 1800):
            raise BackendError("Memory worker renewal configuration is invalid")
        # Checkpoint writes may advance the job, but cannot change its execution.
        job = validate_memory_job_transition(job.model_dump(), job, operation="renew")
        evolved = {"revision", "lease_until", "started", "checkpoint_hash", "error_code"}
        if job.model_dump(exclude=evolved) != claim.job.model_dump(exclude=evolved) or job.revision < claim.job.revision:
            raise BackendError("Memory worker renewal belongs to a different claim")
        result = await self.memory_generation_request("renew", {"generation_id": job.generation_id,
            "worker_id": worker_id, "attempt_token": claim.attempt_token, "attempt": job.attempt, "lease_seconds": lease_seconds})
        return validate_memory_job_transition(result["job"], job, operation="renew")

    async def native_workspace_request(
        self, method: str, path: str, headers: dict[str, str], *,
        payload: dict[str, Any] | None = None, archive: bytes | None = None, binary: bool = False,
    ) -> tuple[dict[str, Any] | bytes, dict[str, str]]:
        self.native_workspace_activity()
        allowed = {"X-Agent-Dispatch-Generation", "X-Workspace-Lease-Key", "X-Workspace-Lease-Generation",
                   "X-Workspace-Snapshot-Parent", "X-Workspace-Snapshot-Sha256"}
        if method not in {"GET", "POST", "PUT"} or not path.startswith("/internal/v1/native-workspaces/") or set(headers) - allowed:
            raise BackendError("Invalid native workspace transport request")
        base = urlsplit(self._base_url)
        if base.scheme not in {"http", "https"} or not base.hostname or base.username or base.password or base.query or base.fragment:
            raise BackendError("Native workspace backend must use a trusted HTTP origin without inline credentials")
        if archive is not None and (payload is not None or not isinstance(archive, bytes) or not archive or len(archive) > 18 << 20):
            raise BackendError("Native workspace archive request exceeds its bound")
        body = archive if archive is not None else (json.dumps(payload).encode("utf-8") if payload is not None else None)
        control_limit = 24 << 20 if _native_file_path(method, path) else (2 << 20 if _native_command_path(method, path) else 4096)
        if _native_pty_path(method, path) or _native_publication_path(method, path):
            control_limit = 256 << 10
        if _native_manifest_path(method, path):
            control_limit = 64 << 10
        if archive is None and body is not None and len(body) > control_limit:
            raise BackendError("Native workspace control request exceeds its bound")
        request_headers = {**headers, **self._internal_headers(), "Accept-Encoding": "identity",
                           "Accept": "application/x-tar" if binary else "application/json"}
        if body is not None:
            request_headers["Content-Type"] = "application/x-tar" if archive is not None else "application/json"
        return await asyncio.to_thread(self._native_workspace_request_sync, method, path, request_headers, body, binary)

    def _native_workspace_request_sync(self, method, path, headers, body, binary):
        command_request = _native_command_path(method, path)
        pty_request = _native_pty_path(method, path)
        pty_cleanup = _native_pty_path(method, path, "/terminate")
        file_request = _native_file_path(method, path)
        limit = 18 << 20 if binary else (24 << 20 if file_request else (3 << 20 if command_request else 64 << 10))
        if not binary and pty_request:
            limit = 2 << 20
        elif not binary and _native_publication_path(method, path):
            limit = 512 << 10
        if not binary and _native_manifest_path(method, path, "/file"):
            limit = 24 << 20
        elif not binary and _native_manifest_path(method, path):
            limit = 128 << 10
        # This credential-bearing channel never follows redirects or takes an
        # implicit environment proxy. No global urllib configuration is changed.
        opener = build_opener(ProxyHandler({}), _NoWorkspaceRedirect())
        failed = False
        error_code, status = "NATIVE_WORKSPACE_TRANSPORT_UNCONFIRMED", None
        try:
            request = Request(self._base_url + path, data=body, method=method, headers=headers)
            timeout = max(self._timeout_seconds, 145.0) if pty_cleanup else (
                max(self._timeout_seconds, 65.0) if command_request or file_request or pty_request else self._timeout_seconds)
            with opener.open(request, timeout=timeout) as response:
                content = response.read(limit + 1)
                response_headers = response.headers
                expected_type = "application/x-tar" if binary else "application/json"
                content_length = response_headers.get("Content-Length")
                if any(len(response_headers.get_all(name, [])) > 1 for name in (
                        "Content-Length", "Content-Type", "Content-Encoding")):
                    raise ValueError("ambiguous response envelope")
                if response.status != 200 or response.geturl() != request.full_url or len(content) > limit or response_headers.get_content_type() != expected_type or response_headers.get("Content-Encoding"):
                    raise ValueError("unexpected response envelope")
                if content_length is not None and (not content_length.isdecimal() or int(content_length) != len(content)):
                    raise ValueError("response length mismatch")
                metadata = {}
                for name in ("X-Workspace-Session-ID", "X-Workspace-Snapshot-Version", "X-Workspace-Snapshot-Sha256"):
                    values = response_headers.get_all(name, [])
                    if len(values) > 1:
                        raise ValueError("duplicate response binding header")
                    if values:
                        metadata[name] = values[0]
                result = content if binary else json.loads(content)
                if not binary and not isinstance(result, dict):
                    raise ValueError("response must be an object")
        except HTTPError as exc:
            status = exc.code
            try:
                detail = exc.read(4096)
                candidate = json.loads(detail).get("error", {}).get("code", "")
                if isinstance(candidate, str) and re.fullmatch(r"[A-Z][A-Z0-9_]{0,127}", candidate):
                    error_code = candidate
            except Exception:
                pass
            finally:
                try:
                    exc.close()
                except Exception:
                    pass
            failed = True
        except Exception:
            failed = True
        if failed:
            raise BackendError("Native workspace response was not confirmed", code=error_code, status_code=status)
        return result, metadata

    @staticmethod
    def _required_data_object(payload: dict[str, Any], label: str) -> dict[str, Any]:
        data = BackendClient._data(payload)
        if not isinstance(data, dict):
            raise BackendError(f"backend {label} response data is not a JSON object")
        return data

    def _get_bytes_sync(
        self, path: str, accept: str, max_bytes: int, label: str
    ) -> tuple[bytes, str]:
        request = Request(
            self._base_url + path,
            method="GET",
            headers={"Accept": accept, **self._internal_headers()},
        )
        try:
            with urlopen(request, timeout=self._timeout_seconds) as response:
                content = response.read(max_bytes + 1)
                content_type = response.headers.get_content_type()
        except HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            raise BackendError.from_http_response(exc.code, detail) from exc
        except (URLError, TimeoutError) as exc:
            raise BackendError(f"backend request failed: {exc}") from exc
        if len(content) > max_bytes:
            raise BackendError(f"{label} exceeds the {max_bytes // (1024 * 1024)} MB limit")
        return content, content_type

    def _post_json_sync(
        self,
        path: str,
        payload: dict[str, Any],
        headers: dict[str, str],
    ) -> dict[str, Any]:
        encoded = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        request = Request(
            self._base_url + path,
            data=encoded,
            method="POST",
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json",
                **headers,
            },
        )
        try:
            with urlopen(request, timeout=self._timeout_seconds) as response:
                result = json.load(response)
        except HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            raise BackendError.from_http_response(exc.code, detail) from exc
        except (URLError, TimeoutError) as exc:
            raise BackendError(f"backend request failed: {exc}") from exc
        if not isinstance(result, dict):
            raise BackendError("backend response is not a JSON object")
        return result

    def _write_json_sync(
        self,
        method: str,
        path: str,
        payload: dict[str, Any],
        headers: dict[str, str],
    ) -> dict[str, Any]:
        encoded = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        request = Request(
            self._base_url + path,
            data=encoded,
            method=method,
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json",
                **headers,
            },
        )
        try:
            with urlopen(request, timeout=self._timeout_seconds) as response:
                result = json.load(response)
        except HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            raise BackendError.from_http_response(exc.code, detail) from exc
        except (URLError, TimeoutError) as exc:
            raise BackendError(f"backend request failed: {exc}") from exc
        if not isinstance(result, dict):
            raise BackendError("backend response is not a JSON object")
        return result

    def _post_json_optional_sync(
        self,
        path: str,
        payload: dict[str, Any],
        headers: dict[str, str],
    ) -> dict[str, Any] | None:
        encoded = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        request = Request(
            self._base_url + path,
            data=encoded,
            method="POST",
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json",
                **headers,
            },
        )
        try:
            with urlopen(request, timeout=self._timeout_seconds) as response:
                if response.status == 204:
                    return None
                result = json.load(response)
        except HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            raise BackendError.from_http_response(exc.code, detail) from exc
        except (URLError, TimeoutError) as exc:
            raise BackendError(f"backend request failed: {exc}") from exc
        if not isinstance(result, dict):
            raise BackendError("backend response is not a JSON object")
        data = self._data(result)
        if not isinstance(data, dict):
            raise BackendError("backend response data is not a JSON object")
        return data

    @staticmethod
    def _data(payload: dict[str, Any]) -> Any:
        return payload.get("data", payload)

    @classmethod
    def _items(cls, payload: dict[str, Any]) -> list[Any]:
        data = cls._data(payload)
        if isinstance(data, dict) and isinstance(data.get("items"), list):
            return data["items"]
        if isinstance(data, list):
            return data
        return []


def _retrieve_conversation_messages(
    messages: list[Any], query: str, limit: int
) -> list[dict[str, Any]]:
    query_terms = _conversation_terms(query)
    if not query_terms:
        return []
    scored: list[tuple[float, int, dict[str, Any]]] = []
    for index, message in enumerate(messages):
        if not isinstance(message, dict):
            continue
        content = str(message.get("content") or "").strip()
        if not content:
            continue
        candidate_terms = _conversation_terms(content)
        matched = len(query_terms & candidate_terms)
        if matched == 0:
            continue
        scored.append((matched / len(query_terms), index, message))
    scored.sort(key=lambda item: (-item[0], item[1]))
    return [item[2] for item in scored[:limit]]


def _conversation_terms(value: str) -> set[str]:
    normalized = value.casefold()
    terms = set(re.findall(r"[a-z0-9][a-z0-9_-]+", normalized))
    cjk_runs = re.findall(r"[\u4e00-\u9fff]+", normalized)
    for run in cjk_runs:
        terms.update(run[index : index + 2] for index in range(len(run) - 1))
    return terms
