from __future__ import annotations

import asyncio
import base64
import hashlib
import ipaddress
import json
import logging
import mimetypes
import os
import shutil
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

from browser_use import Agent, BrowserProfile, BrowserSession

from .artifacts import BrowserArtifacts
from .config import Settings
from .models import BrowserDownload, BrowserTab, BrowserTabActionRequest, BrowserTabActionResponse, BrowserTabElement, BrowserTabHistoryResponse, BrowserTabInspectionResponse, BrowserUpload, JobResult, StartJobRequest, ViewportState
from . import viewport as viewportmod


MAX_DOWNLOAD_BYTES = 25 << 20
MAX_TAB_SCREENSHOT_BYTES = 10 << 20
# CDP calls on a heavy page (charts, canvas) can hang the websocket with no
# useful progress. Bound them so one screenshot cannot freeze the sidecar.
TAB_CDP_TIMEOUT_SECONDS = 15.0
# First Chromium launch is slower than a CDP ping; do not reuse the 15s tab bound.
BROWSER_START_TIMEOUT_SECONDS = 60.0


def _browser_history_url_key(raw_url: str) -> str:
	raw_url = str(raw_url or "").strip()
	if not raw_url:
		return ""
	parsed = urlparse(raw_url)
	if not (parsed.hostname or "").strip():
		return raw_url
	path = parsed.path or ""
	query = f"?{parsed.query}" if parsed.query else ""
	key = f"{parsed.scheme}://{parsed.netloc}{path}{query}"
	return key.rstrip("/")


def _normalize_open_url(raw_url: str) -> str:
	raw_url = str(raw_url or "").strip()
	if not raw_url:
		return ""
	if "://" not in raw_url:
		raw_url = "https://" + raw_url
	return raw_url


def _browser_urls_equal(a: str, b: str) -> bool:
	if _browser_history_url_key(a) == _browser_history_url_key(b):
		return True
	return _browser_history_url_key(_normalize_open_url(a)) == _browser_history_url_key(_normalize_open_url(b))


@dataclass
class _PersistentBrowser:
	browser: BrowserSession
	downloads_dir: Path
	viewport_width: int
	viewport_height: int
	quality: str = viewportmod.DEFAULT_QUALITY


class BrowserTabNotFound(KeyError):
	"""Raised when a tab is not owned by the requested browser session."""


class BrowserTabUnavailable(RuntimeError):
	"""Raised when Chromium has detached or stopped responding for a tab."""


class BrowserDisconnected(BrowserTabUnavailable):
	"""Raised when the Chromium process or CDP websocket is gone."""


