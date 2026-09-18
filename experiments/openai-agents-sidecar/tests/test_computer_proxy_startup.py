import asyncio
import json
from unittest.mock import AsyncMock

import pytest

from test_computer_browser_executor import browser, provisioning


def proxy_config(**changes):
    return {"host": "172.30.0.1", "port": 3128, "token": "b" * 64, **changes}


@pytest.mark.parametrize("proxy", [
    None, {}, proxy_config(host="proxy.test"), proxy_config(host="0.0.0.0"),
    proxy_config(host="224.0.0.1"), proxy_config(host="fe80::1%eth0"),
    proxy_config(host=True), proxy_config(port=True), proxy_config(port=0),
    proxy_config(port=65536), proxy_config(token="short"), proxy_config(token="z" * 64),
    proxy_config(bypass="*"),
])
def test_invalid_proxy_never_launches_browser(proxy):
    engine, *_ = provisioning()
    with pytest.raises(browser.BrowserActionError, match="invalid browser proxy configuration"):
        asyncio.run(browser.open_browser_session(engine, "s", 1, 100, 100, "https://example.test/", proxy))
    engine.chromium.launch.assert_not_awaited()


@pytest.mark.parametrize("host,server", [
    ("172.30.0.1", "http://172.30.0.1:3128"),
    ("fd00::1", "http://[fd00::1]:3128"),
])
def test_proxy_auth_is_separate_from_url_and_session_is_owned(host, server):
    async def run():
        engine, owned, context, _ = provisioning()
        session = await browser.open_browser_session(engine, "s", 1, 100, 100,
                                                     "https://example.test/", proxy_config(host=host))
        engine.chromium.launch.assert_awaited_once_with(headless=True, chromium_sandbox=True,
            proxy={"server": server, "username": "computer", "password": "b" * 64})
        await session.close()
        context.close.assert_awaited_once()
        owned.close.assert_awaited_once()

    asyncio.run(run())


def test_missing_proxy_startup_fails_without_launch_or_secret_receipt():
    async def run():
        engine, *_ = provisioning()
        startup = {"operation": "start", "sequence": 0, "session_id": "s", "generation": 1,
                   "width": 100, "height": 100, "initial_url": "https://example.test/"}
        read = AsyncMock(return_value=(json.dumps(startup) + "\n").encode())
        write = AsyncMock()
        assert not await browser.serve_browser(read, write, engine)
        engine.chromium.launch.assert_not_awaited()
        write.assert_awaited_once_with({"status": "failed", "error_code": "BROWSER_SESSION_FAILED"})

    asyncio.run(run())


def test_proxy_launch_failure_is_redacted_in_service_output():
    async def run():
        engine, *_ = provisioning()
        engine.chromium.launch.side_effect = RuntimeError("sensitive-proxy-token:" + "b" * 64)
        startup = {"operation": "start", "sequence": 0, "session_id": "s", "generation": 1,
                   "width": 100, "height": 100, "initial_url": "https://example.test/", "proxy": proxy_config()}
        read = AsyncMock(return_value=(json.dumps(startup) + "\n").encode())
        write = AsyncMock()
        assert not await browser.serve_browser(read, write, engine)
        write.assert_awaited_once_with({"status": "failed", "error_code": "BROWSER_SESSION_FAILED"})

    asyncio.run(run())
