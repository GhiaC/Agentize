from __future__ import annotations

import json
import sqlite3
import threading
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

from .models import JobResult, JobStatus


@dataclass(frozen=True)
class StoredJobLog:
	id: int
	job_id: str
	level: str
	message: str
	created_at: datetime


@dataclass(frozen=True)
class StoredSessionStats:
	session_id: str
	job_count: int
	last_activity: datetime | None


class BrowserStore:
	"""SQLite persistence for browser jobs, debug logs, and session statistics."""

	def __init__(
		self,
		db_path: Path,
		*,
		max_jobs: int = 5_000,
		max_logs_per_job: int = 500,
		job_retention_seconds: int = 604_800,
	):
		self._db_path = db_path
		self._max_jobs = max(100, max_jobs)
		self._max_logs_per_job = max(1, max_logs_per_job)
		self._job_retention_seconds = max(3_600, job_retention_seconds)
		self._lock = threading.Lock()
		db_path.parent.mkdir(parents=True, exist_ok=True)
		self._init_schema()

	def _connect(self) -> sqlite3.Connection:
		connection = sqlite3.connect(self._db_path, timeout=30, check_same_thread=False)
		connection.row_factory = sqlite3.Row
		return connection

	def _init_schema(self) -> None:
		with self._connect() as connection:
			connection.executescript(
				"""
				PRAGMA journal_mode=WAL;
				PRAGMA synchronous=NORMAL;

				CREATE TABLE IF NOT EXISTS browser_jobs (
					id TEXT PRIMARY KEY,
					session_id TEXT NOT NULL,
					task TEXT NOT NULL,
					status TEXT NOT NULL,
					created_at TEXT NOT NULL,
					started_at TEXT,
					completed_at TEXT,
					result_json TEXT,
					error TEXT NOT NULL DEFAULT ''
				);
				CREATE INDEX IF NOT EXISTS idx_browser_jobs_session
					ON browser_jobs(session_id, created_at DESC);
				CREATE INDEX IF NOT EXISTS idx_browser_jobs_created
					ON browser_jobs(created_at DESC);

				CREATE TABLE IF NOT EXISTS browser_job_logs (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					job_id TEXT NOT NULL,
					level TEXT NOT NULL,
					message TEXT NOT NULL,
					created_at TEXT NOT NULL
				);
				CREATE INDEX IF NOT EXISTS idx_browser_job_logs_job
					ON browser_job_logs(job_id, id DESC);

				CREATE TABLE IF NOT EXISTS browser_session_stats (
					session_id TEXT PRIMARY KEY,
					job_count INTEGER NOT NULL DEFAULT 0,
					last_activity TEXT
				);
				"""
			)

	def upsert_job(
		self,
		job_id: str,
		session_id: str,
		task: str,
		status: JobStatus,
		*,
		created_at: datetime,
		started_at: datetime | None = None,
		completed_at: datetime | None = None,
		result: JobResult | None = None,
		error: str = "",
	) -> None:
		result_json = json.dumps(result.model_dump()) if result is not None else None
		with self._lock, self._connect() as connection:
			connection.execute(
				"""
				INSERT INTO browser_jobs (
					id, session_id, task, status, created_at, started_at, completed_at, result_json, error
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET
					status = excluded.status,
					started_at = COALESCE(excluded.started_at, browser_jobs.started_at),
					completed_at = COALESCE(excluded.completed_at, browser_jobs.completed_at),
					result_json = COALESCE(excluded.result_json, browser_jobs.result_json),
					error = CASE WHEN excluded.error != '' THEN excluded.error ELSE browser_jobs.error END
				""",
				(
					job_id,
					session_id,
					task,
					status.value,
					_iso(created_at),
					_iso(started_at),
					_iso(completed_at),
					result_json,
					error[:4_000],
				),
			)
			connection.execute(
				"""
				INSERT INTO browser_session_stats (session_id, job_count, last_activity)
				VALUES (?, 1, ?)
				ON CONFLICT(session_id) DO UPDATE SET
					job_count = browser_session_stats.job_count + 1,
					last_activity = excluded.last_activity
				""",
				(session_id, _iso(datetime.now(UTC))),
			)
			connection.commit()
		self._prune()

	def append_log(self, job_id: str, level: str, message: str) -> None:
		level = (level or "info").strip().lower()[:16] or "info"
		message = (message or "").strip()
		if not message:
			return
		if len(message) > 4_000:
			message = message[:4_000] + "..."
		with self._lock, self._connect() as connection:
			connection.execute(
				"""
				INSERT INTO browser_job_logs (job_id, level, message, created_at)
				VALUES (?, ?, ?, ?)
				""",
				(job_id, level, message, _iso(datetime.now(UTC))),
			)
			connection.execute(
				"""
				DELETE FROM browser_job_logs
				WHERE job_id = ?
				AND id NOT IN (
					SELECT id FROM (
						SELECT id FROM browser_job_logs
						WHERE job_id = ?
						ORDER BY id DESC
						LIMIT ?
					)
				)
				""",
				(job_id, job_id, self._max_logs_per_job),
			)
			connection.commit()

	def get_job_logs(self, job_id: str, limit: int) -> list[StoredJobLog]:
		limit = max(1, min(limit, self._max_logs_per_job))
		with self._connect() as connection:
			rows = connection.execute(
				"""
				SELECT id, job_id, level, message, created_at
				FROM browser_job_logs
				WHERE job_id = ?
				ORDER BY id DESC
				LIMIT ?
				""",
				(job_id, limit),
			).fetchall()
		return [
			StoredJobLog(
				id=int(row["id"]),
				job_id=str(row["job_id"]),
				level=str(row["level"]),
				message=str(row["message"]),
				created_at=_parse_iso(str(row["created_at"])),
			)
			for row in rows
		]

	def list_session_stats(self, limit: int) -> list[StoredSessionStats]:
		limit = max(1, min(limit, 1_000))
		with self._connect() as connection:
			rows = connection.execute(
				"""
				SELECT session_id, job_count, last_activity
				FROM browser_session_stats
				ORDER BY COALESCE(last_activity, '') DESC
				LIMIT ?
				""",
				(limit,),
			).fetchall()
		return [
			StoredSessionStats(
				session_id=str(row["session_id"]),
				job_count=int(row["job_count"]),
				last_activity=_parse_iso_optional(row["last_activity"]),
			)
			for row in rows
		]

	def count_jobs_for_session(self, session_id: str) -> int:
		with self._connect() as connection:
			row = connection.execute(
				"SELECT COUNT(*) AS total FROM browser_jobs WHERE session_id = ?",
				(session_id,),
			).fetchone()
		return int(row["total"]) if row else 0

	def touch_session(self, session_id: str) -> None:
		with self._lock, self._connect() as connection:
			connection.execute(
				"""
				INSERT INTO browser_session_stats (session_id, job_count, last_activity)
				VALUES (?, 0, ?)
				ON CONFLICT(session_id) DO UPDATE SET last_activity = excluded.last_activity
				""",
				(session_id, _iso(datetime.now(UTC))),
			)
			connection.commit()

	def _prune(self) -> None:
		cutoff = datetime.now(UTC) - timedelta(seconds=self._job_retention_seconds)
		with self._lock, self._connect() as connection:
			connection.execute(
				"""
				DELETE FROM browser_job_logs
				WHERE job_id IN (
					SELECT id FROM browser_jobs
					WHERE completed_at IS NOT NULL AND completed_at < ?
				)
				""",
				(_iso(cutoff),),
			)
			connection.execute(
				"""
				DELETE FROM browser_jobs
				WHERE completed_at IS NOT NULL AND completed_at < ?
				""",
				(_iso(cutoff),),
			)
			row = connection.execute("SELECT COUNT(*) AS total FROM browser_jobs").fetchone()
			total = int(row["total"]) if row else 0
			if total > self._max_jobs:
				overflow = total - self._max_jobs
				connection.execute(
					"""
					DELETE FROM browser_job_logs
					WHERE job_id IN (
						SELECT id FROM browser_jobs
						ORDER BY created_at ASC
						LIMIT ?
					)
					""",
					(overflow,),
				)
				connection.execute(
					"""
					DELETE FROM browser_jobs
					WHERE id IN (
						SELECT id FROM browser_jobs
						ORDER BY created_at ASC
						LIMIT ?
					)
					""",
					(overflow,),
				)
			connection.commit()


def _iso(value: datetime | None) -> str | None:
	if value is None:
		return None
	if value.tzinfo is None:
		value = value.replace(tzinfo=UTC)
	return value.astimezone(UTC).isoformat()


def _parse_iso(value: str) -> datetime:
	parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
	if parsed.tzinfo is None:
		parsed = parsed.replace(tzinfo=UTC)
	return parsed.astimezone(UTC)


def _parse_iso_optional(value: Any) -> datetime | None:
	if value is None or value == "":
		return None
	return _parse_iso(str(value))
