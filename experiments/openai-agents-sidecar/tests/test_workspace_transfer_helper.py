import importlib.util
import io
from pathlib import Path
import tarfile

import pytest


HELPER_PATH = Path(__file__).resolve().parents[3] / "backend/internal/scriptsandbox/workspace_transfer.py"
spec = importlib.util.spec_from_file_location("workspace_transfer_under_test", HELPER_PATH)
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)
LIMIT = 1 << 20


def archive_bytes(items):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w", format=tarfile.PAX_FORMAT) as archive:
        for name, kind, content in items:
            item = tarfile.TarInfo(name)
            item.type, item.mode = kind, 0o666
            item.size = len(content)
            if kind in {tarfile.SYMTYPE, tarfile.LNKTYPE}:
                item.linkname = "/escape"
            archive.addfile(item, io.BytesIO(content))
    return output.getvalue()


def test_real_binary_archive_hydration_repeat_and_nonoverwrite(tmp_path):
    source, destination = tmp_path / "source", tmp_path / "destination"
    (source / "nested").mkdir(parents=True)
    (source / "empty").mkdir()
    (source / "nested/payload.bin").write_bytes(b"\x00\xff\x80\x01\n")
    destination.mkdir()
    expected = helper.entries(source, LIMIT)
    stream = io.BytesIO()
    helper.export_archive(source, expected, stream)
    data = stream.getvalue()
    assert helper.hydrate_archive(destination, data, expected, LIMIT) == expected
    assert (destination / "nested/payload.bin").read_bytes() == b"\x00\xff\x80\x01\n"
    stat = (destination / "nested/payload.bin").stat()
    assert helper.hydrate_archive(destination, data, expected, LIMIT) == expected
    assert (destination / "nested/payload.bin").stat().st_mtime_ns == stat.st_mtime_ns
    (destination / "nested/payload.bin").write_bytes(b"keep changed content")
    with pytest.raises(helper.TransferError, match="WORKSPACE_HYDRATE_CONFLICT"):
        helper.hydrate_archive(destination, data, expected, LIMIT)
    assert (destination / "nested/payload.bin").read_bytes() == b"keep changed content"


@pytest.mark.parametrize("name", ["", ".", "..", "../escape", "/absolute", "C:/host", "a\\b", "a/../b",
                                  "a\tb", "CON.a.b", "a./file", "a /file", "a?/file", "a//b", "x" * 241])
def test_portable_paths_reject_unsafe_names(name):
    assert not helper.safe_path(name)


@pytest.mark.parametrize("name", ["draft.md", "nested/data.bin", "\u4e2d\u6587/notes.txt", "a-b_2", "COM10.txt"])
def test_portable_paths_accept_valid_names(name):
    assert helper.safe_path(name)


@pytest.mark.parametrize("name,kind,code", [
    ("../escape", tarfile.REGTYPE, "WORKSPACE_ARCHIVE_PATH_UNSAFE"),
    ("linked", tarfile.SYMTYPE, "WORKSPACE_ARCHIVE_ENTRY_UNSAFE"),
    ("linked", tarfile.LNKTYPE, "WORKSPACE_ARCHIVE_ENTRY_UNSAFE"),
    ("fifo", tarfile.FIFOTYPE, "WORKSPACE_ARCHIVE_ENTRY_UNSAFE"),
])
def test_hydration_validates_all_members_before_first_write(tmp_path, name, kind, code):
    data = archive_bytes([("first.txt", tarfile.REGTYPE, b"must not be written"), (name, kind, b"")])
    with pytest.raises(helper.TransferError, match=code):
        helper.hydrate_archive(tmp_path, data, [], LIMIT)
    assert list(tmp_path.iterdir()) == []


def test_hydration_rejects_metadata_hash_mismatch_before_writing(tmp_path):
    data = archive_bytes([("one.txt", tarfile.REGTYPE, b"actual")])
    with pytest.raises(helper.TransferError, match="WORKSPACE_HYDRATE_UNCONFIRMED"):
        helper.hydrate_archive(tmp_path, data, [], LIMIT)
    assert list(tmp_path.iterdir()) == []


