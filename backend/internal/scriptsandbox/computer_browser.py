"""Browser action executor for a Runtime-owned isolated Playwright page.

Provisioning, egress isolation, authorization and audit belong to the caller.
This module never attaches to a host browser or launches one on import.
"""

import base64
import asyncio
import json
import ipaddress
import sys
from urllib.parse import urlsplit


class BrowserActionError(ValueError):
    pass


def browser_proxy_config(value):
    if not isinstance(value, dict) or set(value) != {"host", "port", "token"}:
        raise BrowserActionError("invalid browser proxy configuration")
    host, port, token = value["host"], value["port"], value["token"]
    try:
        if not isinstance(host, str) or "%" in host:
            raise ValueError("invalid proxy address")
        address = ipaddress.ip_address(host)
        if address.is_unspecified or address.is_multicast:
            raise ValueError("invalid proxy address")
    except ValueError:
        raise BrowserActionError("invalid browser proxy configuration") from None
    if (type(port) is not int or not 1 <= port <= 65535 or not isinstance(token, str)
            or len(token) != 64 or any(c not in "0123456789abcdefABCDEF" for c in token)):
        raise BrowserActionError("invalid browser proxy configuration")
    authority = f"[{address}]" if address.version == 6 else str(address)
    return {"server": f"http://{authority}:{port}", "username": "computer", "password": token}


async def open_browser_session(playwright, session_id, generation, width, height, initial_url, proxy):
    """Provision a fresh browser inside the caller's isolated execution service.

    URL syntax validation is not an egress policy. The caller must enforce its
    approved network boundary before invoking this function.
    """
    validate_action({"type": "wait"}, width, height)
    proxy_config = browser_proxy_config(proxy)
    if (not isinstance(session_id, str) or not session_id or len(session_id) > 256
            or not session_id.isprintable() or session_id.strip() != session_id
            or type(generation) is not int or generation < 1):
        raise BrowserActionError("invalid browser session identity")
    try:
        if not isinstance(initial_url, str) or len(initial_url) > 8192 or not initial_url.isprintable():
            raise ValueError("invalid initial URL")
        parsed = urlsplit(initial_url)
        if (parsed.scheme not in {"https", "http"} or not parsed.hostname or parsed.username is not None
                or parsed.password is not None or "\\" in initial_url or parsed.port == 0):
            raise ValueError("invalid initial URL")
    except ValueError as exc:
        raise BrowserActionError("invalid initial browser URL") from exc
    browser = None
    context = None

    async def cleanup():
        nonlocal browser, context
        errors = []
        for name, resource in (("context", context), ("browser", browser)):
            if resource is None:
                continue
            try:
                await asyncio.wait_for(resource.close(), timeout=5)
                if name == "context":
                    context = None
                else:
                    browser = None
                    context = None
            except BaseException as exc:
                errors.append(exc)
        if errors:
            raise BaseExceptionGroup("Browser provisioning cleanup failed", errors)

    try:
        async with asyncio.timeout(30):
            browser = await playwright.chromium.launch(headless=True, chromium_sandbox=True, proxy=proxy_config)
            context = await browser.new_context(viewport={"width": width, "height": height},
                                                device_scale_factor=1, accept_downloads=False,
                                                permissions=[], service_workers="block", ignore_https_errors=False)
            page = await context.new_page()
            page.set_default_timeout(10000)
            page.set_default_navigation_timeout(15000)
            await page.goto(initial_url, wait_until="domcontentloaded")
            return BrowserSession(page, cleanup, session_id, generation, width, height)
    except BaseException as exc:
        try:
            await cleanup()
        except BaseException:
            exc.add_note("Browser provisioning cleanup unconfirmed")
        raise


