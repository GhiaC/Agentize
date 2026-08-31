from __future__ import annotations

import base64
import binascii
from datetime import datetime
from enum import StrEnum
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit, urlunsplit

from pydantic import BaseModel, ConfigDict, Field, field_validator


class JobStatus(StrEnum):
	QUEUED = "queued"
	RUNNING = "running"
	SUCCEEDED = "succeeded"
	FAILED = "failed"
	CANCELLED = "cancelled"

	@property
	def terminal(self) -> bool:
		return self in {self.SUCCEEDED, self.FAILED, self.CANCELLED}


class BrowserUpload(BaseModel):
	name: str = Field(min_length=1, max_length=255)
	mime_type: str = Field(default="application/octet-stream", max_length=255)
	data_base64: str = Field(min_length=1)

	@field_validator("name")
	@classmethod
	def safe_name(cls, value: str) -> str:
		value = value.strip()
		if not value or Path(value).name != value or value in {".", ".."}:
			raise ValueError("upload name must be a filename")
		return value

	@field_validator("data_base64")
	@classmethod
	def valid_data(cls, value: str) -> str:
		try:
			data = base64.b64decode(value, validate=True)
		except (ValueError, binascii.Error) as error:
			raise ValueError("upload data must be valid base64") from error
		if not data or len(data) > 10 << 20:
			raise ValueError("upload data must be between 1 byte and 10485760 bytes")
		return value


class BrowserTab(BaseModel):
	"""Safe metadata for one tab in a persistent browser session."""

	id: str = Field(min_length=1, max_length=255)
	url: str = Field(default="", max_length=2_000)
	title: str = Field(default="", max_length=500)
	active: bool = False


class OpenBrowserTabRequest(BaseModel):
	"""A direct, non-LLM navigation request for one persistent session."""

	model_config = ConfigDict(extra="forbid")
	url: str = Field(min_length=1, max_length=2_000)

	@field_validator("url")
	@classmethod
	def safe_web_url(cls, value: str) -> str:
		value = value.strip()
		if "://" not in value:
			value = "https://" + value
		parsed = urlsplit(value)
		if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password:
			raise ValueError("url must be an HTTP or HTTPS address without credentials")
		return urlunsplit((parsed.scheme, parsed.netloc, parsed.path, parsed.query, ""))


class BrowserTabElement(BaseModel):
	"""A safe, visible control exposed to the conversation-owned browser."""

	selector: str = Field(min_length=1, max_length=2_000)
	role: str = Field(default="", max_length=80)
	text: str = Field(default="", max_length=500)
	label: str = Field(default="", max_length=500)


class BrowserTabInspectionResponse(BaseModel):
	tab: BrowserTab
	text: str = Field(default="", max_length=32_000)
	elements: list[BrowserTabElement] = Field(default_factory=list, max_length=100)


class BrowserTabActionRequest(BaseModel):
	"""One explicit, bounded interaction with a persistent browser tab."""

	model_config = ConfigDict(extra="forbid")
	action: str = Field(min_length=1, max_length=20)
	url: str = Field(default="", max_length=2_000)
	selector: str = Field(default="", max_length=2_000)
	text: str = Field(default="", max_length=16_384)
	key: str = Field(default="", max_length=64)
	amount: int = Field(default=0, ge=-10_000, le=10_000)

	@field_validator("action")
	@classmethod
	def supported_action(cls, value: str) -> str:
		value = value.strip().lower()
		if value not in {"navigate", "click", "type", "press", "scroll", "wait", "back", "forward"}:
			raise ValueError("unsupported browser tab action")
		return value


class BrowserTabActionResponse(BaseModel):
	tab: BrowserTab
	result: str = Field(default="", max_length=4_000)
	tabs: list[BrowserTab] = Field(default_factory=list)
	navigation_urls: list[str] = Field(default_factory=list, max_length=200)
	navigation_index: int = -1


class BrowserTabHistoryResponse(BaseModel):
	tab: BrowserTab
	navigation_urls: list[str] = Field(default_factory=list, max_length=200)
	navigation_index: int = -1


