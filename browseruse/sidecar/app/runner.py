from __future__ import annotations

import asyncio
import base64
import hashlib
import ipaddress
import json
import mimetypes
import os
import shutil
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

from browser_use import Agent, BrowserProfile, BrowserSession

from .artifacts import BrowserArtifacts
from .config import Settings
from .models import BrowserDownload, BrowserTab, BrowserTabActionRequest, BrowserTabActionResponse, BrowserTabElement, BrowserTabInspectionResponse, BrowserUpload, JobResult, StartJobRequest


MAX_DOWNLOAD_BYTES = 25 << 20
MAX_TAB_SCREENSHOT_BYTES = 10 << 20


@dataclass
class _PersistentBrowser:
	browser: BrowserSession
	downloads_dir: Path


class BrowserTabNotFound(KeyError):
	"""Raised when a tab is not owned by the requested browser session."""


class BrowserTabUnavailable(RuntimeError):
	"""Raised when Chromium has detached or stopped responding for a tab."""


class BrowserUseRunner:
	def __init__(self, settings: Settings):
		self.settings = settings
		self.llm = self._create_llm()
		self.artifacts = BrowserArtifacts(settings.data_dir)
		self._sessions: dict[str, _PersistentBrowser] = {}

	async def run(self, session_id: str, job_id: str, request: StartJobRequest) -> JobResult:
		session_key = self._session_key(session_id)
		profile_dir = self.settings.data_dir / "profiles" / session_key
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
				state = await agent.browser_session.get_browser_state_summary()
				if not _is_web_page(state.url):
					return
				# Give the active page a short paint window. Without this, the first
				# callback can capture browser-use's own blank startup tab instead of
				# the page opened by the preceding browser action.
				await asyncio.sleep(0.25)
				data = await browser.take_screenshot(full_page=False, format="png")
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
			tabs=await self._snapshot_tabs(browser),
		)

	async def tabs(self, session_id: str) -> list[BrowserTab]:
		persistent = self._sessions.get(session_id)
		if persistent is None:
			return []
		await persistent.browser.start()
		return await self._snapshot_tabs(persistent.browser)

	async def open_tab(self, session_id: str, url: str) -> list[BrowserTab]:
		self._validate_direct_url(url)
		browser = self._browser_for_direct_tab(session_id)
		await browser.start()
		created = await browser.cdp_client.send.Target.createTarget(params={"url": url})
		if isinstance(created, dict):
			target_id = str(created.get("targetId", ""))
		else:
			target_id = str(getattr(created, "target_id", "") or getattr(created, "targetId", ""))
		if not target_id:
			raise RuntimeError("Chromium did not return a target id for the new tab")
		browser.agent_focus_target_id = None
		await browser.get_or_create_cdp_session(target_id, focus=True)
		await asyncio.sleep(0.25)
		return await self._snapshot_tabs(browser)

	async def tab_screenshot(self, session_id: str, tab_id: str) -> bytes:
		persistent = self._sessions.get(session_id)
		if persistent is None:
			raise BrowserTabNotFound(tab_id)
		browser = persistent.browser
		await browser.start()
		current_tabs = await self._snapshot_tabs(browser)
		if not any(tab.id == tab_id for tab in current_tabs):
			raise BrowserTabNotFound(tab_id)
		browser.agent_focus_target_id = None
		await self._tab_cdp_session(browser, tab_id)
		await asyncio.sleep(0.1)
		# A full-page capture can be unbounded on applications with virtual or
		# infinite scrolling, leaving the CDP WebSocket blocked until its timeout.
		# The direct-tab tool only needs the currently visible page state.
		try:
			data = await browser.take_screenshot(full_page=False, format="png")
		except (RuntimeError, TimeoutError) as exc:
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
			if not request.selector:
				raise ValueError("selector is required")
			result = await self._evaluate_tab(browser, tab_id, self._selector_script(request.selector, "click"))
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
		else:
			raise ValueError("unsupported browser tab action")
		await asyncio.sleep(0.15)
		tab = await self._tab_by_id(browser, tab_id)
		return BrowserTabActionResponse(tab=tab, result=_truncate(str(result), 4_000), tabs=await self._snapshot_tabs(browser))

	async def _owned_direct_tab(self, session_id: str, tab_id: str) -> BrowserSession:
		persistent = self._sessions.get(session_id)
		if persistent is None:
			raise BrowserTabNotFound(tab_id)
		browser = persistent.browser
		await browser.start()
		await self._tab_by_id(browser, tab_id)
		browser.agent_focus_target_id = None
		await self._tab_cdp_session(browser, tab_id)
		return browser

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
			return await browser.get_or_create_cdp_session(tab_id, focus=True)
		except (RuntimeError, TimeoutError, ValueError) as exc:
			raise BrowserTabUnavailable("browser tab is temporarily unavailable; reopen it and retry") from exc

	async def _send_to_tab(self, command: str, operation):
		try:
			return await operation
		except (RuntimeError, TimeoutError) as exc:
			raise BrowserTabUnavailable(f"{command} failed because the browser tab is temporarily unavailable") from exc

	def _selector_script(self, selector: str, action: str, text: str = "") -> str:
		selector_json = json.dumps(selector)
		if action == "click":
			return "(() => { const el = document.querySelector(" + selector_json + "); if (!el) throw new Error('element not found'); el.scrollIntoView({block:'center', inline:'center'}); el.click(); return 'clicked'; })()"
		return "(() => { const el = document.querySelector(" + selector_json + "); if (!el) throw new Error('element not found'); if (String(el.type || '').toLowerCase() === 'password') throw new Error('password fields are not supported'); el.focus(); el.value = " + json.dumps(text) + "; el.dispatchEvent(new Event('input', {bubbles:true})); el.dispatchEvent(new Event('change', {bubbles:true})); return 'typed'; })()"

	async def close_tab(self, session_id: str, tab_id: str) -> list[BrowserTab]:
		persistent = self._sessions.get(session_id)
		if persistent is None:
			raise BrowserTabNotFound(tab_id)
		browser = persistent.browser
		await browser.start()
		current_tabs = await self._snapshot_tabs(browser)
		if not any(tab.id == tab_id for tab in current_tabs):
			raise BrowserTabNotFound(tab_id)
		try:
			await browser.close_page(tab_id)
		except (RuntimeError, TimeoutError) as exc:
			raise BrowserTabUnavailable("browser tab could not be closed; retry the operation") from exc
		# Let the CDP detach event update SessionManager before returning the
		# post-close snapshot.
		await asyncio.sleep(0)
		return await self._snapshot_tabs(browser)

	def has_session(self, session_id: str) -> bool:
		return session_id in self._sessions

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

	def _session_key(self, session_id: str) -> str:
		return hashlib.sha256(session_id.encode("utf-8")).hexdigest()

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
		self._sessions[session_id] = _PersistentBrowser(browser=browser, downloads_dir=downloads_dir)
		return browser

	def _browser_for_direct_tab(self, session_id: str) -> BrowserSession:
		session_key = self._session_key(session_id)
		profile_dir = self.settings.data_dir / "profiles" / session_key
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
			for tab in await browser.get_tabs()
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