class BrowserUseRunner:
	def __init__(self, settings: Settings):
		self.settings = settings
		self.llm = self._create_llm()
		self.artifacts = BrowserArtifacts(settings.data_dir)
		self._sessions: dict[str, _PersistentBrowser] = {}

	async def run(self, session_id: str, job_id: str, request: StartJobRequest) -> JobResult:
		session_key = self._session_key(session_id)
		profile_dir = self._profile_dir(session_id)
		downloads_dir = self.settings.data_dir / "downloads" / job_id
		session_downloads_dir = self.settings.data_dir / "downloads" / "sessions" / session_key
		uploads_dir = self._uploads_dir(job_id)
		profile_dir.mkdir(parents=True, exist_ok=True)
		downloads_dir.mkdir(parents=True, exist_ok=True)
		session_downloads_dir.mkdir(parents=True, exist_ok=True)
		upload_paths = self._stage_uploads(request.uploads, uploads_dir)
		har_path, _ = self.artifacts.prepare(job_id)
		browser = self._get_or_create_browser(session_id, request, profile_dir, session_downloads_dir, har_path)
		persistent = self._sessions[session_id]
		download_snapshot = self._download_snapshot(persistent.downloads_dir)
		agent = Agent(
			task=request.task,
			llm=self.llm,
			browser=browser,
			use_vision=self.settings.default_use_vision if request.use_vision is None else request.use_vision,
			use_judge=False,
			enable_signal_handler=False,
			calculate_cost=True,
			available_file_paths=[str(path) for path in upload_paths] or None,
			file_system_path=str(uploads_dir),
		)

		async def capture_latest_screenshot(agent: Agent) -> None:
			try:
				state = await asyncio.wait_for(
					agent.browser_session.get_browser_state_summary(),
					timeout=TAB_CDP_TIMEOUT_SECONDS,
				)
				if not _is_web_page(state.url):
					return
				# Give the active page a short paint window. Without this, the first
				# callback can capture browser-use's own blank startup tab instead of
				# the page opened by the preceding browser action.
				await asyncio.sleep(0.25)
				data = await asyncio.wait_for(
					browser.take_screenshot(full_page=False, format="png"),
					timeout=TAB_CDP_TIMEOUT_SECONDS,
				)
				self.artifacts.save_screenshot(job_id, data)
			except Exception:
				# Screenshot capture is best-effort and must never fail the browser task.
				pass

		try:
			history = await agent.run(
				max_steps=request.max_steps or self.settings.max_steps,
				on_step_end=capture_latest_screenshot,
			)
		finally:
			self._collect_downloads(persistent.downloads_dir, downloads_dir, download_snapshot)
			# browser-use's HAR writer includes headers even when response bodies
			# are omitted. Strip sensitive fields before the artifact remains at rest.
			self.artifacts.sanitize_har(job_id)
		tabs = await self._snapshot_tabs(browser)
		self._persist_tabs_state(session_id, tabs)
		return JobResult(
			final_result=_truncate(history.final_result() or "", 64_000),
			done=history.is_done(),
			successful=history.is_successful(),
			visited_urls=_unique_strings(history.urls(), 200, 2_000),
			steps=history.number_of_steps(),
			duration_seconds=history.total_duration_seconds(),
			action_names=_unique_strings(history.action_names(), 500, 200),
			actions=_bounded_actions(history.action_history()),
			errors=_unique_strings(history.errors(), 100, 4_000),
			tabs=tabs,
		)

	async def tabs(self, session_id: str) -> list[BrowserTab]:
		browser = await self._ensure_live_browser(session_id, create=False)
		if browser is None:
			return await self._restore_persisted_tabs(session_id)
		tabs = await self._snapshot_tabs(browser)
		self._persist_tabs_state(session_id, tabs)
		return tabs

	async def open_tab(self, session_id: str, url: str) -> list[BrowserTab]:
		self._validate_direct_url(url)
		browser = await self._ensure_live_browser(session_id, create=True)
		tabs = await self._snapshot_tabs(browser)
		for tab in reversed(tabs):
			if _browser_urls_equal(tab.url, url):
				browser.agent_focus_target_id = None
				try:
					await self._tab_cdp_session(browser, tab.id)
				except BrowserDisconnected as exc:
					await self._recycle_session(session_id)
					raise BrowserTabUnavailable("browser crashed; reopen the tab and retry") from exc
				await asyncio.sleep(0.25)
				tabs = await self._snapshot_tabs(browser)
				self._persist_tabs_state(session_id, tabs)
				_, _ = await self._navigation_history(browser, tab.id)
				return tabs
		try:
			created = await self._await_cdp(
				browser.cdp_client.send.Target.createTarget(params={"url": url}),
				"Target.createTarget",
			)
		except BrowserDisconnected as exc:
			await self._recycle_session(session_id)
			raise BrowserTabUnavailable("browser crashed; reopen the tab and retry") from exc
		if isinstance(created, dict):
			target_id = str(created.get("targetId", ""))
		else:
			target_id = str(getattr(created, "target_id", "") or getattr(created, "targetId", ""))
		if not target_id:
			raise RuntimeError("Chromium did not return a target id for the new tab")
		browser.agent_focus_target_id = None
		try:
			await self._tab_cdp_session(browser, target_id)
		except BrowserDisconnected as exc:
			await self._recycle_session(session_id)
			raise BrowserTabUnavailable("browser crashed; reopen the tab and retry") from exc
		await asyncio.sleep(0.25)
		tabs = await self._snapshot_tabs(browser)
		self._persist_tabs_state(session_id, tabs)
		if tabs:
			focused = next((tab for tab in tabs if tab.active), tabs[-1])
			_, _ = await self._navigation_history(browser, focused.id)
		return tabs

	async def tab_screenshot(self, session_id: str, tab_id: str) -> bytes:
		browser = await self._ensure_live_browser(session_id, create=False)
		if browser is None:
			raise BrowserTabNotFound(tab_id)
		current_tabs = await self._snapshot_tabs(browser)
		if not any(tab.id == tab_id for tab in current_tabs):
			raise BrowserTabNotFound(tab_id)
		browser.agent_focus_target_id = None
		try:
			await self._tab_cdp_session(browser, tab_id)
			await asyncio.sleep(0.1)
			data = await self._await_cdp(
				browser.take_screenshot(full_page=False, format="png"),
				"Page.captureScreenshot",
			)
		except BrowserDisconnected as exc:
			await self._recycle_session(session_id)
			raise BrowserTabUnavailable("browser crashed; reopen the tab and retry") from exc
		except BrowserTabUnavailable as exc:
			raise BrowserTabUnavailable("browser tab screenshot timed out; reopen the tab and retry") from exc
		if not data:
			raise FileNotFoundError("browser tab screenshot is empty")
		if len(data) > MAX_TAB_SCREENSHOT_BYTES:
			raise ValueError(f"browser tab screenshot exceeds {MAX_TAB_SCREENSHOT_BYTES} byte limit")
		return data

	async def inspect_tab(self, session_id: str, tab_id: str) -> BrowserTabInspectionResponse:
		browser = await self._owned_direct_tab(session_id, tab_id)
		payload = await self._evaluate_tab(browser, tab_id, """
(() => {
  const cap = (value, limit) => String(value || '').replace(/\\s+/g, ' ').trim().slice(0, limit);
  const selectorFor = (element) => {
    if (element.id) return '#' + CSS.escape(element.id);
    const parts = [];
    for (let node = element; node && node.nodeType === 1 && parts.length < 5; node = node.parentElement) {
      let part = node.tagName.toLowerCase();
      if (node.getAttribute('name')) part += '[name="' + CSS.escape(node.getAttribute('name')) + '"]';
      else { let index = 1; for (let sibling = node.previousElementSibling; sibling; sibling = sibling.previousElementSibling) if (sibling.tagName === node.tagName) index++; part += ':nth-of-type(' + index + ')'; }
      parts.unshift(part);
      if (node.id) break;
    }
    return parts.join(' > ');
  };
  const elements = Array.from(document.querySelectorAll('a,button,input:not([type="password"]),textarea,select,[role="button"]'))
    .filter((element) => { const style = getComputedStyle(element); return !element.disabled && style.display !== 'none' && style.visibility !== 'hidden'; })
    .slice(0, 100)
    .map((element) => ({selector: selectorFor(element), role: element.getAttribute('role') || element.tagName.toLowerCase(), text: cap(element.innerText || element.value || '', 500), label: cap(element.getAttribute('aria-label') || element.getAttribute('placeholder') || '', 500)}));
  return JSON.stringify({text: cap(document.body ? document.body.innerText : '', 32000), elements});
})()
""")
		try:
			parsed = json.loads(payload or "{}")
		except json.JSONDecodeError as exc:
			raise ValueError("browser inspection returned invalid page data") from exc
		tab = await self._tab_by_id(browser, tab_id)
		return BrowserTabInspectionResponse(tab=tab, text=str(parsed.get("text", "")), elements=[BrowserTabElement.model_validate(item) for item in parsed.get("elements", [])])

	async def act_on_tab(self, session_id: str, tab_id: str, request: BrowserTabActionRequest) -> BrowserTabActionResponse:
		browser = await self._owned_direct_tab(session_id, tab_id)
		if request.action == "navigate":
			self._validate_direct_url(request.url)
			cdp_session = await self._tab_cdp_session(browser, tab_id)
			await self._send_to_tab(
				"Page.navigate",
				cdp_session.cdp_client.send.Page.navigate(params={"url": request.url}, session_id=cdp_session.session_id),
			)
			await asyncio.sleep(0.35)
			result = "navigation started"
		elif request.action == "wait":
			if not 1 <= request.amount <= 30:
				raise ValueError("wait duration must be between 1 and 30 seconds")
			await asyncio.sleep(request.amount)
			result = f"waited {request.amount}s"
		elif request.action == "click":
			if request.x is not None and request.y is not None:
				result = await self._click_tab_at(browser, tab_id, request.x, request.y)
			elif request.selector:
				result = await self._evaluate_tab(browser, tab_id, self._selector_script(request.selector, "click"))
			else:
				raise ValueError("selector or coordinates are required")
		elif request.action == "type":
			if not request.selector or not request.text:
				raise ValueError("selector and text are required")
			result = await self._evaluate_tab(browser, tab_id, self._selector_script(request.selector, "type", request.text))
		elif request.action == "press":
			if not request.key:
				raise ValueError("key is required")
			result = await self._evaluate_tab(browser, tab_id, "(() => { const key = " + json.dumps(request.key) + "; const target = document.activeElement || document.body; target.dispatchEvent(new KeyboardEvent('keydown', {key, bubbles:true})); target.dispatchEvent(new KeyboardEvent('keyup', {key, bubbles:true})); return 'pressed ' + key; })()")
		elif request.action == "scroll":
			if request.amount == 0:
				raise ValueError("scroll amount must not be zero")
			result = await self._evaluate_tab(browser, tab_id, "window.scrollBy({top:" + str(request.amount) + ", behavior:'instant'}); 'scrolled'")
		elif request.action in {"back", "forward"}:
			delta = -1 if request.action == "back" else 1
			result = await self._navigate_history_step(browser, tab_id, delta)
		else:
			raise ValueError("unsupported browser tab action")
		await asyncio.sleep(0.15)
		tab = await self._tab_by_id(browser, tab_id)
		tabs = await self._snapshot_tabs(browser)
		self._persist_tabs_state(session_id, tabs)
		nav_urls, nav_index = await self._navigation_history(browser, tab_id)
		return BrowserTabActionResponse(
			tab=tab,
			result=_truncate(str(result), 4_000),
			tabs=tabs,
			navigation_urls=nav_urls,
			navigation_index=nav_index,
		)

	async def _navigate_history_step(self, browser: BrowserSession, tab_id: str, delta: int) -> str:
		if delta not in {-1, 1}:
			raise ValueError("history step must be back or forward")
		cdp_session = await self._tab_cdp_session(browser, tab_id)
		response = await self._send_to_tab(
			"Page.getNavigationHistory",
			cdp_session.cdp_client.send.Page.getNavigationHistory(session_id=cdp_session.session_id),
		)
		entries: list[Any]
		current_index = -1
		if isinstance(response, dict):
			entries = response.get("entries") or []
			current_index = int(response.get("currentIndex", -1))
		else:
			entries = list(getattr(response, "entries", None) or [])
			current_index = int(getattr(response, "current_index", -1) or getattr(response, "currentIndex", -1))
		target_index = current_index + delta
		if current_index < 0 or target_index < 0 or target_index >= len(entries):
			raise ValueError("cannot navigate " + ("back" if delta < 0 else "forward"))
		entry = entries[target_index]
		if isinstance(entry, dict):
			entry_id = entry.get("id")
		else:
			entry_id = getattr(entry, "id", None)
		if entry_id is None:
			raise ValueError("browser history entry is unavailable")
		await self._send_to_tab(
			"Page.navigateToHistoryEntry",
			cdp_session.cdp_client.send.Page.navigateToHistoryEntry(
				params={"entryId": entry_id},
				session_id=cdp_session.session_id,
			),
		)
		await asyncio.sleep(0.35)
		return "history " + ("back" if delta < 0 else "forward")

	async def _navigation_history(self, browser: BrowserSession, tab_id: str) -> tuple[list[str], int]:
		cdp_session = await self._tab_cdp_session(browser, tab_id)
		response = await self._send_to_tab(
			"Page.getNavigationHistory",
			cdp_session.cdp_client.send.Page.getNavigationHistory(session_id=cdp_session.session_id),
		)
		if isinstance(response, dict):
			entries = response.get("entries") or []
			current_index = int(response.get("currentIndex", -1))
		else:
			entries = list(getattr(response, "entries", None) or [])
			current_index = int(getattr(response, "current_index", -1) or getattr(response, "currentIndex", -1))
		return compact_navigation_history(entries, current_index)

	async def tab_history(self, session_id: str, tab_id: str) -> BrowserTabHistoryResponse:
		browser = await self._owned_direct_tab(session_id, tab_id)
		urls, index = await self._navigation_history(browser, tab_id)
		tab = await self._tab_by_id(browser, tab_id)
		return BrowserTabHistoryResponse(tab=tab, navigation_urls=urls, navigation_index=index)

	async def _owned_direct_tab(self, session_id: str, tab_id: str) -> BrowserSession:
		browser = await self._ensure_live_browser(session_id, create=False)
		if browser is None:
			raise BrowserTabNotFound(tab_id)
		await self._tab_by_id(browser, tab_id)
		browser.agent_focus_target_id = None
		try:
			await self._tab_cdp_session(browser, tab_id)
		except BrowserDisconnected:
			await self._recycle_session(session_id)
			raise
		return browser

	async def _recycle_session(self, session_id: str) -> None:
		persistent = self._sessions.pop(session_id, None)
		if persistent is None:
			return
		try:
			await persistent.browser.kill()
		except Exception:
			pass

	async def _start_new_browser(self, session_id: str) -> BrowserSession:
		browser = self._browser_for_direct_tab(session_id)
		try:
			await self._await_cdp(browser.start(), "Browser.start", timeout=BROWSER_START_TIMEOUT_SECONDS)
			return browser
		except BaseException:
			await self._recycle_session(session_id)
			raise

	async def _ensure_live_browser(self, session_id: str, *, create: bool) -> BrowserSession | None:
		persistent = self._sessions.get(session_id)
		if persistent is None:
			if not create:
				return None
			return await self._start_new_browser(session_id)
		try:
			await self._await_cdp(persistent.browser.start(), "Browser.start", timeout=BROWSER_START_TIMEOUT_SECONDS)
			await self._await_cdp(persistent.browser.get_tabs(), "Target.getTargets")
			return persistent.browser
		except BrowserTabUnavailable:
			await self._recycle_session(session_id)
			if not create:
				return None
			return await self._start_new_browser(session_id)

	async def _tab_by_id(self, browser: BrowserSession, tab_id: str) -> BrowserTab:
		for tab in await self._snapshot_tabs(browser):
			if tab.id == tab_id:
				return tab
		raise BrowserTabNotFound(tab_id)

	async def _evaluate_tab(self, browser: BrowserSession, tab_id: str, expression: str) -> str:
		cdp_session = await self._tab_cdp_session(browser, tab_id)
		response = await self._send_to_tab(
			"Runtime.evaluate",
			cdp_session.cdp_client.send.Runtime.evaluate(
				params={"expression": expression, "returnByValue": True, "awaitPromise": True},
				session_id=cdp_session.session_id,
			),
		)
		if isinstance(response, dict):
			exception = response.get("exceptionDetails")
			result = response.get("result")
		else:
			exception = getattr(response, "exception_details", None) or getattr(response, "exceptionDetails", None)
			result = getattr(response, "result", None)
		if exception:
			raise ValueError("page interaction failed")
		if isinstance(result, dict):
			return str(result.get("value", ""))
		if result is not None:
			return str(getattr(result, "value", ""))
		return ""

	async def _tab_cdp_session(self, browser: BrowserSession, tab_id: str):
		try:
			cdp_session = await self._await_cdp(
				browser.get_or_create_cdp_session(tab_id, focus=True),
				"Target.attachToTarget",
			)
		except BrowserDisconnected:
			raise
		except (BrowserTabUnavailable, ValueError) as exc:
			raise BrowserTabUnavailable("browser tab is temporarily unavailable; reopen it and retry") from exc
		await self._pin_tab_viewport(cdp_session, browser)
		return cdp_session

	def _default_viewport(self) -> viewportmod.ViewportPreset:
		return viewportmod.preset_from_size(self.settings.viewport_width, self.settings.viewport_height)

	def _viewport_preset(self, session_id: str) -> viewportmod.ViewportPreset:
		persistent = self._sessions.get(session_id)
		if persistent is not None and persistent.quality:
			return viewportmod.resolve_quality(persistent.quality, self._default_viewport().quality)
		return viewportmod.load_persisted(self.settings.data_dir, self._session_key(session_id), self._default_viewport())

	def viewport_state(self, session_id: str) -> ViewportState:
		preset = self._viewport_preset(session_id)
		return ViewportState(
			quality=preset.quality,
			width=preset.width,
			height=preset.height,
			label=preset.label,
			options=viewportmod.options(),
		)

	async def set_viewport(self, session_id: str, quality: str) -> ViewportState:
		preset = viewportmod.parse_quality(quality)
		started = time.perf_counter()
		viewportmod.persist(self.settings.data_dir, self._session_key(session_id), preset)
		persistent = self._sessions.get(session_id)
		applied_tabs = 0
		if persistent is not None:
			persistent.quality = preset.quality
			persistent.viewport_width = preset.width
			persistent.viewport_height = preset.height
			browser = await self._ensure_live_browser(session_id, create=False)
			if browser is None:
				return self.viewport_state(session_id)
			tabs = await self._snapshot_tabs(browser)
			for tab in tabs:
				try:
					await self._tab_cdp_session(browser, tab.id)
					applied_tabs += 1
				except BrowserTabUnavailable:
					continue
		duration_ms = int((time.perf_counter() - started) * 1000)
		logging.getLogger("browser-use.viewport").info(
			"viewport applied session=%s quality=%s %sx%s tabs=%d live=%s duration_ms=%d",
			session_id[:16],
			preset.quality,
			preset.width,
			preset.height,
			applied_tabs,
			persistent is not None,
			duration_ms,
		)
		return self.viewport_state(session_id)

	def _pin_metrics(self, browser: BrowserSession | None) -> tuple[int, int]:
		if browser is not None:
			for persistent in self._sessions.values():
				if persistent.browser is browser:
					return persistent.viewport_width, persistent.viewport_height
		default = self._default_viewport()
		return default.width, default.height

	async def _pin_tab_viewport(self, cdp_session, browser: BrowserSession | None = None) -> None:
		# browser-use applies its viewport override only on its own TabCreated /
		# AgentFocusChanged paths. This sidecar creates targets over raw CDP, so
		# without this every UI-opened tab keeps Chromium's 800x600 headless
		# default window and sites answer with their narrow/mobile layout.
		width, height = self._pin_metrics(browser)
		try:
			await self._await_cdp(
				cdp_session.cdp_client.send.Emulation.setDeviceMetricsOverride(
					params={
						"width": width,
						"height": height,
						"deviceScaleFactor": 1.0,
						"mobile": False,
					},
					session_id=cdp_session.session_id,
				),
				"Emulation.setDeviceMetricsOverride",
			)
		except BrowserDisconnected:
			raise
		except BrowserTabUnavailable as exc:
			raise BrowserTabUnavailable("browser tab viewport could not be applied; retry") from exc

	async def _await_cdp(self, operation, command: str, timeout: float | None = None):
		limit = TAB_CDP_TIMEOUT_SECONDS if timeout is None else timeout
		try:
			return await asyncio.wait_for(operation, timeout=limit)
		except TimeoutError as exc:
			raise BrowserTabUnavailable(f"{command} timed out; reopen the tab and retry") from exc
		except BrowserTabUnavailable:
			raise
		except Exception as exc:
			# ConnectionClosed / OSError / browser-use CDP errors are not RuntimeError.
			# Leaving them uncaught 500s the API and keeps a dead BrowserSession in RAM.
			raise BrowserDisconnected(f"{command} failed because Chromium disconnected") from exc

	async def _send_to_tab(self, command: str, operation):
		return await self._await_cdp(operation, command)

	async def _click_tab_at(self, browser: BrowserSession, tab_id: str, x: int, y: int) -> str:
		if x < 0 or y < 0 or x > 10_000 or y > 10_000:
			raise ValueError("click coordinates are out of range")
		cdp_session = await self._tab_cdp_session(browser, tab_id)
		point = {"x": float(x), "y": float(y)}
		events = (
			{"type": "mouseMoved", **point},
			{"type": "mousePressed", "button": "left", "clickCount": 1, **point},
			{"type": "mouseReleased", "button": "left", "clickCount": 1, **point},
		)
		for params in events:
			await self._send_to_tab(
				"Input.dispatchMouseEvent",
				cdp_session.cdp_client.send.Input.dispatchMouseEvent(
					params=params,
					session_id=cdp_session.session_id,
				),
			)
		return f"clicked at {x},{y}"

	def _selector_script(self, selector: str, action: str, text: str = "") -> str:
		selector_json = json.dumps(selector)
		if action == "click":
			return "(() => { const el = document.querySelector(" + selector_json + "); if (!el) throw new Error('element not found'); el.scrollIntoView({block:'center', inline:'center'}); el.click(); return 'clicked'; })()"
		return "(() => { const el = document.querySelector(" + selector_json + "); if (!el) throw new Error('element not found'); if (String(el.type || '').toLowerCase() === 'password') throw new Error('password fields are not supported'); el.focus(); el.value = " + json.dumps(text) + "; el.dispatchEvent(new Event('input', {bubbles:true})); el.dispatchEvent(new Event('change', {bubbles:true})); return 'typed'; })()"

	async def close_tab(self, session_id: str, tab_id: str) -> list[BrowserTab]:
		browser = await self._ensure_live_browser(session_id, create=False)
		if browser is None:
			raise BrowserTabNotFound(tab_id)
		current_tabs = await self._snapshot_tabs(browser)
		if not any(tab.id == tab_id for tab in current_tabs):
			raise BrowserTabNotFound(tab_id)
		try:
			await self._await_cdp(browser.close_page(tab_id), "Target.closeTarget")
		except BrowserDisconnected as exc:
			await self._recycle_session(session_id)
			raise BrowserTabUnavailable("browser crashed; reopen the tab and retry") from exc
		except BrowserTabUnavailable as exc:
			raise BrowserTabUnavailable("browser tab could not be closed; retry the operation") from exc
		await asyncio.sleep(0)
		tabs = await self._snapshot_tabs(browser)
		self._persist_tabs_state(session_id, tabs)
		return tabs

	def has_session(self, session_id: str) -> bool:
		return session_id in self._sessions

	def list_sessions(self) -> list[str]:
		return list(self._sessions.keys())

	async def kill_session(self, session_id: str) -> None:
		persistent = self._sessions.pop(session_id, None)
		if persistent is None:
			return
		try:
			await persistent.browser.kill()
		except Exception:
			pass

	async def shutdown(self) -> None:
		sessions = list(self._sessions.values())
		self._sessions.clear()
		for persistent in sessions:
			try:
				await persistent.browser.kill()
			except Exception:
				# Shutdown is best-effort; the sidecar is exiting and Chromium will
				# be reaped with the container if it cannot be contacted.
				pass

		return None

	def screenshot_available(self, job_id: str) -> bool:
		return self.artifacts.screenshot_available(job_id)

	def read_screenshot(self, job_id: str) -> bytes:
		return self.artifacts.read_screenshot(job_id)

	def network_loads(self, job_id: str, limit: int):
		return self.artifacts.network_loads(job_id, limit)

	def cleanup(self, job_id: str) -> None:
		self.artifacts.cleanup(job_id)
		try:
			shutil.rmtree(self._downloads_dir(job_id))
		except FileNotFoundError:
			pass
		except OSError:
			pass
		try:
			shutil.rmtree(self._uploads_dir(job_id))
		except FileNotFoundError:
			pass
		except OSError:
			pass

	def list_downloads(self, job_id: str) -> list[BrowserDownload]:
		root = self._downloads_dir(job_id)
		if not root.is_dir():
			return []
		files: list[BrowserDownload] = []
		for path in sorted(root.iterdir(), key=lambda item: item.name.lower()):
			try:
				resolved = path.resolve()
				resolved.relative_to(root)
				if not resolved.is_file():
					continue
				size = resolved.stat().st_size
			except (OSError, ValueError):
				continue
			mime_type, _ = mimetypes.guess_type(resolved.name)
			files.append(BrowserDownload(name=resolved.name, mime_type=mime_type or "application/octet-stream", size=size))
		return files

	def read_download(self, job_id: str, name: str) -> tuple[BrowserDownload, bytes]:
		for download in self.list_downloads(job_id):
			if download.name != name:
				continue
			if download.size > MAX_DOWNLOAD_BYTES:
				raise ValueError(f"browser download exceeds {MAX_DOWNLOAD_BYTES} byte limit")
			path = self._downloads_dir(job_id) / download.name
			data = path.read_bytes()
			if not data:
				raise FileNotFoundError("browser download is empty")
			return download, data
		raise FileNotFoundError("browser download not found")

	def _downloads_dir(self, job_id: str) -> Path:
		return (self.settings.data_dir / "downloads" / job_id).resolve()

	def _tabs_state_path(self, session_id: str) -> Path:
		session_key = self._session_key(session_id)
		return self.settings.data_dir / "sessions" / session_key / "tabs.json"

	def _persist_tabs_state(self, session_id: str, tabs: list[BrowserTab]) -> None:
		path = self._tabs_state_path(session_id)
		path.parent.mkdir(parents=True, exist_ok=True)
		payload = {
			"session_id": session_id,
			"tabs": [
				{"id": tab.id, "url": tab.url, "title": tab.title, "active": tab.active}
				for tab in tabs
				if _is_web_page(tab.url)
			],
		}
		path.write_text(json.dumps(payload, ensure_ascii=True), encoding="utf-8")

	def _tab_dicts(self, raw: Any) -> list[dict[str, Any]]:
		if isinstance(raw, dict):
			raw = raw.get("tabs", [])
		if not isinstance(raw, list):
			return []
		result: list[dict[str, Any]] = []
		for item in raw:
			if not isinstance(item, dict):
				continue
			url = str(item.get("url", "")).strip()
			if not _is_web_page(url):
				continue
			result.append(
				{
					"id": str(item.get("id", "")).strip(),
					"url": url,
					"title": str(item.get("title", "")).strip(),
					"active": bool(item.get("active")),
				}
			)
		return result

	def _load_tabs_state(self, session_id: str) -> list[dict[str, Any]]:
		path = self._tabs_state_path(session_id)
		if not path.is_file():
			return []
		try:
			raw = json.loads(path.read_text(encoding="utf-8"))
		except (OSError, json.JSONDecodeError):
			return []
		return self._tab_dicts(raw)

	def persisted_tabs(self, session_id: str) -> list[BrowserTab]:
		return [
			BrowserTab(id=item["id"], url=item["url"], title=item["title"], active=item["active"])
			for item in self._load_tabs_state(session_id)
			if item.get("id")
		]

	def list_persisted_tab_sessions(self) -> dict[str, list[BrowserTab]]:
		root = self.settings.data_dir / "sessions"
		if not root.is_dir():
			return {}
		out: dict[str, list[BrowserTab]] = {}
		for path in root.glob("*/tabs.json"):
			try:
				raw = json.loads(path.read_text(encoding="utf-8"))
			except (OSError, json.JSONDecodeError):
				continue
			session_id = ""
			if isinstance(raw, dict):
				session_id = str(raw.get("session_id", "")).strip()
			if not session_id:
				continue
			tabs = [
				BrowserTab(id=item["id"], url=item["url"], title=item["title"], active=item["active"])
				for item in self._tab_dicts(raw)
				if item.get("id")
			]
			if tabs:
				out[session_id] = tabs
		return out

	async def _restore_persisted_tabs(self, session_id: str) -> list[BrowserTab]:
		state = self._load_tabs_state(session_id)
		if not state:
			return []
		browser = await self._ensure_live_browser(session_id, create=True)
		existing = await self._snapshot_tabs(browser)
		if existing:
			self._persist_tabs_state(session_id, existing)
			return existing
		for item in state:
			url = str(item.get("url", "")).strip()
			if not url:
				continue
			try:
				created = await self._await_cdp(
					browser.cdp_client.send.Target.createTarget(params={"url": url}),
					"Target.createTarget",
				)
			except BrowserDisconnected as exc:
				await self._recycle_session(session_id)
				raise BrowserTabUnavailable("browser crashed; reopen the tab and retry") from exc
			if isinstance(created, dict):
				target_id = str(created.get("targetId", ""))
			else:
				target_id = str(getattr(created, "target_id", "") or getattr(created, "targetId", ""))
			if not target_id:
				continue
			browser.agent_focus_target_id = None
			try:
				await self._tab_cdp_session(browser, target_id)
			except BrowserDisconnected as exc:
				await self._recycle_session(session_id)
				raise BrowserTabUnavailable("browser crashed; reopen the tab and retry") from exc
			await asyncio.sleep(0.15)
		tabs = await self._snapshot_tabs(browser)
		self._persist_tabs_state(session_id, tabs)
		return tabs

	def _session_key(self, session_id: str) -> str:
		return hashlib.sha256(session_id.encode("utf-8")).hexdigest()

	def _profile_dir(self, session_id: str) -> Path:
		# Isolate Chromium profiles per viewport so changing
		# BROWSER_USE_VIEWPORT_* picks up a fresh desktop window size.
		session_key = self._session_key(session_id)
		viewport_tag = f"{self.settings.viewport_width}x{self.settings.viewport_height}"
		return self.settings.data_dir / "profiles" / session_key / viewport_tag

	def _get_or_create_browser(
		self,
		session_id: str,
		request: StartJobRequest,
		profile_dir: Path,
		downloads_dir: Path,
		har_path: Path,
	) -> BrowserSession:
		persistent = self._sessions.get(session_id)
		if persistent is not None:
			return persistent.browser

		preset = self._viewport_preset(session_id)
		self._clear_stale_chromium_locks(profile_dir)
		profile = BrowserProfile(
			executable_path=self.settings.executable_path,
			headless=self.settings.headless,
			user_data_dir=profile_dir,
			downloads_path=downloads_dir,
			allowed_domains=self._allowed_domains(request),
			prohibited_domains=list(self.settings.prohibited_domains) or None,
			block_ip_addresses=self.settings.block_ip_addresses,
			chromium_sandbox=self.settings.chromium_sandbox,
			# Keep every tab, tool call, and manual open on the same desktop viewport.
			# Note: browser-use 0.13.6 BrowserProfile has no is_mobile/has_touch
			# fields and silently ignores unknown kwargs; desktop emulation is
			# enforced per tab in _pin_tab_viewport instead.
			window_size={"width": preset.width, "height": preset.height},
			viewport={"width": preset.width, "height": preset.height},
			user_agent=(
				"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
				"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
			),
			device_scale_factor=1.0,
			# Keep the Chromium process and its tabs alive after Agent.run(). The
			# same BrowserSession object is reused for later jobs in this session.
			keep_alive=True,
			proxy={"server": self.settings.proxy_url} if self.settings.proxy_url else None,
			# browser-use polls the CDP endpoint through 127.0.0.1. Pin Chromium to
			# that address as well; newer Chromium builds may otherwise select IPv6
			# localhost, which makes the browser-use startup probe time out.
			args=["--remote-debugging-address=127.0.0.1"],
			record_har_path=har_path,
			record_har_content="omit",
			record_har_mode="full",
		)
		browser = BrowserSession(browser_profile=profile)
		self._sessions[session_id] = _PersistentBrowser(
			browser=browser,
			downloads_dir=downloads_dir,
			viewport_width=preset.width,
			viewport_height=preset.height,
			quality=preset.quality,
		)
		return browser

	def _browser_for_direct_tab(self, session_id: str) -> BrowserSession:
		profile_dir = self._profile_dir(session_id)
		session_key = self._session_key(session_id)
		downloads_dir = self.settings.data_dir / "downloads" / "sessions" / session_key
		artifact_dir = self.settings.data_dir / "browser-sessions" / session_key
		profile_dir.mkdir(parents=True, exist_ok=True)
		downloads_dir.mkdir(parents=True, exist_ok=True)
		artifact_dir.mkdir(parents=True, exist_ok=True)
		return self._get_or_create_browser(
			session_id,
			StartJobRequest(task="Direct URL open"),
			profile_dir,
			downloads_dir,
			artifact_dir / "network.har",
		)

	def _validate_direct_url(self, url: str) -> None:
		parsed = urlparse(url)
		host = (parsed.hostname or "").strip(".").lower()
		if parsed.scheme not in {"http", "https"} or not host or parsed.username or parsed.password:
			raise ValueError("url must be an HTTP or HTTPS address without credentials")
		if any(_domain_matches(host, value) for value in self.settings.prohibited_domains):
			raise ValueError("url host is prohibited by browser policy")
		if self.settings.allowed_domains and not any(_domain_matches(host, value) for value in self.settings.allowed_domains):
			raise ValueError("url host is not in the browser allowlist")
		if self.settings.block_ip_addresses:
			if host == "localhost" or host.endswith(".localhost"):
				raise ValueError("local addresses are blocked by browser policy")
			try:
				address = ipaddress.ip_address(host)
			except ValueError:
				address = None
			if address is not None and not address.is_global:
				raise ValueError("non-public IP addresses are blocked by browser policy")

	def _download_snapshot(self, directory: Path) -> dict[str, tuple[int, int]]:
		if not directory.is_dir():
			return {}
		result: dict[str, tuple[int, int]] = {}
		for path in directory.iterdir():
			try:
				if path.is_file():
					stat = path.stat()
					result[path.name] = (stat.st_size, stat.st_mtime_ns)
			except OSError:
				continue
		return result

	def _collect_downloads(
		self,
		session_directory: Path,
		job_directory: Path,
		before: dict[str, tuple[int, int]],
	) -> None:
		job_directory.mkdir(parents=True, exist_ok=True)
		for path in session_directory.iterdir():
			try:
				if not path.is_file():
					continue
				stat = path.stat()
				current = (stat.st_size, stat.st_mtime_ns)
			except OSError:
				continue
			if before.get(path.name) == current:
				continue
			target = job_directory / path.name
			index = 1
			while target.exists():
				target = job_directory / f"{index}-{path.name}"
				index += 1
			try:
				shutil.move(str(path), str(target))
			except OSError:
				continue

	async def _snapshot_tabs(self, browser: BrowserSession) -> list[BrowserTab]:
		focused_id = str(browser.agent_focus_target_id or "")
		return [
			BrowserTab(
				id=str(tab.target_id),
				url=str(tab.url or ""),
				title=str(tab.title or ""),
				active=str(tab.target_id) == focused_id,
			)
			for tab in await self._await_cdp(browser.get_tabs(), "Target.getTargets")
		]

	def _clear_stale_chromium_locks(self, profile_dir: Path) -> None:
		# Chromium leaves these files behind when a job/container is interrupted.
		# Jobs sharing a profile are serialized by JobManager, so no live browser can
		# own them when a new job for this session starts.
		for name in ("SingletonLock", "SingletonCookie", "SingletonSocket"):
			try:
				(profile_dir / name).unlink(missing_ok=True)
			except OSError:
				# A malformed profile must not prevent the job from reporting Chromium's
				# own startup error.
				pass

	def _uploads_dir(self, job_id: str) -> Path:
		return (self.settings.data_dir / "uploads" / job_id).resolve()

	def _stage_uploads(self, uploads: list[BrowserUpload], directory: Path) -> list[Path]:
		directory.mkdir(parents=True, exist_ok=True)
		paths: list[Path] = []
		for index, upload in enumerate(uploads, start=1):
			data = base64.b64decode(upload.data_base64, validate=True)
			path = directory / upload.name
			if path.exists():
				path = directory / f"{index}-{upload.name}"
			path.write_bytes(data)
			paths.append(path)
		return paths

	def _allowed_domains(self, request: StartJobRequest) -> list[str] | None:
		# A deployment-wide allowlist is authoritative. Callers can only provide a
		# per-job allowlist when the operator has not configured one.
		if self.settings.allowed_domains:
			return list(self.settings.allowed_domains)
		return request.allowed_domains or None

	def _create_llm(self):
		provider = self.settings.llm_provider
		model = self.settings.llm_model
		base_url = self.settings.llm_base_url
		if provider == "openai":
			from browser_use import ChatOpenAI

			return ChatOpenAI(
				model=model,
				api_key=_required_key("OPENAI_API_KEY"),
				base_url=base_url,
			)
		if provider == "browser-use":
			from browser_use import ChatBrowserUse

			return ChatBrowserUse(
				model=model,
				api_key=_required_key("BROWSER_USE_API_KEY"),
			)
		if provider == "openrouter":
			from browser_use.llm.openrouter.chat import ChatOpenRouter

			return ChatOpenRouter(
				model=model,
				api_key=_required_key("OPENROUTER_API_KEY"),
				base_url=base_url or "https://openrouter.ai/api/v1",
			)
		if provider == "anthropic":
			from browser_use import ChatAnthropic

			return ChatAnthropic(
				model=model,
				api_key=_required_key("ANTHROPIC_API_KEY"),
				base_url=base_url,
			)
		if provider == "google":
			from browser_use import ChatGoogle

			return ChatGoogle(
				model=model,
				api_key=_required_key("GOOGLE_API_KEY"),
			)
		raise RuntimeError(f"unsupported LLM provider: {provider}")