class StartJobRequest(BaseModel):
	model_config = ConfigDict(extra="forbid")

	task: str = Field(min_length=1, max_length=20_000)
	allowed_domains: list[str] = Field(default_factory=list, max_length=100)
	max_steps: int | None = Field(default=None, ge=1, le=500)
	use_vision: bool | None = None
	uploads: list[BrowserUpload] = Field(default_factory=list, max_length=10)

	@field_validator("task")
	@classmethod
	def normalize_task(cls, value: str) -> str:
		value = value.strip()
		if not value:
			raise ValueError("task cannot be blank")
		return value

	@field_validator("allowed_domains")
	@classmethod
	def normalize_domains(cls, values: list[str]) -> list[str]:
		result: list[str] = []
		for value in values:
			value = value.strip()
			if not value or len(value) > 255:
				raise ValueError("allowed domain entries must be 1-255 characters")
			if value not in result:
				result.append(value)
		return result


class JobResult(BaseModel):
	final_result: str = ""
	done: bool
	successful: bool | None = None
	visited_urls: list[str] = Field(default_factory=list)
	steps: int
	duration_seconds: float
	action_names: list[str] = Field(default_factory=list)
	actions: list[dict[str, Any]] = Field(default_factory=list)
	errors: list[str] = Field(default_factory=list)
	tabs: list[BrowserTab] = Field(default_factory=list)


class JobResponse(BaseModel):
	id: str
	status: JobStatus
	created_at: datetime
	started_at: datetime | None = None
	completed_at: datetime | None = None
	result: JobResult | None = None
	error: str = ""
	screenshot_available: bool = False


class BrowserLoad(BaseModel):
	started_at: datetime | None = None
	duration_ms: float = 0
	method: str = "GET"
	url: str = ""
	status: int = 0
	status_text: str = ""
	mime_type: str = ""
	bytes: int = 0
	failed: bool = False


class BrowserDownload(BaseModel):
	name: str
	mime_type: str = "application/octet-stream"
	size: int = Field(ge=0)


class BrowserDownloadsResponse(BaseModel):
	files: list[BrowserDownload] = Field(default_factory=list)


class ViewportState(BaseModel):
	quality: str = Field(min_length=1, max_length=32)
	width: int = Field(ge=800, le=3840)
	height: int = Field(ge=600, le=2160)
	label: str = Field(default="", max_length=32)
	options: list[dict] = Field(default_factory=list)


class SetViewportRequest(BaseModel):
	model_config = ConfigDict(extra="forbid")
	quality: str = Field(min_length=1, max_length=32)


class BrowserTabsResponse(BaseModel):
	tabs: list[BrowserTab] = Field(default_factory=list)
	session_busy: bool = False
	active_job_id: str = ""
	viewport: ViewportState | None = None


class DebugJobResponse(JobResponse):
	session_id: str
	task: str
	load_count: int = 0
	loads: list[BrowserLoad] = Field(default_factory=list)


class BrowserJobLog(BaseModel):
	id: int
	level: str
	message: str
	created_at: datetime


class BrowserJobLogsResponse(BaseModel):
	job_id: str
	logs: list[BrowserJobLog] = Field(default_factory=list)


class DebugSessionResponse(BaseModel):
	session_id: str
	persistent: bool = False
	tab_count: int = 0
	tabs: list[BrowserTab] = Field(default_factory=list)
	active_jobs: int = 0
	total_jobs: int = 0
	last_activity: datetime | None = None


class BrowserDebugResponse(BaseModel):
	total_jobs: int
	running_jobs: int
	max_jobs: int
	max_concurrent_jobs: int
	live_sessions: int = 0
	total_tabs: int = 0
	jobs: list[DebugJobResponse] = Field(default_factory=list)
	sessions: list[DebugSessionResponse] = Field(default_factory=list)


class HealthResponse(BaseModel):
	status: str = "ok"
	component: str = "agentize-browser-use"
