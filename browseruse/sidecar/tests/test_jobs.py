from __future__ import annotations

import asyncio
import json
import sys
import types
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

sys.modules.setdefault("browser_use", MagicMock())
try:
	from fastapi import HTTPException
except ImportError:
	class HTTPException(Exception):
		def __init__(self, status_code=500, detail="", headers=None, **_kwargs):
			super().__init__(str(detail))
			self.status_code = status_code
			self.detail = detail
			self.headers = headers or {}

	class _Status:
		HTTP_400_BAD_REQUEST = 400
		HTTP_401_UNAUTHORIZED = 401
		HTTP_404_NOT_FOUND = 404
		HTTP_413_REQUEST_ENTITY_TOO_LARGE = 413
		HTTP_501_NOT_IMPLEMENTED = 501
		HTTP_503_SERVICE_UNAVAILABLE = 503

	_fastapi = types.ModuleType("fastapi")
	_fastapi.HTTPException = HTTPException
	_fastapi.status = _Status()
	sys.modules["fastapi"] = _fastapi

from app.artifacts import BrowserArtifacts
from app.config import Settings
from app.jobs import JobManager
from app.models import BrowserDownload, BrowserTab, BrowserTabActionRequest, JobResult, JobStatus, StartJobRequest
from app.runner import BrowserDisconnected, BrowserTabUnavailable, BrowserUseRunner


def settings() -> Settings:
	return Settings(
		service_token="test-token",
		data_dir=Path("/tmp/browser-use-tests"),
		executable_path="/usr/bin/chromium",
		llm_provider="openai",
		llm_model="test-model",
		llm_base_url=None,
		max_concurrent_jobs=2,
		max_steps=20,
		job_timeout_seconds=30,
		job_ttl_seconds=60,
		max_jobs=20,
		db_max_jobs=100,
		db_max_logs_per_job=100,
		db_job_retention_seconds=3600,
		headless=True,
		chromium_sandbox=False,
		block_ip_addresses=True,
		default_use_vision=False,
		viewport_width=1920,
		viewport_height=1080,
		allowed_domains=(),
		prohibited_domains=(),
		proxy_url=None,
	)


def result() -> JobResult:
	return JobResult(
		final_result="done",
		done=True,
		successful=True,
		steps=1,
		duration_seconds=0.01,
	)


class CompletingRunner:
	async def run(self, _session_id: str, _job_id: str, _request: StartJobRequest) -> JobResult:
		await asyncio.sleep(0)
		return result()

	def screenshot_available(self, _job_id: str) -> bool:
		return True

	def read_screenshot(self, _job_id: str) -> bytes:
		return b"PNG"

	def network_loads(self, _job_id: str, _limit: int):
		return 1, []

	def list_downloads(self, _job_id: str):
		return [BrowserDownload(name="report.csv", mime_type="text/csv", size=5)]

	def read_download(self, _job_id: str, name: str):
		if name != "report.csv":
			raise FileNotFoundError
		return BrowserDownload(name=name, mime_type="text/csv", size=5), b"a,b\n1"


class TabRunner(CompletingRunner):
	def __init__(self):
		self.current = [BrowserTab(id="tab-1", url="https://example.com", active=True)]
		self.tabs_session = ""
		self.closed = ""
		self.opened = ""

	async def tabs(self, session_id: str):
		self.tabs_session = session_id
		return self.current

	async def close_tab(self, session_id: str, tab_id: str):
		self.tabs_session = session_id
		self.closed = tab_id
		if not any(tab.id == tab_id for tab in self.current):
			raise KeyError(tab_id)
		self.current = []
		return self.current

	async def open_tab(self, session_id: str, url: str):
		self.tabs_session = session_id
		self.opened = url
		self.current = [BrowserTab(id="tab-2", url=url, active=True)]
		return self.current

	async def tab_screenshot(self, session_id: str, tab_id: str):
		self.tabs_session = session_id
		if not any(tab.id == tab_id for tab in self.current):
			raise KeyError(tab_id)
		return b"TAB-PNG"

	def list_sessions(self):
		if self.tabs_session:
			return [self.tabs_session]
		return []

	async def kill_session(self, session_id: str):
		if self.tabs_session == session_id:
			self.tabs_session = ""
			self.current = []


class BlockingRunner:
	def __init__(self):
		self.started = asyncio.Event()

	async def run(self, _session_id: str, _job_id: str, _request: StartJobRequest) -> JobResult:
		self.started.set()
		await asyncio.Event().wait()
		return result()


