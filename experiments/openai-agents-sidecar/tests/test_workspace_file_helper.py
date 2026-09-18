import base64
import hashlib
import io
import json
from pathlib import Path
import stat
from types import SimpleNamespace

import pytest

from test_workspace_transfer_helper import LIMIT, ProcessFixture, helper


def operation(root, kind, name, *, data=None, limit=LIMIT, **options):
    request = {"operation": kind, "path": name, **options}
    if data is not None:
        request["data"] = base64.b64encode(data).decode("ascii")
    return helper.file_operation(root, request, limit)


def test_binary_files_directories_and_bounded_listing(tmp_path):
    assert operation(tmp_path, "mkdir", ".", parents=True)["entries"][0]["directory"]
    operation(tmp_path, "mkdir", "nested/deep", parents=True)
    body = b"\x00\xff\x80\x01\n"
    receipt = operation(tmp_path, "write", "nested/deep/payload.bin", data=body)
    assert receipt["data"] == ""
    assert receipt["sha256"] == hashlib.sha256(body).hexdigest()
    assert receipt["entries"][0]["size_bytes"] == len(body)
    read = operation(tmp_path, "read", "nested/deep/payload.bin")
    assert base64.b64decode(read["data"]) == body
    assert read["sha256"] == receipt["sha256"]
    assert operation(tmp_path, "list", ".")["entries"][0]["path"] == "nested"
    assert [entry["path"] for entry in operation(tmp_path, "list", "nested")["entries"]] == ["nested/deep"]
    assert "sha256" not in operation(tmp_path, "stat", "nested/deep/payload.bin")["entries"][0]
    operation(tmp_path, "write", "nested/deep/payload.bin", data=b"replacement")
    assert (tmp_path / "nested/deep/payload.bin").read_bytes() == b"replacement"
    operation(tmp_path, "write", "empty", data=b"")
    assert operation(tmp_path, "read", "empty")["sha256"] == hashlib.sha256(b"").hexdigest()
    operation(tmp_path, "remove", "empty")
    operation(tmp_path, "remove", "nested", recursive=True)
    assert operation(tmp_path, "remove", "absent", recursive=True)["entries"] == []
    assert list(tmp_path.iterdir()) == []


@pytest.mark.parametrize("payload", [
    [], {"operation": [], "path": "file"}, {"operation": "exec", "path": "file"},
    {"operation": "write", "path": "../escape"}, {"operation": "write", "path": "/absolute"},
    {"operation": "write", "path": "C:/host"}, {"operation": "write", "path": "a\\b"},
    {"operation": "write", "path": "a/../b"}, {"operation": "write", "path": "CON.txt"},
    {"operation": "write", "path": "x" * 241}, {"operation": "write", "path": "\u4e2d" * 81},
    {"operation": "write", "path": "."}, {"operation": "remove", "path": ".", "recursive": True},
    {"operation": "write", "path": "file", "parents": True},
    {"operation": "mkdir", "path": "dir", "parents": 1},
    {"operation": "remove", "path": "file", "recursive": "true"},
    {"operation": "read", "path": "file", "recursive": True},
    {"operation": "write", "path": "file", "mode": 0o600},
    {"operation": "chmod", "path": "file"}, {"operation": "chmod", "path": "file", "mode": True},
    {"operation": "chmod", "path": "file", "mode": 0o1600},
    {"operation": "chmod", "path": "file", "mode": -1},
    {"operation": "write", "path": "file", "data": None},
    {"operation": "write", "path": "file", "data": "?"},
    {"operation": "write", "path": "file", "data": "YQ==\n"},
    {"operation": "write", "path": "file", "data": "YR=="},
    {"operation": "read", "path": "file", "data": "YQ=="},
    {"operation": "write", "path": "file", "program": "python"},
])
def test_invalid_requests_never_mutate_files(tmp_path, payload):
    existing = tmp_path / "file"
    existing.write_bytes(b"keep")
    with pytest.raises(helper.TransferError, match="WORKSPACE_FILE_REQUEST_INVALID"):
        helper.file_operation(tmp_path, payload, LIMIT)
    assert list(tmp_path.iterdir()) == [existing]
    assert existing.read_bytes() == b"keep"


