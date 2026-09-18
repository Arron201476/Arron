import asyncio
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock
from unittest.mock import Mock

import pytest


source = Path(__file__).resolve().parents[3] / "backend/internal/scriptsandbox/computer_browser.py"
spec = importlib.util.spec_from_file_location("computer_browser_fixture", source)
browser = importlib.util.module_from_spec(spec)
spec.loader.exec_module(browser)


def page():
    return SimpleNamespace(
        viewport_size={"width": 100, "height": 100}, is_closed=lambda: False,
        keyboard=SimpleNamespace(down=AsyncMock(), up=AsyncMock(), insert_text=AsyncMock()),
        mouse=SimpleNamespace(click=AsyncMock(), dblclick=AsyncMock(), move=AsyncMock(), wheel=AsyncMock(), down=AsyncMock(), up=AsyncMock()),
        go_back=AsyncMock(), go_forward=AsyncMock(), wait_for_timeout=AsyncMock(), screenshot=AsyncMock(return_value=b"png-fixture"),
    )


@pytest.mark.parametrize("action,method,args", [
    ({"type": "click", "x": 5, "y": 6, "button": "left"}, "click", (5, 6)),
    ({"type": "double_click", "x": 5, "y": 6}, "dblclick", (5, 6)),
    ({"type": "move", "x": 5, "y": 6}, "move", (5, 6)),
    ({"type": "scroll", "x": 5, "y": 6, "scroll_x": 7, "scroll_y": -8}, "wheel", (7, -8)),
])
def test_mouse_actions_dispatch_to_playwright(action, method, args):
    target = page()
    asyncio.run(browser.execute_action(target, action, 100, 100))
    assert getattr(target.mouse, method).await_args.args == args


def test_keyboard_type_wait_screenshot_and_history_dispatch():
    async def run():
        target = page()
        for action in [{"type": "type", "text": "fixture"}, {"type": "wait"},
                       {"type": "keypress", "keys": ["CTRL", "a"]},
                       {"type": "click", "x": 0, "y": 0, "button": "back"},
                       {"type": "click", "x": 0, "y": 0, "button": "forward"}]:
            await browser.execute_action(target, action, 100, 100)
        target.keyboard.insert_text.assert_awaited_once_with("fixture")
        assert [call.args[0] for call in target.keyboard.down.await_args_list] == ["Control", "a"]
        assert [call.args[0] for call in target.keyboard.up.await_args_list] == ["a", "Control"]
        target.go_back.assert_awaited_once()
        target.go_forward.assert_awaited_once()
        assert await browser.execute_action(target, {"type": "screenshot"}, 100, 100) == {"png_base64": "cG5nLWZpeHR1cmU="}
        target.screenshot.assert_awaited_once_with(type="png", full_page=False, animations="disabled")

    asyncio.run(run())


@pytest.mark.parametrize("failure", [RuntimeError("fixture"), asyncio.CancelledError()])
def test_drag_failure_releases_mouse_and_all_modifiers(failure):
    target = page()
    target.mouse.move.side_effect = [None, failure]
    target.keyboard.up.side_effect = [RuntimeError("release failure"), None]
    with pytest.raises(type(failure)):
        asyncio.run(browser.execute_action(target, {"type": "drag", "path": [[0, 0], [1, 1]], "keys": ["CTRL", "SHIFT"]}, 100, 100))
    target.mouse.up.assert_awaited_once_with(button="left")
    assert [call.args[0] for call in target.keyboard.up.await_args_list] == ["Shift", "Control"]


@pytest.mark.parametrize("action", [
    {"type": "shell", "command": "anything"}, {"type": "move", "x": -1, "y": 0},
    {"type": "move", "x": True, "y": 0}, {"type": "move", "x": 100, "y": 0},
    {"type": "type", "text": "x" * 65537}, {"type": "wait", "seconds": 300},
    {"type": "click", "x": 0, "y": 0, "button": "back", "keys": ["CTRL"]},
    {"type": "move", "x": 0, "y": 0, "keys": ["a"]}, {"type": "keypress", "keys": ["CTRL", "Control"]},
])
def test_invalid_requests_do_not_dispatch(action):
    target = page()
    with pytest.raises(browser.BrowserActionError):
        asyncio.run(browser.execute_action(target, action, 100, 100))
    target.keyboard.down.assert_not_awaited()
    target.mouse.click.assert_not_awaited()
    target.mouse.move.assert_not_awaited()