class TabCDPClient:
	def __init__(self):
		self.navigate = AsyncMock(return_value={})
		self.evaluate = AsyncMock(return_value={"result": {"value": "evaluated"}})
		self.get_navigation_history = AsyncMock(
			return_value={"entries": [{"id": 1, "url": "https://example.com"}], "currentIndex": 0}
		)
		self.send = SimpleNamespace(
			Page=SimpleNamespace(navigate=self.navigate, getNavigationHistory=self.get_navigation_history),
			Runtime=SimpleNamespace(evaluate=self.evaluate),
		)


class TabBrowser:
	def __init__(self):
		self.cdp_client = TabCDPClient()
		self.cdp_session = SimpleNamespace(cdp_client=self.cdp_client, session_id="cdp-session-1")
		self.get_or_create_cdp_session = AsyncMock(return_value=self.cdp_session)
		self.start = AsyncMock()
		self.take_screenshot = AsyncMock(return_value=b"TAB-PNG")


class BrowserUseRunnerTabTests(unittest.IsolatedAsyncioTestCase):
	async def test_navigation_uses_the_tab_cdp_session(self):
		runner = object.__new__(BrowserUseRunner)
		browser = TabBrowser()
		runner.settings = SimpleNamespace(
			prohibited_domains=(),
			allowed_domains=(),
			block_ip_addresses=False,
			data_dir=Path(self.enterContext(TemporaryDirectory())),
		)
		runner._owned_direct_tab = AsyncMock(return_value=browser)
		runner._tab_cdp_session = AsyncMock(return_value=browser.cdp_session)
		runner._tab_by_id = AsyncMock(return_value=BrowserTab(id="tab-1", url="https://example.com"))
		runner._snapshot_tabs = AsyncMock(return_value=[])
		response = await runner.act_on_tab("session-1", "tab-1", BrowserTabActionRequest(action="navigate", url="https://example.com"))
		self.assertEqual(response.result, "navigation started")
		browser.cdp_client.navigate.assert_awaited_once_with(params={"url": "https://example.com"}, session_id="cdp-session-1")

	async def test_page_evaluation_uses_the_tab_cdp_session(self):
		runner = object.__new__(BrowserUseRunner)
		browser = TabBrowser()
		runner._tab_cdp_session = AsyncMock(return_value=browser.cdp_session)
		self.assertEqual(await runner._evaluate_tab(browser, "tab-1", "'ok'"), "evaluated")
		browser.cdp_client.evaluate.assert_awaited_once_with(
			params={"expression": "'ok'", "returnByValue": True, "awaitPromise": True},
			session_id="cdp-session-1",
		)

	async def test_tab_screenshot_is_limited_to_the_visible_viewport(self):
		runner = object.__new__(BrowserUseRunner)
		browser = TabBrowser()
		runner._sessions = {"session-1": SimpleNamespace(browser=browser)}
		runner._ensure_live_browser = AsyncMock(return_value=browser)
		runner._snapshot_tabs = AsyncMock(return_value=[BrowserTab(id="tab-1", url="https://example.com")])
		runner._tab_cdp_session = AsyncMock(return_value=browser.cdp_session)
		self.assertEqual(await runner.tab_screenshot("session-1", "tab-1"), b"TAB-PNG")
		browser.take_screenshot.assert_awaited_once_with(full_page=False, format="png")

	async def test_tab_screenshot_times_out_when_cdp_hangs(self):
		runner = object.__new__(BrowserUseRunner)
		browser = TabBrowser()

		async def hang(**_kwargs):
			await asyncio.Event().wait()
			return b"PNG"

		browser.take_screenshot = hang
		runner._sessions = {"session-1": SimpleNamespace(browser=browser)}
		runner._ensure_live_browser = AsyncMock(return_value=browser)
		runner._snapshot_tabs = AsyncMock(return_value=[BrowserTab(id="tab-1", url="https://example.com")])
		runner._tab_cdp_session = AsyncMock(return_value=browser.cdp_session)
		runner._recycle_session = AsyncMock()
		with patch("app.runner.TAB_CDP_TIMEOUT_SECONDS", 0.05):
			with self.assertRaises(BrowserTabUnavailable) as caught:
				await runner.tab_screenshot("session-1", "tab-1")
		self.assertIn("timed out", str(caught.exception))
		runner._recycle_session.assert_not_awaited()
		self.assertIn("session-1", runner._sessions)

	async def test_cdp_connection_error_is_browser_disconnected(self):
		runner = object.__new__(BrowserUseRunner)

		async def boom():
			raise ConnectionError("cdp closed")

		with self.assertRaises(BrowserDisconnected):
			await runner._await_cdp(boom(), "Target.getTargets")

	async def test_dead_session_is_recycled_on_liveness_ping(self):
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

	async def test_tab_screenshot_recycles_after_chromium_disconnect(self):
		runner = object.__new__(BrowserUseRunner)
		browser = TabBrowser()
		browser.take_screenshot = AsyncMock(side_effect=ConnectionError("cdp closed"))
		runner._sessions = {"session-1": SimpleNamespace(browser=browser)}
		runner._ensure_live_browser = AsyncMock(return_value=browser)
		runner._snapshot_tabs = AsyncMock(return_value=[BrowserTab(id="tab-1", url="https://example.com")])
		runner._tab_cdp_session = AsyncMock(return_value=browser.cdp_session)
		runner._recycle_session = AsyncMock()
		with self.assertRaises(BrowserTabUnavailable) as caught:
			await runner.tab_screenshot("session-1", "tab-1")
		self.assertIn("crashed", str(caught.exception))
		runner._recycle_session.assert_awaited_once_with("session-1")

	async def test_unavailable_tab_is_reported_as_service_unavailable(self):
		runner = TabRunner()
		runner.act_on_tab = AsyncMock(side_effect=BrowserTabUnavailable("browser tab is temporarily unavailable"))
		manager = JobManager(settings(), runner)
		with self.assertRaises(HTTPException) as caught:
			await manager.act_on_tab("session-1", "tab-1", BrowserTabActionRequest(action="wait", amount=1))
		self.assertEqual(caught.exception.status_code, 503)
		await manager.shutdown()


