from __future__ import annotations

import json
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import subprocess
import threading
from urllib.parse import parse_qs, urlparse

from content_agent_sidecar.backend import BackendClient
import content_agent_sidecar.backend as backend_module


class Handler(BaseHTTPRequestHandler):
    requests: list[str] = []
    last_post: dict[str, object] | None = None
    last_get_authorization: str | None = None

    def do_GET(self) -> None:  # noqa: N802
        type(self).requests.append(self.path)
        type(self).last_get_authorization = self.headers.get("Authorization")
        parsed = urlparse(self.path)
        if parsed.path == "/api/v1/assets/image-1/content":
            body = b"test-image-bytes"
            self.send_response(200)
            self.send_header("Content-Type", "image/jpeg")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if parsed.path == "/api/v1/assets/video-1/content":
            body = b"test-video-bytes"
            self.send_response(200)
            self.send_header("Content-Type", "video/mp4")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if parsed.path == "/api/v1/assets/text-1/parsed-text":
            query = parse_qs(parsed.query)
            data = {
                "asset_id": "text-1",
                "asset_snapshot_id": query.get("asset_snapshot_id", [""])[0],
                "content": "第一章正文",
            }
        elif parsed.path == "/api/v1/projects/project%201/assets":
            data = {"items": [{
                "asset_id": "text-1",
                "project_id": "project 1",
                "current_snapshot_id": "snapshot text 1",
                "kind": "text",
                "parse_status": "completed",
            }]}
        elif parsed.path.endswith("/messages"):
            query = parse_qs(parsed.query)
            all_messages = [
                {"role": "user", "content": "第一轮确定主角叫沈砚"},
                {"role": "assistant", "content": "已记录角色名"},
                {"role": "user", "content": "现在继续"},
                {"role": "user", "content": "项目代号青曜，林舟左手受伤"},
            ]
            term = query.get("q", [""])[0]
            data = {"items": [item for item in all_messages if not term or term in item["content"]]}
        elif parsed.path.endswith("/artifacts"):
            data = {"items": [
                {"artifact_id": "outline", "current_version_id": "v1", "title": "故事大纲", "artifact_type": "generic_document", "scope_key": "singleton", "capability_id": "generic"},
                {"artifact_id": "episode", "current_version_id": "v2", "title": "第一集", "artifact_type": "episode_script", "scope_key": "episode:1", "capability_id": "novel_to_script"},
            ]}
        elif self.path.endswith("/approvals") or self.path.endswith("/revision-requests"):
            data = {"items": []}
        elif self.path == "/api/v1/artifacts/outline":
            data = {"artifact_id": "outline", "current_version_id": "v1", "title": "故事大纲"}
        elif self.path == "/api/v1/artifact-versions/v1":
            data = {"artifact_version_id": "v1", "payload": {"content": "大纲正文"}}
        elif self.path == "/api/v1/assets/image-1":
            data = {
                "asset_id": "image-1",
                "current_snapshot_id": "snapshot-1",
                "kind": "image",
                "status": "available",
                "size_bytes": len(b"test-image-bytes"),
                "checksum_algorithm": "sha256",
                "checksum": hashlib.sha256(b"test-image-bytes").hexdigest(),
            }
        elif self.path == "/api/v1/assets/video-1":
            data = {
                "asset_id": "video-1",
                "current_snapshot_id": "snapshot-1",
                "kind": "video",
                "status": "available",
                "size_bytes": len(b"test-video-bytes"),
                "checksum_algorithm": "sha256",
                "checksum": hashlib.sha256(b"test-video-bytes").hexdigest(),
            }
        else:
            data = {"path": self.path, "project_id": "project 1"}
        body = json.dumps({"data": data}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def do_POST(self) -> None:  # noqa: N802
        content_length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(content_length))
        type(self).last_post = {
            "path": self.path,
            "body": body,
            "authorization": self.headers.get("Authorization"),
            "idempotency_key": self.headers.get("Idempotency-Key"),
        }
        encoded = json.dumps({"data": {"agent_message": {"content": "已提交"}}}).encode()
        self.send_response(201)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)


def test_backend_client_reads_authoritative_snapshot() -> None:
    Handler.requests = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        result = asyncio.run(client.get_project_snapshot("project 1"))
        assert result["data"]["path"] == "/api/v1/projects/project%201/snapshot"
        assert Handler.last_get_authorization == "Bearer internal-token"
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_executes_skill_script_through_internal_audited_call() -> None:
    Handler.last_post = None
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        asyncio.run(
            client.execute_skill_script(
                "tool-call-1",
                "sdk-call-2",
                {
                    "skill_name": "sandbox-skill",
                    "script_id": "render",
                    "input_json": '{"title":"demo"}',
                },
            )
        )
        assert Handler.last_post is not None
        assert Handler.last_post["path"] == (
            "/internal/v1/agent-tool-calls/tool-call-1/skill-script-executions"
        )
        assert Handler.last_post["authorization"] == "Bearer internal-token"
        assert Handler.last_post["body"] == {
            "expected_sdk_tool_call_id": "sdk-call-2",
            "arguments": {
                "skill_name": "sandbox-skill",
                "script_id": "render",
                "input_json": '{"title":"demo"}',
            },
        }
    finally:
        server.shutdown()
        thread.join(timeout=2)
        server.server_close()