def test_entries_enforce_file_and_byte_quotas(tmp_path):
    (tmp_path / "large.bin").write_bytes(b"x" * (LIMIT + 1))
    with pytest.raises(helper.TransferError, match="WORKSPACE_ARCHIVE_LIMIT_EXCEEDED"):
        helper.entries(tmp_path, LIMIT)
    (tmp_path / "large.bin").unlink()
    for index in range(257):
        (tmp_path / f"{index}.txt").touch()
    with pytest.raises(helper.TransferError, match="WORKSPACE_ARCHIVE_LIMIT_EXCEEDED"):
        helper.entries(tmp_path, LIMIT)


class ProcessFixture:
    def __init__(self, monkeypatch):
        monkeypatch.setattr(helper.signal, "SIGSTOP", 19, raising=False)
        monkeypatch.setattr(helper.signal, "SIGCONT", 18, raising=False)
        self.states = {1: ("S", "init"), 999: ("R", "helper"), 10: ("R", "original"), 11: ("T", "already-stopped")}
        self.signals, self.closed = [], []
        self.resume_failure = None
        self.never_stop = False

    def open(self, pid):
        owner, identity = self, self.states[pid]

        class Ref:
            def send(self, value):
                owner.signals.append((pid, identity[1], value))
                if value == 18 and owner.resume_failure == pid:
                    raise OSError("resume failed")
                if owner.states.get(pid, (None, None))[1] != identity[1]:
                    raise ProcessLookupError()
                state = "T" if value == 19 else "R"
                if not owner.never_stop:
                    owner.states[pid] = (state, identity[1])

            def close(self):
                owner.closed.append((pid, identity[1]))

        return Ref()

    def quiet(self):
        # Never use the default scanner or process signalling against this host.
        return helper.quiescent(scan=lambda: dict(self.states), process_ref=self.open,
                                own_pid=999, sleep=lambda _: None)


def test_quiescence_releases_only_processes_it_stopped_on_body_failure(monkeypatch):
    fixture = ProcessFixture(monkeypatch)
    with pytest.raises(ValueError, match="archive failed"):
        with fixture.quiet():
            assert fixture.states[10][0] == "T"
            raise ValueError("archive failed")
    assert fixture.signals == [(10, "original", 19), (10, "original", 18)]
    assert fixture.closed == [(10, "original")]
    assert fixture.states[11][0] == "T"


def test_quiescence_pid_reuse_does_not_resume_new_process(monkeypatch):
    fixture = ProcessFixture(monkeypatch)
    with fixture.quiet():
        fixture.states[10] = ("T", "replacement")
    assert fixture.states[10] == ("T", "replacement")
    assert fixture.closed == [(10, "original")]


def test_quiescence_attempts_all_resumes_and_reports_failure(monkeypatch):
    fixture = ProcessFixture(monkeypatch)
    fixture.states[12] = ("R", "second")
    fixture.resume_failure = 10
    with pytest.raises(helper.TransferError, match="WORKSPACE_TRANSFER_UNCONFIRMED"):
        with fixture.quiet():
            pass
    assert (12, "second", 18) in fixture.signals
    assert len(fixture.closed) == 2


def test_quiescence_busy_never_enters_archive_and_releases_references(monkeypatch):
    fixture = ProcessFixture(monkeypatch)
    fixture.never_stop = True
    with pytest.raises(helper.TransferError, match="WORKSPACE_BUSY"):
        with fixture.quiet():
            pytest.fail("archive ran before user processes stopped")
    assert fixture.signals[-1] == (10, "original", 18)
    assert fixture.closed == [(10, "original")]


def test_quiescence_fd_quota_is_fail_closed(monkeypatch):
    fixture = ProcessFixture(monkeypatch)
    fixture.states.update({index: ("R", str(index)) for index in range(100, 160)})
    with pytest.raises(helper.TransferError, match="WORKSPACE_BUSY"):
        with fixture.quiet():
            pytest.fail("archive ran after reference quota overflow")
    assert len(fixture.closed) == 48
    assert sum(value == 18 for _, _, value in fixture.signals) == 48


def test_main_never_signals_on_unsupported_host(monkeypatch):
    monkeypatch.setattr(helper.sys, "platform", "win32")
    monkeypatch.setattr(helper, "processes", lambda: pytest.fail("host process scan"))
    with pytest.raises(helper.TransferError, match="WORKSPACE_ADAPTER_UNAVAILABLE"):
        helper.main()