class JobManagerTests(unittest.IsolatedAsyncioTestCase):
	async def test_running_job_makes_tab_actions_fail_fast_but_job_screenshot_remains_available(self):
		class BlockingTabRunner(TabRunner):
			def __init__(self):
				super().__init__()
				self.started = asyncio.Event()

			async def run(self, _session_id: str, _job_id: str, _request: StartJobRequest) -> JobResult:
				self.started.set()
				await asyncio.Event().wait()
				return result()

		runner = BlockingTabRunner()
		manager = JobManager(settings(), runner)
		created = await manager.create("session-1", StartJobRequest(task="hold browser session"))
		await asyncio.wait_for(runner.started.wait(), timeout=1)
		self.assertEqual(await manager.screenshot("session-1", created.id), b"PNG")
		started = asyncio.get_running_loop().time()
		with patch("app.jobs.TAB_LOCK_TIMEOUT_SECONDS", 0.05):
			with self.assertRaises(HTTPException) as caught:
				await manager.open_tab("session-1", "https://openai.com")
		self.assertEqual(caught.exception.status_code, 503)
		self.assertEqual(caught.exception.headers.get("X-Browser-Session-Busy"), "1")
		self.assertIn(created.id, str(caught.exception.detail))
		self.assertLess(asyncio.get_running_loop().time() - started, 0.5)
		await manager.cancel("session-1", created.id)
		await manager.shutdown()

	async def test_hung_tab_operation_has_a_bounded_timeout(self):
		class HangingOpenRunner(TabRunner):
			async def open_tab(self, _session_id: str, _url: str):
				await asyncio.Event().wait()
				return []

		manager = JobManager(settings(), HangingOpenRunner())
		with patch("app.jobs.TAB_OP_TIMEOUT_SECONDS", 0.05):
			with self.assertRaises(HTTPException) as caught:
				await manager.open_tab("session-1", "https://openai.com")
		self.assertEqual(caught.exception.status_code, 503)
		self.assertIn("timed out", str(caught.exception.detail))
		await manager.shutdown()

	async def test_wait_follows_running_job_until_terminal(self):
		manager = JobManager(settings(), CompletingRunner())
		created = await manager.create("session-1", StartJobRequest(task="test"))
		completed = await manager.get("session-1", created.id, wait_seconds=2)
		self.assertEqual(completed.status, JobStatus.SUCCEEDED)
		self.assertEqual(completed.result, result())
		await manager.shutdown()

	async def test_job_is_hidden_from_other_sessions(self):
		manager = JobManager(settings(), CompletingRunner())
		created = await manager.create("session-1", StartJobRequest(task="test"))
		with self.assertRaises(HTTPException) as caught:
			await manager.get("session-2", created.id)
		self.assertEqual(caught.exception.status_code, 404)
		await manager.shutdown()

	async def test_screenshot_is_scoped_to_job_owner(self):
		manager = JobManager(settings(), CompletingRunner())
		created = await manager.create("session-1", StartJobRequest(task="test"))
		await manager.get("session-1", created.id, wait_seconds=2)
		self.assertEqual(await manager.screenshot("session-1", created.id), b"PNG")
		with self.assertRaises(HTTPException) as caught:
			await manager.screenshot("session-2", created.id)
		self.assertEqual(caught.exception.status_code, 404)
		await manager.shutdown()

	async def test_downloads_are_scoped_to_job_owner(self):
		manager = JobManager(settings(), CompletingRunner())
		created = await manager.create("session-1", StartJobRequest(task="download a report"))
		await manager.get("session-1", created.id, wait_seconds=2)
		files = await manager.downloads("session-1", created.id)
		self.assertEqual(files[0].name, "report.csv")
		download, data = await manager.download("session-1", created.id, "report.csv")
		self.assertEqual(download.mime_type, "text/csv")
		self.assertEqual(data, b"a,b\n1")
		with self.assertRaises(HTTPException) as caught:
			await manager.downloads("session-2", created.id)
		self.assertEqual(caught.exception.status_code, 404)
		await manager.shutdown()

	async def test_debug_snapshot_includes_recent_jobs_and_load_counts(self):
		manager = JobManager(settings(), CompletingRunner())
		created = await manager.create("session-1", StartJobRequest(task="inspect example.com"))
		await manager.get("session-1", created.id, wait_seconds=2)
		snapshot = await manager.debug(job_limit=10, load_limit=50)
		self.assertEqual(snapshot.total_jobs, 1)
		self.assertEqual(snapshot.jobs[0].session_id, "session-1")
		self.assertEqual(snapshot.jobs[0].load_count, 1)
		self.assertTrue(snapshot.jobs[0].screenshot_available)
		logs = await manager.job_logs(created.id, 20)
		self.assertGreaterEqual(len(logs.logs), 2)
		await manager.shutdown()

	async def test_debug_snapshot_includes_persisted_tabs_when_chromium_is_gone(self):
		class PersistedOnlyRunner(CompletingRunner):
			def list_sessions(self):
				return []

			def list_persisted_tab_sessions(self):
				return {
					"user:owner-1": [BrowserTab(id="tab-1", url="https://example.com", title="Example", active=True)],
				}

		manager = JobManager(settings(), PersistedOnlyRunner())
		snapshot = await manager.debug(job_limit=10, load_limit=0, session_limit=50)
		self.assertEqual(snapshot.live_sessions, 0)
		self.assertEqual(snapshot.total_tabs, 1)
		session = next(item for item in snapshot.sessions if item.session_id == "user:owner-1")
		self.assertFalse(session.persistent)
		self.assertEqual(session.tabs[0].url, "https://example.com")
		await manager.shutdown()

	async def test_debug_snapshot_includes_tabs_opened_by_host_client(self):
		runner = TabRunner()
		manager = JobManager(settings(), runner)
		await manager.open_tab("user:owner-1", "https://example.com")
		snapshot = await manager.debug(job_limit=10, load_limit=0, session_limit=50)
		self.assertEqual(snapshot.live_sessions, 1)
		self.assertEqual(snapshot.total_tabs, 1)
		session = next(item for item in snapshot.sessions if item.session_id == "user:owner-1")
		self.assertTrue(session.persistent)
		self.assertEqual(session.tabs[0].url, "https://example.com")
		await manager.shutdown()

	async def test_debug_snapshot_does_not_block_on_hung_live_tabs(self):
		class HangingTabsRunner(TabRunner):
			async def tabs(self, session_id: str):
				self.tabs_session = session_id
				await asyncio.Event().wait()
				return self.current

		runner = HangingTabsRunner()
		runner.tabs_session = "session-1"
		manager = JobManager(settings(), runner)
		started = asyncio.get_running_loop().time()
		snapshot = await asyncio.wait_for(manager.debug(job_limit=10, load_limit=0, session_limit=50), timeout=3)
		self.assertLess(asyncio.get_running_loop().time() - started, 2.9)
		self.assertTrue(any(item.session_id == "session-1" for item in snapshot.sessions))
		await manager.shutdown()

	async def test_admin_cancel_and_kill_session(self):
		runner = TabRunner()
		blocking = BlockingRunner()
		manager = JobManager(settings(), blocking)
		created = await manager.create("session-1", StartJobRequest(task="hold"))
		await asyncio.wait_for(blocking.started.wait(), timeout=1)
		cancelled = await manager.admin_cancel(created.id)
		self.assertEqual(cancelled.status, JobStatus.CANCELLED)
		await manager.shutdown()

		manager = JobManager(settings(), runner)
		await manager.open_tab("session-1", "https://example.com")
		killed = await manager.kill_session("session-1")
		self.assertFalse(killed.persistent)
		await manager.shutdown()

	async def test_immediate_cancel_transitions_queued_job(self):
		manager = JobManager(settings(), BlockingRunner())
		created = await manager.create("session-1", StartJobRequest(task="test"))
		cancelled = await manager.cancel("session-1", created.id)
		self.assertEqual(cancelled.status, JobStatus.CANCELLED)
		await manager.shutdown()

	async def test_cancel_running_job(self):
		runner = BlockingRunner()
		manager = JobManager(settings(), runner)
		created = await manager.create("session-1", StartJobRequest(task="test"))
		await asyncio.wait_for(runner.started.wait(), timeout=1)
		cancelled = await manager.cancel("session-1", created.id)
		self.assertEqual(cancelled.status, JobStatus.CANCELLED)
		await manager.shutdown()

	async def test_tabs_are_scoped_and_close_returns_new_snapshot(self):
		runner = TabRunner()
		manager = JobManager(settings(), runner)
		tabs = await manager.tabs("session-1")
		self.assertEqual(tabs[0].id, "tab-1")
		self.assertEqual(runner.tabs_session, "session-1")
		remaining = await manager.close_tab("session-1", "tab-1")
		self.assertEqual(remaining, [])
		self.assertEqual(runner.closed, "tab-1")
		with self.assertRaises(HTTPException) as caught:
			await manager.close_tab("session-1", "missing")
		self.assertEqual(caught.exception.status_code, 404)
		await manager.shutdown()

	async def test_direct_tab_open_and_screenshot_share_session_lock(self):
		runner = TabRunner()
		manager = JobManager(settings(), runner)
		opened = await manager.open_tab("session-1", "https://openai.com")
		self.assertEqual(opened[0].id, "tab-2")
		self.assertEqual(runner.opened, "https://openai.com")
		self.assertEqual(await manager.tab_screenshot("session-1", "tab-2"), b"TAB-PNG")
		with self.assertRaises(HTTPException) as caught:
			await manager.tab_screenshot("session-1", "missing")
		self.assertEqual(caught.exception.status_code, 404)
		await manager.shutdown()


