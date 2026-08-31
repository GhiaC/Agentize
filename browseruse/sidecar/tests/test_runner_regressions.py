from __future__ import annotations

import asyncio
import sys
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
sys.modules.setdefault("browser_use", MagicMock())

from app.models import BrowserTab  # noqa: E402
from app.runner import (  # noqa: E402
	BrowserDisconnected,
	BrowserTabUnavailable,
	BrowserUseRunner,
	compact_navigation_history,
)


JUP = "https://www.coinglass.com/tv/Binance_JUPUSDT"
EXAMPLE = "https://example.com"


class CompactNavigationHistoryTest(unittest.TestCase):
	def test_after_back_keeps_forward_stack(self) -> None:
		entries = [
			{"url": "about:blank"},
			{"url": JUP},
			{"url": EXAMPLE},
		]
		urls, index = compact_navigation_history(entries, 1)
		self.assertEqual(urls, [JUP, EXAMPLE])
		self.assertEqual(index, 0)

	def test_current_blank_does_not_snap_to_last_url(self) -> None:
		entries = [
			{"url": "about:blank"},
			{"url": JUP},
			{"url": EXAMPLE},
		]
		urls, index = compact_navigation_history(entries, 0)
		self.assertEqual(urls, [JUP, EXAMPLE])
		self.assertEqual(index, 0)
		self.assertNotEqual(index, len(urls) - 1)

	def test_current_last_page_disables_forward(self) -> None:
		entries = [
			{"url": "about:blank"},
			{"url": JUP},
			{"url": EXAMPLE},
		]
		urls, index = compact_navigation_history(entries, 2)
		self.assertEqual(urls, [JUP, EXAMPLE])
		self.assertEqual(index, 1)


class PersistedTabsStateTest(unittest.TestCase):
	def test_persisted_tabs_round_trip_keeps_session_id(self) -> None:
		runner = object.__new__(BrowserUseRunner)
		with TemporaryDirectory() as directory:
			runner.settings = SimpleNamespace(data_dir=Path(directory))
			runner._persist_tabs_state("user:owner-1", [BrowserTab(id="tab-1", url=EXAMPLE, title="Example", active=True)])
			tabs = runner.persisted_tabs("user:owner-1")
			self.assertEqual(tabs[0].url, EXAMPLE)
			listed = runner.list_persisted_tab_sessions()
			self.assertIn("user:owner-1", listed)
			self.assertEqual(listed["user:owner-1"][0].id, "tab-1")


class BrowserCrashRecoveryTest(unittest.IsolatedAsyncioTestCase):
	async def test_await_cdp_maps_connection_error(self) -> None:
		runner = object.__new__(BrowserUseRunner)

		async def boom():
			raise ConnectionError("cdp closed")

		with self.assertRaises(BrowserDisconnected):
			await runner._await_cdp(boom(), "Target.getTargets")

	async def test_await_cdp_timeout_is_not_a_disconnect(self) -> None:
		runner = object.__new__(BrowserUseRunner)

		async def hang():
			await asyncio.Event().wait()

		with patch("app.runner.TAB_CDP_TIMEOUT_SECONDS", 0.05):
			with self.assertRaises(BrowserTabUnavailable) as caught:
				await runner._await_cdp(hang(), "Page.captureScreenshot")
		self.assertNotIsInstance(caught.exception, BrowserDisconnected)
		self.assertIn("timed out", str(caught.exception))

	async def test_dead_session_is_killed_and_dropped(self) -> None:
		runner = object.__new__(BrowserUseRunner)
		dead = SimpleNamespace(
			start=AsyncMock(side_effect=ConnectionError("cdp closed")),
			get_tabs=AsyncMock(),
			kill=AsyncMock(),
		)
		runner._sessions = {"session-1": SimpleNamespace(browser=dead)}
		self.assertIsNone(await runner._ensure_live_browser("session-1", create=False))
		self.assertNotIn("session-1", runner._sessions)
		dead.kill.assert_awaited()

	async def test_screenshot_timeout_does_not_recycle(self) -> None:
		runner = object.__new__(BrowserUseRunner)
		browser = SimpleNamespace(
			take_screenshot=None,
			agent_focus_target_id=None,
		)

		async def hang(**_kwargs):
			await asyncio.Event().wait()
			return b"PNG"

		browser.take_screenshot = hang
		runner._sessions = {"session-1": SimpleNamespace(browser=browser)}
		runner._ensure_live_browser = AsyncMock(return_value=browser)
		runner._snapshot_tabs = AsyncMock(return_value=[BrowserTab(id="tab-1", url=EXAMPLE)])
		runner._tab_cdp_session = AsyncMock()
		runner._recycle_session = AsyncMock()
		with patch("app.runner.TAB_CDP_TIMEOUT_SECONDS", 0.05):
			with self.assertRaises(BrowserTabUnavailable) as caught:
				await runner.tab_screenshot("session-1", "tab-1")
		self.assertIn("timed out", str(caught.exception))
		runner._recycle_session.assert_not_awaited()
		self.assertIn("session-1", runner._sessions)

	async def test_screenshot_disconnect_recycles_session(self) -> None:
		runner = object.__new__(BrowserUseRunner)
		browser = SimpleNamespace(
			take_screenshot=AsyncMock(side_effect=ConnectionError("cdp closed")),
			agent_focus_target_id=None,
		)
		runner._sessions = {"session-1": SimpleNamespace(browser=browser)}
		runner._ensure_live_browser = AsyncMock(return_value=browser)
		runner._snapshot_tabs = AsyncMock(return_value=[BrowserTab(id="tab-1", url=EXAMPLE)])
		runner._tab_cdp_session = AsyncMock()
		runner._recycle_session = AsyncMock()
		with self.assertRaises(BrowserTabUnavailable) as caught:
			await runner.tab_screenshot("session-1", "tab-1")
		self.assertIn("crashed", str(caught.exception))
		runner._recycle_session.assert_awaited_once_with("session-1")


if __name__ == "__main__":
	unittest.main()
