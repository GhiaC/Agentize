from __future__ import annotations

import asyncio
import hashlib
import logging
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from typing import Protocol
from uuid import uuid4

from fastapi import HTTPException, status

from .config import Settings
from .models import (
	BrowserDebugResponse,
	BrowserDownload,
	BrowserJobLog,
	BrowserJobLogsResponse,
	BrowserTab,
	DebugJobResponse,
	DebugSessionResponse,
	JobResponse,
	JobResult,
	JobStatus,
	StartJobRequest,
)
from .runner import BrowserTabUnavailable
from .store import BrowserStore


DEBUG_LIVE_TABS_BUDGET_SECONDS = 2.0


class BrowserRunner(Protocol):
	async def run(self, session_id: str, job_id: str, request: StartJobRequest) -> JobResult: ...


TAB_LOCK_TIMEOUT_SECONDS = 2.0
TAB_OP_TIMEOUT_SECONDS = 25.0
LOGGER = logging.getLogger("browser-use.jobs")


def _session_ref(session_id: str) -> str:
	return hashlib.sha256(session_id.encode("utf-8")).hexdigest()[:12]


@dataclass
class _Job:
	id: str
	session_id: str
	request: StartJobRequest
	status: JobStatus = JobStatus.QUEUED
	created_at: datetime = field(default_factory=lambda: datetime.now(UTC))
	started_at: datetime | None = None
	completed_at: datetime | None = None
	result: JobResult | None = None
	error: str = ""
	task: asyncio.Task[None] | None = None
	changed: asyncio.Condition = field(default_factory=asyncio.Condition)

	def response(self) -> JobResponse:
		return JobResponse(
			id=self.id,
			status=self.status,
			created_at=self.created_at,
			started_at=self.started_at,
			completed_at=self.completed_at,
			result=self.result,
			error=self.error,
		)