def test_backend_client_reads_authoritative_project_goal() -> None:
    Handler.requests = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        result = asyncio.run(client.get_project_goal("project 1"))
        assert result["data"]["path"] == "/api/v1/projects/project%201/goal"
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_lists_assets_and_reads_parsed_text_snapshot() -> None:
    Handler.requests = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        assets = asyncio.run(client.get_project_assets("project 1"))
        parsed = asyncio.run(
            client.get_parsed_asset_text("text-1", "snapshot text 1")
        )

        assert assets["data"]["items"][0]["asset_id"] == "text-1"
        assert parsed["data"]["content"] == "第一章正文"
        assert (
            "/api/v1/assets/text-1/parsed-text?asset_snapshot_id=snapshot+text+1"
            in Handler.requests
        )
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_builds_validated_image_data_url() -> None:
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        result = asyncio.run(client.get_image_data_url("image-1", "snapshot-1"))

        assert result == "data:image/jpeg;base64,dGVzdC1pbWFnZS1ieXRlcw=="
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_builds_validated_video_frame_data_urls(monkeypatch) -> None:  # type: ignore[no-untyped-def]
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        monkeypatch.setattr(
            client,
            "_extract_video_frames_sync",
            lambda content: [
                "data:image/jpeg;base64,ZnJhbWU="
                if content == b"test-video-bytes"
                else "unexpected"
            ],
        )
        import asyncio

        result = asyncio.run(
            client.get_video_frame_data_urls("video-1", "snapshot-1")
        )

        assert result == ["data:image/jpeg;base64,ZnJhbWU="]
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_project_overview_excludes_pending_proposed_actions() -> None:
    Handler.requests = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        result = asyncio.run(client.get_project_overview("project 1"))
        assert "pending_proposed_actions" not in result
        assert not any(path.endswith("/proposed-actions") for path in Handler.requests)
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_searches_artifacts_without_loading_payloads() -> None:
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        result = asyncio.run(client.search_artifacts("project 1", query="第一集"))
        assert result == {"items": [{
            "artifact_id": "episode",
            "current_version_id": "v2",
            "title": "第一集",
            "artifact_type": "episode_script",
            "scope_key": "episode:1",
            "capability_id": "novel_to_script",
            "run_id": None,
            "status": None,
            "updated_at": None,
        }], "count": 1}
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_reads_current_artifact_version_exactly() -> None:
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        result = asyncio.run(client.get_current_artifact_version("outline"))
        assert result["artifact"]["title"] == "故事大纲"
        assert result["version"]["payload"]["content"] == "大纲正文"
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_searches_authoritative_conversation_history() -> None:
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        result = asyncio.run(
            client.search_conversation_messages("conversation 1", "沈砚", limit=5)
        )
        assert result == {
            "items": [{"role": "user", "content": "第一轮确定主角叫沈砚"}],
            "count": 1,
        }
        assert Handler.requests[-1].endswith("?q=%E6%B2%88%E7%A0%9A&limit=5")
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_can_read_full_conversation_for_session_bootstrap() -> None:
    Handler.requests = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        result = asyncio.run(client.get_conversation_messages("conversation 1", limit=0))
        assert result["count"] == 4
        assert Handler.requests[-1].endswith("/messages")
        assert "?limit=" not in Handler.requests[-1]
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_retrieves_rephrased_authoritative_history() -> None:
    Handler.requests = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}", internal_token="internal-token"
        )
        import asyncio

        result = asyncio.run(
            client.search_conversation_messages(
                "conversation 1", "项目代号是什么，林舟哪里受伤", limit=5
            )
        )

        assert result["items"] == [
            {"role": "user", "content": "项目代号青曜，林舟左手受伤"}
        ]
        assert Handler.requests[-1].endswith("/messages")
    finally:
        server.shutdown()
        server.server_close()


def test_backend_client_commits_turn_with_internal_auth_and_idempotency() -> None:
    Handler.last_post = None
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        client = BackendClient(
            f"http://127.0.0.1:{server.server_port}",
            internal_token="internal-token",
        )
        import asyncio

        result = asyncio.run(
            client.commit_agent_turn(
                "project",
                "conversation",
                {"content": "生成一份大纲"},
                {"intent": "create_artifact"},
                "11111111-1111-4111-8111-111111111111",
                "turn_test",
            )
        )
        assert result["data"]["agent_message"]["content"] == "已提交"
        assert Handler.last_post == {
            "path": "/internal/v1/agent/turn-commits",
            "body": {
                "project_id": "project",
                "conversation_id": "conversation",
                "request": {"content": "生成一份大纲"},
                "decision": {"intent": "create_artifact"},
                "agent_turn_id": "turn_test",
            },
            "authorization": "Bearer internal-token",
            "idempotency_key": "11111111-1111-4111-8111-111111111111",
        }
    finally:
        server.shutdown()
        server.server_close()


def test_video_extraction_uses_configured_writable_temp_directory(
    tmp_path: Path, monkeypatch
) -> None:  # type: ignore[no-untyped-def]
    configured = tmp_path / "sidecar-temp"
    commands: list[list[str]] = []

    def run(command, **kwargs):  # type: ignore[no-untyped-def]
        commands.append(command)
        if "-show_entries" in command:
            return subprocess.CompletedProcess(command, 0, stdout="1.0", stderr="")
        Path(command[-1].replace("%02d", "01")).write_bytes(b"jpeg-frame")
        return subprocess.CompletedProcess(command, 0, stdout=b"", stderr=b"")

    monkeypatch.setattr(backend_module.subprocess, "run", run)
    client = BackendClient("http://127.0.0.1:9", temp_dir=str(configured))

    frames = client._extract_video_frames_sync(b"video")

    assert all(str(configured.resolve()) in " ".join(command) for command in commands)
    assert frames == ["data:image/jpeg;base64,anBlZy1mcmFtZQ=="]
    assert list(configured.iterdir()) == []