def parse_frame(raw):
    def unique_pairs(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise BrowserActionError("duplicate frame field")
            result[key] = value
        return result

    if not isinstance(raw, bytes) or not raw.endswith(b"\n") or len(raw) > 256 * 1024:
        raise BrowserActionError("invalid browser frame")
    def reject_constant(value):
        raise BrowserActionError("invalid JSON number")

    try:
        value = json.loads(raw, object_pairs_hook=unique_pairs,
                           parse_constant=reject_constant)
    except (ValueError, UnicodeError) as exc:
        raise BrowserActionError("invalid browser frame JSON") from exc
    if not isinstance(value, dict):
        raise BrowserActionError("browser frame must be an object")
    return value


class BrowserSession:
    """Serial executor inside an already isolated, Runtime-owned browser session.

    The Go owner must authenticate/authorize before sending frames. Execution
    receipts intentionally contain no authorization claim.
    """

    def __init__(self, page, close_context, session_id, generation, width, height):
        if (not isinstance(session_id, str) or not session_id or len(session_id) > 256
                or not session_id.isprintable() or session_id.strip() != session_id
                or type(generation) is not int or generation < 1):
            raise BrowserActionError("invalid browser session identity")
        validate_action({"type": "wait"}, width, height)
        self.page, self.close_context = page, close_context
        self.session_id, self.generation = session_id, generation
        self.width, self.height = width, height
        self.sequence = 0
        self.closed = False
        self.cleaned = False
        self.active = None
        self.lock = asyncio.Lock()

    async def _cleanup(self):
        self.closed = True
        if not self.cleaned:
            await asyncio.wait_for(self.close_context(), timeout=5)
            self.cleaned = True

    async def close(self):
        self.closed = True
        if self.active is not None and not self.active.done() and not self.active.cancelling():
            self.active.cancel()
        async with self.lock:
            await self._cleanup()

    async def handle(self, raw):
        async with self.lock:
            if self.closed:
                raise BrowserActionError("browser session is closed")
            try:
                frame = parse_frame(raw)
                if (set(frame) != {"session_id", "generation", "sequence", "action"}
                        or frame["session_id"] != self.session_id
                        or type(frame["generation"]) is not int or frame["generation"] != self.generation
                        or type(frame["sequence"]) is not int or frame["sequence"] != self.sequence + 1):
                    raise BrowserActionError("browser frame identity or sequence mismatch")
                if frame["action"] == {"type": "close"}:
                    self.sequence = frame["sequence"]
                    await self._cleanup()
                    return {"session_id": self.session_id, "generation": self.generation,
                            "sequence": self.sequence, "status": "closed"}
                validate_action(frame["action"], self.width, self.height)
                # Consume the sequence before any browser effect; an unknown
                # outcome terminates this session rather than replaying a write.
                self.sequence = frame["sequence"]
                self.active = asyncio.create_task(execute_action(self.page, frame["action"], self.width, self.height))
                result = await asyncio.wait_for(self.active, timeout=30)
                if self.closed:
                    raise BrowserActionError("browser session was stopped during action")
                return {"session_id": self.session_id, "generation": self.generation,
                        "sequence": self.sequence, "status": "completed", **result}
            except BaseException as exc:
                try:
                    await self._cleanup()
                except BaseException:
                    exc.add_note("Browser session cleanup unconfirmed")
                raise
            finally:
                self.active = None


KEY_NAMES = {
    "CTRL": "Control", "CONTROL": "Control", "ALT": "Alt", "SHIFT": "Shift",
    "META": "Meta", "CMD": "Meta", "COMMAND": "Meta", "SUPER": "Meta",
    "ENTER": "Enter", "RETURN": "Enter", "TAB": "Tab", "ESC": "Escape", "ESCAPE": "Escape",
    "BACKSPACE": "Backspace", "DELETE": "Delete", "DEL": "Delete", "SPACE": "Space",
    "ARROWUP": "ArrowUp", "UP": "ArrowUp", "ARROWDOWN": "ArrowDown", "DOWN": "ArrowDown",
    "ARROWLEFT": "ArrowLeft", "LEFT": "ArrowLeft", "ARROWRIGHT": "ArrowRight", "RIGHT": "ArrowRight",
    "HOME": "Home", "END": "End", "PAGEUP": "PageUp", "PAGEDOWN": "PageDown",
}


def normalized_keys(value):
    if not isinstance(value, list) or len(value) > 16:
        raise BrowserActionError("invalid key combination")
    result = []
    for key in value:
        if not isinstance(key, str) or not key or len(key) > 64 or not key.isprintable():
            raise BrowserActionError("invalid key")
        resolved = KEY_NAMES.get(key.upper())
        if resolved is None:
            if len(key) == 1 and key != "+":
                resolved = key
            elif key.upper() in {f"F{n}" for n in range(1, 13)}:
                resolved = key.upper()
            else:
                raise BrowserActionError("unsupported key")
        if resolved in result:
            raise BrowserActionError("duplicate key")
        result.append(resolved)
    return result


def validate_action(action, width, height):
    if any(type(value) is not int or not 1 <= value <= 4096 for value in (width, height)):
        raise BrowserActionError("invalid viewport")
    if not isinstance(action, dict):
        raise BrowserActionError("invalid action")
    kind = action.get("type")
    required = {
        "screenshot": set(), "wait": set(), "type": {"text"}, "keypress": {"keys"},
        "click": {"x", "y", "button"}, "double_click": {"x", "y"}, "move": {"x", "y"},
        "scroll": {"x", "y", "scroll_x", "scroll_y"}, "drag": {"path"},
    }
    if not isinstance(kind, str) or kind not in required:
        raise BrowserActionError("unsupported action")
    optional = {"keys"} if kind in {"click", "double_click", "move", "scroll", "drag"} else set()
    if not required[kind].issubset(action) or set(action) - {"type"} - required[kind] - optional:
        raise BrowserActionError("invalid action fields")

    def point(x, y):
        if type(x) is not int or type(y) is not int or not 0 <= x < width or not 0 <= y < height:
            raise BrowserActionError("coordinates outside viewport")

    if "x" in action:
        point(action["x"], action["y"])
    if kind == "click" and action["button"] not in {"left", "right", "wheel", "back", "forward"}:
        raise BrowserActionError("invalid mouse button")
    if kind == "scroll" and any(type(action[key]) is not int or abs(action[key]) > 100000 for key in ("scroll_x", "scroll_y")):
        raise BrowserActionError("invalid scroll distance")
    if kind == "type" and (not isinstance(action["text"], str) or len(action["text"].encode("utf-8")) > 65536):
        raise BrowserActionError("invalid text size")
    if kind == "drag":
        if not isinstance(action["path"], list) or not 2 <= len(action["path"]) <= 512:
            raise BrowserActionError("invalid drag path")
        for item in action["path"]:
            if not isinstance(item, (list, tuple)) or len(item) != 2:
                raise BrowserActionError("invalid drag point")
            point(*item)
    keys = normalized_keys(action.get("keys", []))
    if kind != "keypress" and any(key not in {"Control", "Alt", "Shift", "Meta"} for key in keys):
        raise BrowserActionError("mouse actions accept modifier keys only")
    if kind == "click" and action["button"] in {"back", "forward"} and keys:
        raise BrowserActionError("history navigation does not accept modifiers")
    return kind, keys


async def execute_action(page, action, width, height):
    kind, keys = validate_action(action, width, height)
    if page.viewport_size != {"width": width, "height": height} or page.is_closed():
        raise BrowserActionError("browser viewport or session changed")
    held = []
    mouse_down = False
    try:
        for key in keys:
            held.append(key)
            await page.keyboard.down(key)
        if kind == "click":
            button = action["button"]
            if button in {"back", "forward"}:
                await (page.go_back() if button == "back" else page.go_forward())
            else:
                await page.mouse.click(action["x"], action["y"], button="middle" if button == "wheel" else button)
        elif kind == "double_click":
            await page.mouse.dblclick(action["x"], action["y"])
        elif kind == "move":
            await page.mouse.move(action["x"], action["y"])
        elif kind == "scroll":
            await page.mouse.move(action["x"], action["y"])
            await page.mouse.wheel(action["scroll_x"], action["scroll_y"])
        elif kind == "type":
            await page.keyboard.insert_text(action["text"])
        elif kind == "wait":
            await page.wait_for_timeout(250)
        elif kind == "drag":
            await page.mouse.move(*action["path"][0])
            mouse_down = True
            await page.mouse.down(button="left")
            for point in action["path"][1:]:
                await page.mouse.move(*point)
        elif kind == "screenshot":
            png = await page.screenshot(type="png", full_page=False, animations="disabled")
            if len(png) > 16 * 1024 * 1024:
                raise BrowserActionError("screenshot too large")
            return {"png_base64": base64.b64encode(png).decode("ascii")}
        return {}
    finally:
        primary = sys.exc_info()[1]
        cleanup_errors = []
        try:
            if mouse_down:
                await page.mouse.up(button="left")
        except BaseException as exc:
            cleanup_errors.append(exc)
        for key in reversed(held):
            try:
                await page.keyboard.up(key)
            except BaseException as exc:
                cleanup_errors.append(exc)
        if cleanup_errors:
            if primary is not None:
                primary.add_note("Browser input release unconfirmed; session must be closed")
            else:
                raise BaseExceptionGroup("Browser input release failed", cleanup_errors)


async def serve_browser(read_frame, write_frame, playwright):
    session = None
    success = False
    try:
        startup = parse_frame(await read_frame())
        if (set(startup) != {"operation", "sequence", "session_id", "generation", "width", "height", "initial_url", "proxy"}
                or startup["operation"] != "start" or type(startup["sequence"]) is not int or startup["sequence"] != 0):
            raise BrowserActionError("invalid browser startup frame")
        session = await open_browser_session(playwright, startup["session_id"], startup["generation"],
                                             startup["width"], startup["height"], startup["initial_url"], startup["proxy"])
        await write_frame({"session_id": session.session_id, "generation": session.generation,
                           "sequence": 0, "status": "ready", "width": session.width, "height": session.height})
        while not session.closed:
            raw = await read_frame()
            if raw == b"":
                break
            await write_frame(await session.handle(raw))
        success = True
    except asyncio.CancelledError:
        raise
    except Exception:
        try:
            await write_frame({"status": "failed", "error_code": "BROWSER_SESSION_FAILED"})
        except Exception:
            pass
    finally:
        if session is not None:
            try:
                await session.close()
            except Exception:
                success = False
    return success


async def _stdio_main():
    from playwright.async_api import async_playwright

    async def read_frame():
        return await asyncio.to_thread(sys.stdin.buffer.readline, (256 << 10) + 1)

    async def write_frame(value):
        raw = (json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8")
        if len(raw) > (24 << 20) + (64 << 10):
            raise BrowserActionError("browser response frame exceeds limit")
        sys.stdout.buffer.write(raw)
        sys.stdout.buffer.flush()

    async with async_playwright() as playwright:
        return await serve_browser(read_frame, write_frame, playwright)


if __name__ == "__main__":
    try:
        exit_code = 0 if asyncio.run(_stdio_main()) else 1
    except BaseException:
        exit_code = 1
    sys.exit(exit_code)