class BrowserArtifactsTests(unittest.TestCase):
	def test_har_is_projected_to_bounded_non_sensitive_load_metadata(self):
		with TemporaryDirectory() as directory:
			artifacts = BrowserArtifacts(Path(directory))
			har_path, _ = artifacts.prepare("job-1")
			har_path.write_text(
				json.dumps(
					{
						"log": {
							"entries": [
								{
									"startedDateTime": "2026-07-30T10:00:00Z",
									"time": 12.5,
									"request": {
										"method": "GET",
										"url": "https://example.com/app.js",
										"headers": [{"name": "Authorization", "value": "secret"}],
									},
									"response": {
										"status": 200,
										"statusText": "OK",
										"bodySize": 42,
										"content": {"mimeType": "text/javascript", "text": "private body"},
									},
								}
							]
						}
					}
				),
				encoding="utf-8",
			)

			count, loads = artifacts.network_loads("job-1", 10)
			self.assertEqual(count, 1)
			self.assertEqual(loads[0].url, "https://example.com/app.js")
			self.assertEqual(loads[0].bytes, 42)
			self.assertNotIn("secret", loads[0].model_dump_json())
			self.assertNotIn("private body", loads[0].model_dump_json())
			artifacts.sanitize_har("job-1")
			sanitized = har_path.read_text(encoding="utf-8")
			self.assertNotIn("Authorization", sanitized)
			self.assertNotIn("secret", sanitized)
			self.assertNotIn("private body", sanitized)

	def test_screenshot_round_trip_is_atomic_and_bounded(self):
		with TemporaryDirectory() as directory:
			artifacts = BrowserArtifacts(Path(directory))
			artifacts.save_screenshot("job-1", b"PNG")
			self.assertTrue(artifacts.screenshot_available("job-1"))
			self.assertEqual(artifacts.read_screenshot("job-1"), b"PNG")