def _truncate(value: str, limit: int) -> str:
	return value if len(value) <= limit else value[:limit] + "..."


def _is_web_page(value: str | None) -> bool:
	parsed = urlparse(value or "")
	return parsed.scheme in {"http", "https"} and bool(parsed.netloc)


def _history_entry_url(entry: Any) -> str:
	if isinstance(entry, dict):
		return str(entry.get("url", "")).strip()
	return str(getattr(entry, "url", "") or "").strip()


def compact_navigation_history(entries: list[Any], current_index: int) -> tuple[list[str], int]:
	"""Keep the forward stack after Back. Do not snap a skipped about:blank to the last URL."""
	urls: list[str] = []
	entry_to_compact: dict[int, int] = {}
	for index, entry in enumerate(entries or []):
		url = _history_entry_url(entry)
		if not _is_web_page(url):
			continue
		if not urls or urls[-1] != url:
			urls.append(url)
		entry_to_compact[index] = len(urls) - 1
	if not urls:
		return [], -1
	if current_index in entry_to_compact:
		return urls, entry_to_compact[current_index]
	for index in range(current_index, -1, -1):
		if index in entry_to_compact:
			return urls, entry_to_compact[index]
	for index in range(current_index + 1, len(entries or [])):
		if index in entry_to_compact:
			return urls, entry_to_compact[index]
	return urls, 0