class JobManager:
	def __init__(self, settings: Settings, runner: BrowserRunner):
		self.settings = settings
		self.runner = runner
		self._jobs: dict[str, _Job] = {}
		self._jobs_lock = asyncio.Lock()
		self._semaphore = asyncio.Semaphore(settings.max_concurrent_jobs)
		self._session_locks: dict[str, asyncio.Lock] = {}
		self._active_job_ids: dict[str, str] = {}
		self._store = BrowserStore(
			settings.data_dir / "browser.db",
			max_jobs=settings.db_max_jobs,
			max_logs_per_job=settings.db_max_logs_per_job,
			job_retention_seconds=settings.db_job_retention_seconds,
		)

	def _log(self, job: _Job, level: str, message: str) -> None:
		self._store.append_log(job.id, level, message)

	def _persist_job(self, job: _Job) -> None:
		self._store.upsert_job(
			job.id,
			job.session_id,
			job.request.task,
			job.status,
			created_at=job.created_at,
			started_at=job.started_at,
			completed_at=job.completed_at,
			result=job.result,
			error=job.error,
		)

	async def create(self, session_id: str, request: StartJobRequest) -> JobResponse:
		async with self._jobs_lock:
			self._prune_locked()
			if len(self._jobs) >= self.settings.max_jobs:
				raise HTTPException(
					status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
					detail="browser job capacity reached; retry after completed jobs expire",
				)
			job = _Job(id=str(uuid4()), session_id=session_id, request=request)
			self._jobs[job.id] = job
			self._session_locks.setdefault(session_id, asyncio.Lock())
			self._persist_job(job)
			self._log(job, "info", f"job queued for session {session_id}")
			job.task = asyncio.create_task(self._execute(job), name=f"browser-use:{job.id}")
			return job.response()

	async def get(self, session_id: str, job_id: str, wait_seconds: float = 0) -> JobResponse:
		job = await self._owned_job(session_id, job_id)
		if wait_seconds > 0 and not job.status.terminal:
			deadline = asyncio.get_running_loop().time() + wait_seconds
			async with job.changed:
				while not job.status.terminal:
					remaining = deadline - asyncio.get_running_loop().time()
					if remaining <= 0:
						break
					try:
						await asyncio.wait_for(job.changed.wait(), timeout=remaining)
					except TimeoutError:
						break
		return self._response(job)

	async def cancel(self, session_id: str, job_id: str) -> JobResponse:
		job = await self._owned_job(session_id, job_id)
		task = job.task
		if not job.status.terminal and task is not None:
			task.cancel()
			try:
				await task
			except asyncio.CancelledError:
				pass
			# A task cancelled before its coroutine gets its first event-loop turn
			# never reaches _execute's CancelledError handler.
			if not job.status.terminal:
				await self._transition(job, JobStatus.CANCELLED)
		return self._response(job)

	async def screenshot(self, session_id: str, job_id: str) -> bytes:
		job = await self._owned_job(session_id, job_id)
		reader = getattr(self.runner, "read_screenshot", None)
		if reader is None:
			raise HTTPException(
				status_code=status.HTTP_501_NOT_IMPLEMENTED,
				detail="browser runner does not support screenshots",
			)
		try:
			return reader(job.id)
		except FileNotFoundError:
			raise HTTPException(
				status_code=status.HTTP_404_NOT_FOUND,
				detail="no screenshot is available for this browser job yet",
			) from None
		except ValueError as exc:
			raise HTTPException(status_code=status.HTTP_413_REQUEST_ENTITY_TOO_LARGE, detail=str(exc)) from exc

	async def downloads(self, session_id: str, job_id: str) -> list[BrowserDownload]:
		job = await self._owned_job(session_id, job_id)
		lister = getattr(self.runner, "list_downloads", None)
		if lister is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support downloads")
		return lister(job.id)

	async def download(self, session_id: str, job_id: str, name: str) -> tuple[BrowserDownload, bytes]:
		job = await self._owned_job(session_id, job_id)
		reader = getattr(self.runner, "read_download", None)
		if reader is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support downloads")
		try:
			return reader(job.id, name)
		except FileNotFoundError:
			raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser download not found") from None
		except ValueError as exc:
			raise HTTPException(status_code=status.HTTP_413_REQUEST_ENTITY_TOO_LARGE, detail=str(exc)) from exc

	def session_busy(self, session_id: str) -> tuple[bool, str]:
		job_id = self._active_job_ids.get(session_id, "")
		return bool(job_id), job_id

	async def tabs(self, session_id: str) -> list[BrowserTab]:
		lister = getattr(self.runner, "tabs", None)
		if lister is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support tabs")
		return await self._with_tab_lock(session_id, lambda: lister(session_id), "tabs")

	async def open_tab(self, session_id: str, url: str) -> list[BrowserTab]:
		opener = getattr(self.runner, "open_tab", None)
		if opener is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support opening tabs")
		tabs = await self._with_tab_lock(session_id, lambda: opener(session_id, url), "open_tab")
		self._store.touch_session(session_id)
		LOGGER.info("tab opened session=%s tabs=%d", _session_ref(session_id), len(tabs))
		return tabs

	async def tab_screenshot(self, session_id: str, tab_id: str) -> bytes:
		reader = getattr(self.runner, "tab_screenshot", None)
		if reader is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support tab screenshots")
		async def capture() -> bytes:
			try:
				return await reader(session_id, tab_id)
			except KeyError:
				raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser tab not found") from None
			except BrowserTabUnavailable as exc:
				raise HTTPException(status_code=status.HTTP_503_SERVICE_UNAVAILABLE, detail=str(exc)) from None
			except FileNotFoundError:
				raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser tab screenshot is not available") from None
			except ValueError as exc:
				raise HTTPException(status_code=status.HTTP_413_REQUEST_ENTITY_TOO_LARGE, detail=str(exc)) from exc

		return await self._with_tab_lock(session_id, capture, "tab_screenshot")

	async def inspect_tab(self, session_id: str, tab_id: str):
		inspector = getattr(self.runner, "inspect_tab", None)
		if inspector is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support tab inspection")
		async def inspect():
			try:
				return await inspector(session_id, tab_id)
			except KeyError:
				raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser tab not found") from None
			except BrowserTabUnavailable as exc:
				raise HTTPException(status_code=status.HTTP_503_SERVICE_UNAVAILABLE, detail=str(exc)) from None

		return await self._with_tab_lock(session_id, inspect, "inspect_tab")

	async def tab_history(self, session_id: str, tab_id: str):
		reader = getattr(self.runner, "tab_history", None)
		if reader is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support tab history")

		async def read():
			try:
				return await reader(session_id, tab_id)
			except KeyError:
				raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser tab not found") from None
			except BrowserTabUnavailable as exc:
				raise HTTPException(status_code=status.HTTP_503_SERVICE_UNAVAILABLE, detail=str(exc)) from None

		return await self._with_tab_lock(session_id, read, "tab_history")

	async def act_on_tab(self, session_id: str, tab_id: str, request):
		actor = getattr(self.runner, "act_on_tab", None)
		if actor is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support tab actions")
		async def act():
			try:
				return await actor(session_id, tab_id, request)
			except KeyError:
				raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser tab not found") from None
			except BrowserTabUnavailable as exc:
				raise HTTPException(status_code=status.HTTP_503_SERVICE_UNAVAILABLE, detail=str(exc)) from None
			except ValueError as exc:
				raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(exc)) from exc

		return await self._with_tab_lock(session_id, act, "act_on_tab")

	async def close_tab(self, session_id: str, tab_id: str) -> list[BrowserTab]:
		closer = getattr(self.runner, "close_tab", None)
		if closer is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support tabs")
		async def close():
			try:
				return await closer(session_id, tab_id)
			except KeyError:
				raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser tab not found") from None
			except BrowserTabUnavailable as exc:
				raise HTTPException(status_code=status.HTTP_503_SERVICE_UNAVAILABLE, detail=str(exc)) from None

		return await self._with_tab_lock(session_id, close, "close_tab")

	def viewport(self, session_id: str):
		reader = getattr(self.runner, "viewport_state", None)
		if reader is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support viewport quality")
		return reader(session_id)

	async def set_viewport(self, session_id: str, quality: str):
		setter = getattr(self.runner, "set_viewport", None)
		if setter is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support viewport quality")
		async def apply():
			try:
				return await setter(session_id, quality)
			except ValueError as exc:
				raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(exc)) from exc
			except BrowserTabUnavailable as exc:
				raise HTTPException(status_code=status.HTTP_503_SERVICE_UNAVAILABLE, detail=str(exc)) from None

		return await self._with_tab_lock(session_id, apply, "set_viewport")

	async def debug(self, job_limit: int, load_limit: int, session_limit: int = 50) -> BrowserDebugResponse:
		async with self._jobs_lock:
			jobs = sorted(self._jobs.values(), key=lambda item: item.created_at, reverse=True)[:job_limit]
			total_jobs = len(self._jobs)
			running_jobs = sum(1 for item in self._jobs.values() if not item.status.terminal)

		debug_jobs: list[DebugJobResponse] = []
		load_reader = getattr(self.runner, "network_loads", None)
		for job in jobs:
			load_count, loads = load_reader(job.id, load_limit) if load_reader is not None else (0, [])
			response = self._response(job)
			debug_jobs.append(
				DebugJobResponse(
					**response.model_dump(),
					session_id=job.session_id,
					task=_truncate(job.request.task, 2_000),
					load_count=load_count,
					loads=loads,
				)
			)

		sessions = await self._debug_sessions(session_limit)
		live_sessions = sum(1 for item in sessions if item.persistent)
		total_tabs = sum(item.tab_count for item in sessions)
		return BrowserDebugResponse(
			total_jobs=total_jobs,
			running_jobs=running_jobs,
			max_jobs=self.settings.max_jobs,
			max_concurrent_jobs=self.settings.max_concurrent_jobs,
			live_sessions=live_sessions,
			total_tabs=total_tabs,
			jobs=debug_jobs,
			sessions=sessions,
		)

	async def debug_session(self, session_id: str) -> DebugSessionResponse:
		sessions = await self._debug_sessions(1_000)
		for session in sessions:
			if session.session_id == session_id:
				return session
		raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser session not found")

	async def job_logs(self, job_id: str, limit: int) -> BrowserJobLogsResponse:
		async with self._jobs_lock:
			job = self._jobs.get(job_id)
		if job is None:
			# Allow logs for pruned in-memory jobs that remain in SQLite.
			rows = self._store.get_job_logs(job_id, limit)
			if not rows:
				raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser job not found")
			return BrowserJobLogsResponse(
				job_id=job_id,
				logs=[
					BrowserJobLog(id=row.id, level=row.level, message=row.message, created_at=row.created_at)
					for row in rows
				],
			)
		rows = self._store.get_job_logs(job_id, limit)
		return BrowserJobLogsResponse(
			job_id=job_id,
			logs=[
				BrowserJobLog(id=row.id, level=row.level, message=row.message, created_at=row.created_at)
				for row in rows
			],
		)

	async def admin_cancel(self, job_id: str) -> JobResponse:
		async with self._jobs_lock:
			job = self._jobs.get(job_id)
		if job is None:
			raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser job not found")
		return await self.cancel(job.session_id, job_id)

	async def kill_session(self, session_id: str) -> DebugSessionResponse:
		killer = getattr(self.runner, "kill_session", None)
		if killer is None:
			raise HTTPException(
				status_code=status.HTTP_501_NOT_IMPLEMENTED,
				detail="browser runner does not support killing sessions",
			)
		await self._with_tab_lock(session_id, lambda: killer(session_id), "kill_session")
		self._store.touch_session(session_id)
		self._store.append_log("session:" + session_id, "warn", "operator killed persistent browser session")
		return await self.debug_session(session_id)

	async def admin_close_tab(self, session_id: str, tab_id: str) -> list[BrowserTab]:
		tabs = await self.close_tab(session_id, tab_id)
		self._store.append_log("session:" + session_id, "info", f"operator closed tab {tab_id}")
		return tabs

	async def _debug_sessions(self, limit: int) -> list[DebugSessionResponse]:
		limit = max(1, min(limit, 1_000))
		lister = getattr(self.runner, "list_sessions", None)
		live_ids: set[str] = set()
		live_tabs: dict[str, list[BrowserTab]] = {}
		if lister is not None:
			# Live tab listing talks to Chromium. If CDP is wedged, the debug
			# page must still return jobs instead of waiting until the HTTP
			# client deadline.
			deadline = asyncio.get_running_loop().time() + DEBUG_LIVE_TABS_BUDGET_SECONDS
			for session_id in lister():
				live_ids.add(session_id)
				tab_lister = getattr(self.runner, "tabs", None)
				if tab_lister is None:
					live_tabs[session_id] = []
					continue
				remaining = deadline - asyncio.get_running_loop().time()
				if remaining <= 0:
					LOGGER.warning("debug live tabs budget exhausted session=%s", _session_ref(session_id))
					reader = getattr(self.runner, "persisted_tabs", None)
					live_tabs[session_id] = list(reader(session_id) or []) if reader is not None else []
					continue
				try:
					live_tabs[session_id] = await asyncio.wait_for(tab_lister(session_id), timeout=remaining)
				except Exception as exc:
					LOGGER.warning(
						"debug live tabs failed session=%s err=%s",
						_session_ref(session_id),
						type(exc).__name__,
					)
					reader = getattr(self.runner, "persisted_tabs", None)
					live_tabs[session_id] = list(reader(session_id) or []) if reader is not None else []

		async with self._jobs_lock:
			active_by_session: dict[str, int] = {}
			for job in self._jobs.values():
				if not job.status.terminal:
					active_by_session[job.session_id] = active_by_session.get(job.session_id, 0) + 1

		seen: set[str] = set()
		sessions: list[DebugSessionResponse] = []
		for session_id in sorted(live_ids):
			seen.add(session_id)
			tabs = live_tabs.get(session_id, [])
			sessions.append(
				DebugSessionResponse(
					session_id=session_id,
					persistent=True,
					tab_count=len(tabs),
					tabs=tabs,
					active_jobs=active_by_session.get(session_id, 0),
					total_jobs=self._store.count_jobs_for_session(session_id),
					last_activity=datetime.now(UTC),
				)
			)

		persisted = getattr(self.runner, "list_persisted_tab_sessions", None)
		if persisted is not None:
			for session_id, tabs in persisted().items():
				if session_id in seen:
					continue
				seen.add(session_id)
				sessions.append(
					DebugSessionResponse(
						session_id=session_id,
						persistent=False,
						tab_count=len(tabs),
						tabs=tabs,
						active_jobs=active_by_session.get(session_id, 0),
						total_jobs=self._store.count_jobs_for_session(session_id),
						last_activity=datetime.now(UTC),
					)
				)

		for stats in self._store.list_session_stats(limit):
			if stats.session_id in seen:
				continue
			seen.add(stats.session_id)
			sessions.append(
				DebugSessionResponse(
					session_id=stats.session_id,
					persistent=False,
					tab_count=0,
					tabs=[],
					active_jobs=active_by_session.get(stats.session_id, 0),
					total_jobs=stats.job_count,
					last_activity=stats.last_activity,
				)
			)
			if len(sessions) >= limit:
				break

		sessions.sort(
			key=lambda item: (
				0 if item.persistent else 1,
				-(item.last_activity.timestamp() if item.last_activity else 0),
			),
		)
		return sessions[:limit]

	async def shutdown(self) -> None:
		async with self._jobs_lock:
			tasks = [job.task for job in self._jobs.values() if job.task and not job.task.done()]
		for task in tasks:
			task.cancel()
		if tasks:
			await asyncio.gather(*tasks, return_exceptions=True)
		shutdown_runner = getattr(self.runner, "shutdown", None)
		if shutdown_runner is not None:
			await shutdown_runner()

	async def _execute(self, job: _Job) -> None:
		try:
			async with self._semaphore:
				session_lock = self._session_locks[job.session_id]
				await session_lock.acquire()
				self._active_job_ids[job.session_id] = job.id
				try:
					await self._transition(job, JobStatus.RUNNING)
					self._log(job, "info", "job started")
					job.result = await asyncio.wait_for(
						self.runner.run(job.session_id, job.id, job.request),
						timeout=self.settings.job_timeout_seconds,
					)
					await self._transition(job, JobStatus.SUCCEEDED)
					self._log(job, "info", f"job succeeded in {job.result.steps} steps")
					if job.result.action_names:
						self._log(job, "info", "actions: " + ", ".join(job.result.action_names[:20]))
				finally:
					self._active_job_ids.pop(job.session_id, None)
					session_lock.release()
		except asyncio.CancelledError:
			await self._transition(job, JobStatus.CANCELLED)
			self._log(job, "warn", "job cancelled")
			raise
		except TimeoutError:
			job.error = f"browser job exceeded {self.settings.job_timeout_seconds} second timeout"
			await self._transition(job, JobStatus.FAILED)
			self._log(job, "error", job.error)
		except Exception as exc:
			job.error = _safe_error(exc)
			await self._transition(job, JobStatus.FAILED)
			self._log(job, "error", job.error)

	async def _transition(self, job: _Job, new_status: JobStatus) -> None:
		async with job.changed:
			job.status = new_status
			now = datetime.now(UTC)
			if new_status == JobStatus.RUNNING:
				job.started_at = now
			if new_status.terminal:
				job.completed_at = now
			job.changed.notify_all()
		self._persist_job(job)

	async def _owned_job(self, session_id: str, job_id: str) -> _Job:
		async with self._jobs_lock:
			job = self._jobs.get(job_id)
		if job is None:
			raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser job not found")
		if job.session_id != session_id:
			# Do not reveal whether another session owns the requested identifier.
			raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser job not found")
		return job

	async def _session_lock(self, session_id: str) -> asyncio.Lock:
		async with self._jobs_lock:
			return self._session_locks.setdefault(session_id, asyncio.Lock())

	async def _with_tab_lock(self, session_id: str, operation, operation_name: str = "tab_operation"):
		lock = await self._session_lock(session_id)
		try:
			await asyncio.wait_for(lock.acquire(), timeout=TAB_LOCK_TIMEOUT_SECONDS)
		except TimeoutError as exc:
			job_id = self._active_job_ids.get(session_id, "")
			LOGGER.warning("tab_lock_busy operation=%s session=%s active_job=%s", operation_name, _session_ref(session_id), job_id or "none")
			detail = f"browser session busy: autonomous job {job_id} running" if job_id else "browser session busy"
			raise HTTPException(
				status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
				detail=detail,
				headers={"X-Browser-Session-Busy": "1"},
			) from exc
		try:
			return await asyncio.wait_for(operation(), timeout=TAB_OP_TIMEOUT_SECONDS)
		except TimeoutError as exc:
			LOGGER.warning("tab_operation_timeout operation=%s session=%s", operation_name, _session_ref(session_id))
			raise HTTPException(
				status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
				detail="browser tab operation timed out; retry",
			) from exc
		finally:
			lock.release()

	def _response(self, job: _Job) -> JobResponse:
		response = job.response()
		checker = getattr(self.runner, "screenshot_available", None)
		if checker is not None:
			response.screenshot_available = bool(checker(job.id))
		return response

	def _prune_locked(self) -> None:
		cutoff = datetime.now(UTC) - timedelta(seconds=self.settings.job_ttl_seconds)
		expired = [
			job_id
			for job_id, job in self._jobs.items()
			if job.status.terminal and job.completed_at is not None and job.completed_at < cutoff
		]
		for job_id in expired:
			del self._jobs[job_id]
			cleanup = getattr(self.runner, "cleanup", None)
			if cleanup is not None:
				cleanup(job_id)
		active_sessions = {job.session_id for job in self._jobs.values()}
		has_session = getattr(self.runner, "has_session", None)
		for session_id in list(self._session_locks):
			persistent = bool(has_session(session_id)) if has_session is not None else False
			if session_id not in active_sessions and not persistent:
				del self._session_locks[session_id]


def _safe_error(error: Exception) -> str:
	message = f"{type(error).__name__}: {error}".strip()
	if len(message) > 4_000:
		return message[:4_000] + "..."
	return message


def _truncate(value: str, limit: int) -> str:
	return value if len(value) <= limit else value[:limit] + "..."