@pytest.mark.parametrize("kind,name,options,code", [
    ("read", "absent", {}, "NOT_FOUND"), ("stat", "absent", {}, "NOT_FOUND"),
    ("list", "absent", {}, "NOT_FOUND"), ("remove", "absent", {}, "NOT_FOUND"),
    ("write", "missing/file", {"data": b"new"}, "NOT_FOUND"),
    ("mkdir", "missing/dir", {}, "NOT_FOUND"),
    ("write", "file/child", {"data": b"new"}, "NOT_DIRECTORY"),
    ("mkdir", "file/child", {"parents": True}, "NOT_DIRECTORY"),
    ("read", "dir", {}, "IS_DIRECTORY"), ("write", "dir", {"data": b"new"}, "IS_DIRECTORY"),
    ("list", "file", {}, "NOT_DIRECTORY"), ("remove", "dir", {}, "IS_DIRECTORY"),
    ("mkdir", "dir", {}, "EXISTS"), ("mkdir", "file", {"parents": True}, "EXISTS"),
    ("write", "FILE", {"data": b"new"}, "PATH_COLLISION"),
    ("mkdir", "Dir/new", {"parents": True}, "PATH_COLLISION"),
])
def test_path_and_type_preconditions_preserve_workspace(tmp_path, kind, name, options, code):
    (tmp_path / "file").write_bytes(b"keep")
    (tmp_path / "dir").mkdir()
    before = helper.entries(tmp_path, LIMIT)
    with pytest.raises(helper.TransferError, match="WORKSPACE_FILE_" + code):
        operation(tmp_path, kind, name, **options)
    assert helper.entries(tmp_path, LIMIT) == before


def test_atomic_overwrite_reserves_peak_bytes_and_preserves_previous_content(tmp_path):
    (tmp_path / "file").write_bytes(b"keep")
    with pytest.raises(helper.TransferError, match="WORKSPACE_FILE_LIMIT_EXCEEDED"):
        operation(tmp_path, "write", "file", data=b"new", limit=6)
    assert (tmp_path / "file").read_bytes() == b"keep"
    assert len(list(tmp_path.iterdir())) == 1
    operation(tmp_path, "write", "file", data=b"new", limit=7)
    assert (tmp_path / "file").read_bytes() == b"new"


def test_creating_file_and_parent_directories_enforces_count_limits(tmp_path):
    for index in range(256):
        (tmp_path / f"{index}.txt").touch()
    with pytest.raises(helper.TransferError, match="WORKSPACE_FILE_LIMIT_EXCEEDED"):
        operation(tmp_path, "write", "overflow", data=b"")
    for index in range(767):
        (tmp_path / f"d{index}").mkdir()
    with pytest.raises(helper.TransferError, match="WORKSPACE_FILE_LIMIT_EXCEEDED"):
        operation(tmp_path, "mkdir", "new/child", parents=True)
    assert not (tmp_path / "new").exists()
    operation(tmp_path, "mkdir", "last")
    assert len(list(tmp_path.iterdir())) == 1024


def test_metadata_operations_do_not_read_file_contents(tmp_path, monkeypatch):
    (tmp_path / "file").write_bytes(b"keep")
    monkeypatch.setattr(Path, "open", lambda *_args, **_kwargs: pytest.fail("metadata read file bytes"))
    assert operation(tmp_path, "stat", "file")["entries"][0]["size_bytes"] == 4
    assert len(operation(tmp_path, "list", ".")["entries"]) == 1


@pytest.mark.parametrize("stage", ["sync", "replace"])
def test_failed_atomic_write_preserves_old_file_and_removes_temporary(tmp_path, monkeypatch, stage):
    (tmp_path / "file").write_bytes(b"keep")

    def fail(*_args, **_kwargs):
        raise OSError("injected write failure")

    if stage == "sync":
        monkeypatch.setattr(helper.os, "fsync", fail)
    else:
        monkeypatch.setattr(Path, "replace", fail)
    with pytest.raises(OSError, match="injected write failure"):
        operation(tmp_path, "write", "file", data=b"new")
    assert (tmp_path / "file").read_bytes() == b"keep"
    assert [item.name for item in tmp_path.iterdir()] == ["file"]


def test_read_access_failure_is_safe_but_write_access_failure_is_unconfirmed(tmp_path, monkeypatch):
    (tmp_path / "file").write_bytes(b"keep")

    def deny(*_args, **_kwargs):
        raise PermissionError("injected denial")

    monkeypatch.setattr(Path, "open", deny)
    with pytest.raises(helper.TransferError, match="WORKSPACE_FILE_ACCESS_DENIED"):
        operation(tmp_path, "read", "file")
    with pytest.raises(PermissionError, match="injected denial"):
        operation(tmp_path, "write", "file", data=b"new")


