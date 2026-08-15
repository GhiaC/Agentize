from __future__ import annotations

import unittest
from datetime import UTC, datetime
from pathlib import Path
from tempfile import TemporaryDirectory

from app.models import JobResult, JobStatus
from app.store import BrowserStore


class BrowserStoreTests(unittest.TestCase):
	def test_job_logs_and_prune(self):
		with TemporaryDirectory() as directory:
			store = BrowserStore(Path(directory) / "browser.db", max_jobs=2, max_logs_per_job=3)
			created = datetime(2026, 1, 1, tzinfo=UTC)
			store.upsert_job(
				"job-1",
				"session-1",
				"first task",
				JobStatus.QUEUED,
				created_at=created,
			)
			store.append_log("job-1", "info", "queued")
			store.append_log("job-1", "info", "started")
			store.append_log("job-1", "error", "failed hard")
			store.append_log("job-1", "info", "overflow")
			logs = store.get_job_logs("job-1", 10)
			self.assertEqual(len(logs), 3)
			self.assertEqual(logs[0].message, "overflow")

			store.upsert_job(
				"job-2",
				"session-2",
				"second",
				JobStatus.SUCCEEDED,
				created_at=created,
				completed_at=created,
				result=JobResult(done=True, steps=1, duration_seconds=0.1),
			)
			store.upsert_job(
				"job-3",
				"session-3",
				"third",
				JobStatus.SUCCEEDED,
				created_at=created,
				completed_at=created,
				result=JobResult(done=True, steps=1, duration_seconds=0.1),
			)
			stats = store.list_session_stats(10)
			self.assertGreaterEqual(len(stats), 2)