def frame(sequence=1, **changes):
    value = {"session_id": "browser-1", "generation": 1, "sequence": sequence,
             "action": {"type": "click", "x": 2, "y": 3, "button": "left"}, **changes}
    return (json.dumps(value) + "\n").encode()


def test_session_executes_once_and_rejects_duplicate_frame():
    async def run():
        target, close = page(), AsyncMock()
        session = browser.BrowserSession(target, close, "browser-1", 1, 100, 100)
        receipt = await session.handle(frame())
        assert receipt == {"session_id": "browser-1", "generation": 1, "sequence": 1, "status": "completed"}
        with pytest.raises(browser.BrowserActionError, match="sequence"):
            await session.handle(frame())
        target.mouse.click.assert_awaited_once()
        close.assert_awaited_once()
        assert session.closed
        with pytest.raises(browser.BrowserActionError, match="closed"):
            await session.handle(frame(2))

    asyncio.run(run())


@pytest.mark.parametrize("raw", [
    frame(session_id="foreign"), frame(generation=2), frame(sequence=True), frame(2),
    frame(extra="unknown"), b'{"sequence":1,"sequence":2}\n', b'{"x":NaN}\n', b'[]\n', b'{}',
])
def test_invalid_frames_close_without_browser_effects(raw):
    async def run():
        target, close = page(), AsyncMock()
        session = browser.BrowserSession(target, close, "browser-1", 1, 100, 100)
        with pytest.raises(browser.BrowserActionError):
            await session.handle(raw)
        target.mouse.click.assert_not_awaited()
        close.assert_awaited_once()

    asyncio.run(run())


def test_cancelled_inflight_session_is_closed_without_replay():
    async def run():
        target, close = page(), AsyncMock()
        entered = asyncio.Event()

        async def click(*args, **kwargs):
            entered.set()
            await asyncio.Event().wait()

        target.mouse.click.side_effect = click
        session = browser.BrowserSession(target, close, "browser-1", 1, 100, 100)
        task = asyncio.create_task(session.handle(frame()))
        await asyncio.wait_for(entered.wait(), timeout=1)
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert session.sequence == 1 and session.closed
        close.assert_awaited_once()
        target.mouse.click.assert_awaited_once()

    asyncio.run(run())


@pytest.mark.parametrize("suppress_cancel", [False, True])
def test_explicit_close_interrupts_action_and_blocks_queued_frames(suppress_cancel):
    async def run():
        target, close = page(), AsyncMock()
        entered = asyncio.Event()

        async def click(*args, **kwargs):
            entered.set()
            try:
                await asyncio.Event().wait()
            except asyncio.CancelledError:
                if not suppress_cancel:
                    raise

        target.mouse.click.side_effect = click
        session = browser.BrowserSession(target, close, "browser-1", 1, 100, 100)
        active = asyncio.create_task(session.handle(frame()))
        await asyncio.wait_for(entered.wait(), timeout=1)
        queued = asyncio.create_task(session.handle(frame(2)))
        try:
            await asyncio.wait_for(session.close(), timeout=1)
            results = await asyncio.gather(active, queued, return_exceptions=True)
            assert isinstance(results[0], browser.BrowserActionError if suppress_cancel else asyncio.CancelledError)
            assert isinstance(results[1], browser.BrowserActionError)
            target.mouse.click.assert_awaited_once()
            close.assert_awaited_once()
        finally:
            for task in (active, queued):
                if not task.done():
                    task.cancel()
            await asyncio.gather(active, queued, return_exceptions=True)

    asyncio.run(run())


def test_failed_close_can_retry_cleanup_but_never_actions():
    async def run():
        target = page()
        close = AsyncMock(side_effect=[RuntimeError("cleanup failed"), None])
        session = browser.BrowserSession(target, close, "browser-1", 1, 100, 100)
        with pytest.raises(RuntimeError):
            await session.close()
        with pytest.raises(browser.BrowserActionError, match="closed"):
            await session.handle(frame())
        await session.close()
        await session.close()
        assert close.await_count == 2
        target.mouse.click.assert_not_awaited()

    asyncio.run(run())


def provisioning():
    target = page()
    target.goto = AsyncMock()
    target.set_default_timeout = Mock()
    target.set_default_navigation_timeout = Mock()
    context = SimpleNamespace(new_page=AsyncMock(return_value=target), close=AsyncMock())
    owned = SimpleNamespace(new_context=AsyncMock(return_value=context), close=AsyncMock())
    engine = SimpleNamespace(chromium=SimpleNamespace(launch=AsyncMock(return_value=owned)))
    return engine, owned, context, target