def _domain_matches(host: str, rule: str) -> bool:
	rule = str(rule or "").strip().lower()
	if "://" in rule:
		rule = (urlparse(rule).hostname or "").lower()
	rule = rule.lstrip("*.").strip(".")
	return bool(rule) and (host == rule or host.endswith("." + rule))


def _required_key(name: str) -> str:
	value = os.getenv(name, "").strip()
	if not value:
		raise RuntimeError(f"{name} is required for the selected browser-use LLM provider")
	return value


def _unique_strings(values: list[Any], max_items: int, max_length: int) -> list[str]:
	result: list[str] = []
	for value in values:
		if value is None:
			continue
		text = _truncate(str(value), max_length)
		if text and text not in result:
			result.append(text)
		if len(result) >= max_items:
			break
	return result


def _bounded_actions(steps: list[list[dict[str, Any]]]) -> list[dict[str, Any]]:
	actions: list[dict[str, Any]] = []
	for step_number, step in enumerate(steps, start=1):
		for action in step:
			item = _sanitize(action, depth=0)
			if isinstance(item, dict):
				item["step"] = step_number
				actions.append(item)
			if len(actions) >= 200:
				return actions
	return actions


def _sanitize(value: Any, depth: int) -> Any:
	if depth >= 5:
		return _truncate(str(value), 1_000)
	if isinstance(value, str):
		return _truncate(value, 4_000)
	if isinstance(value, dict):
		return {str(key)[:200]: _sanitize(item, depth + 1) for key, item in list(value.items())[:50]}
	if isinstance(value, list):
		return [_sanitize(item, depth + 1) for item in value[:50]]
	if value is None or isinstance(value, (bool, int, float)):
		return value
	return _truncate(str(value), 1_000)