def test_chmod_verifies_actual_permissions_instead_of_reporting_requested_mode(tmp_path, monkeypatch):
    target = tmp_path / "file"
    target.write_bytes(b"keep")
    actual = stat.S_IMODE(target.stat().st_mode) & 0o777
    calls = []
    # Windows chmod is not POSIX; this tests receipt checking, not Linux modes.
    monkeypatch.setattr(Path, "chmod", lambda path, mode: calls.append((path, mode)))
    assert operation(tmp_path, "chmod", "file", mode=actual)["entries"][0]["mode"] == actual
    with pytest.raises(helper.TransferError, match="WORKSPACE_FILE_OPERATION_UNCONFIRMED"):
        operation(tmp_path, "chmod", "file", mode=actual ^ 0o100)
    assert len(calls) == 2


def test_unsafe_tree_and_root_are_rejected_before_writing(tmp_path, monkeypatch):
    target = tmp_path / "link"
    target.write_bytes(b"keep")
    real_lstat = Path.lstat

    def unsafe(path, *args, **kwargs):
        if path == target:
            return SimpleNamespace(st_mode=stat.S_IFLNK | 0o777)
        return real_lstat(path, *args, **kwargs)

    monkeypatch.setattr(Path, "lstat", unsafe)
    with pytest.raises(helper.TransferError, match="WORKSPACE_ARCHIVE_ENTRY_UNSAFE"):
        operation(tmp_path, "write", "new", data=b"new")
    with pytest.raises(helper.TransferError, match="WORKSPACE_FILE_PATH_UNSAFE"):
        operation(target, "write", "new", data=b"new")
    assert not (tmp_path / "new").exists()


@pytest.mark.parametrize("resume_failure", [False, True])
def test_protocol_emits_success_only_after_processes_resume(tmp_path, monkeypatch, resume_failure):
    fixture = ProcessFixture(monkeypatch)
    fixture.resume_failure = 10 if resume_failure else None
    class Receipt(io.StringIO):
        def write(self, text):
            assert fixture.states[10][0] == "R"
            return super().write(text)

    output = Receipt()
    request = json.dumps({"operation": "write", "path": "file", "data": "YQ=="}).encode()
    # Only the protocol entrypoint is exercised. Every process and fixed root
    # is replaced before main; no host /proc scan or signal is ever permitted.
    real_os = helper.os
    fake_os = SimpleNamespace(**{name: getattr(real_os, name) for name in dir(real_os)})
    fake_os.geteuid, fake_os.getpid, fake_os.pidfd_open = lambda: 65532, lambda: 999, None
    fake_signal = SimpleNamespace(SIGSTOP=19, SIGCONT=18, pidfd_send_signal=None)
    monkeypatch.setattr(helper, "os", fake_os)
    monkeypatch.setattr(helper, "signal", fake_signal)
    monkeypatch.setattr(helper, "sys", SimpleNamespace(
        platform="linux", argv=["helper", "file", str(LIMIT)],
        stdin=SimpleNamespace(buffer=io.BytesIO(request)), stdout=output,
    ))
    real_quiescent = helper.quiescent
    monkeypatch.setattr(helper, "quiescent", lambda: real_quiescent(
        scan=lambda: dict(fixture.states), process_ref=fixture.open, own_pid=999, sleep=lambda _: None,
    ))

    def fixed_root(name):
        assert name == "/workspace"
        return tmp_path

    monkeypatch.setattr(helper, "Path", fixed_root)
    # entries uses Path for descendants too; keep it pure with its real root.
    original_entries = helper.entries

    def listing(root, limit, **options):
        with monkeypatch.context() as context:
            context.setattr(helper, "Path", Path)
            return original_entries(root, limit, **options)

    monkeypatch.setattr(helper, "entries", listing)
    if resume_failure:
        with pytest.raises(helper.TransferError, match="WORKSPACE_TRANSFER_UNCONFIRMED"):
            helper.main()
        assert output.getvalue() == ""
        assert (tmp_path / "file").read_bytes() == b"a"
    else:
        helper.main()
        assert fixture.states[10][0] == "R"
        assert json.loads(output.getvalue())["sha256"] == hashlib.sha256(b"a").hexdigest()
    assert (10, "original", 18) in fixture.signals