def test_provisioning_uses_fresh_sandboxed_browser_and_closes_owned_resources():
    async def run():
        engine, owned, context, target = provisioning()
        session = await browser.open_browser_session(engine, "browser-1", 1, 100, 100, "https://example.test/")
        engine.chromium.launch.assert_awaited_once_with(headless=True, chromium_sandbox=True)
        config = owned.new_context.await_args.kwargs
        assert config["permissions"] == [] and config["accept_downloads"] is False
        assert config["service_workers"] == "block" and config["ignore_https_errors"] is False
        target.goto.assert_awaited_once_with("https://example.test/", wait_until="domcontentloaded")
        await session.close()
        context.close.assert_awaited_once()
        owned.close.assert_awaited_once()

    asyncio.run(run())


@pytest.mark.parametrize("stage", ["context", "page", "navigation"])
def test_partial_provisioning_failure_closes_browser(stage):
    async def run():
        engine, owned, context, target = provisioning()
        operation = {"context": owned.new_context, "page": context.new_page, "navigation": target.goto}[stage]
        operation.side_effect = RuntimeError("fixture provisioning failure")
        with pytest.raises(RuntimeError, match="provisioning failure"):
            await browser.open_browser_session(engine, "browser-1", 1, 100, 100, "https://example.test/")
        owned.close.assert_awaited_once()
        if stage != "context":
            context.close.assert_awaited_once()

    asyncio.run(run())


@pytest.mark.parametrize("url", ["file:///etc/passwd", "javascript:alert(1)", "https://user:secret@example.test", "https://example.test:99999", "https://example.test/\n"])
def test_invalid_initial_url_does_not_launch_browser(url):
    engine, *_ = provisioning()
    with pytest.raises(browser.BrowserActionError):
        asyncio.run(browser.open_browser_session(engine, "browser-1", 1, 100, 100, url))
    engine.chromium.launch.assert_not_awaited()


def startup_frame(**changes):
    value = {"operation": "start", "sequence": 0, "session_id": "browser-1", "generation": 1,
             "width": 100, "height": 100, "initial_url": "https://example.test/", **changes}
    return (json.dumps(value) + "\n").encode()


@pytest.mark.parametrize("explicit_close", [False, True])
def test_frame_service_start_action_close_and_eof(explicit_close):
    async def run():
        engine, owned, context, target = provisioning()
        frames = [startup_frame(), frame()]
        if explicit_close:
            frames.append(frame(2, action={"type": "close"}))
        pending = iter(frames)
        responses = []

        async def read():
            return next(pending, b"")

        async def write(value):
            responses.append(value)

        assert await browser.serve_browser(read, write, engine)
        assert responses[0]["status"] == "ready" and responses[0]["sequence"] == 0
        assert responses[1]["status"] == "completed" and responses[1]["sequence"] == 1
        if explicit_close:
            assert responses[2]["status"] == "closed" and responses[2]["sequence"] == 2
        target.mouse.click.assert_awaited_once()
        context.close.assert_awaited_once()
        owned.close.assert_awaited_once()

    asyncio.run(run())


def test_service_failure_returns_only_redacted_code_and_closes_resources():
    async def run():
        engine, owned, context, target = provisioning()
        target.mouse.click.side_effect = RuntimeError("private-page-content-and-token")
        pending = iter([startup_frame(), frame()])
        responses = []

        async def read():
            return next(pending, b"")

        async def write(value):
            responses.append(value)

        assert not await browser.serve_browser(read, write, engine)
        assert responses[-1] == {"status": "failed", "error_code": "BROWSER_SESSION_FAILED"}
        assert "private" not in json.dumps(responses)
        owned.close.assert_awaited_once()
        context.close.assert_awaited_once()

    asyncio.run(run())


def test_service_broken_output_pipe_closes_browser():
    async def run():
        engine, owned, context, target = provisioning()
        read = AsyncMock(return_value=startup_frame())
        write = AsyncMock(side_effect=BrokenPipeError())
        assert not await browser.serve_browser(read, write, engine)
        target.mouse.click.assert_not_awaited()
        owned.close.assert_awaited_once()
        context.close.assert_awaited_once()

    asyncio.run(run())


def test_service_invalid_startup_never_creates_browser():
    async def run():
        engine, *_ = provisioning()
        read = AsyncMock(return_value=startup_frame(sequence=True))
        write = AsyncMock()
        assert not await browser.serve_browser(read, write, engine)
        engine.chromium.launch.assert_not_awaited()
        write.assert_awaited_once_with({"status": "failed", "error_code": "BROWSER_SESSION_FAILED"})

    asyncio.run(run())
