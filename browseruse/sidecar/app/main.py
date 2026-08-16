from __future__ import annotations

import hmac
from contextlib import asynccontextmanager

from fastapi import Depends, FastAPI, Header, HTTPException, Query, Response, status
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer

from .config import Settings
from .jobs import JobManager
from .models import BrowserDebugResponse, BrowserDownloadsResponse, BrowserJobLogsResponse, BrowserTabActionRequest, BrowserTabActionResponse, BrowserTabInspectionResponse, BrowserTabsResponse, DebugSessionResponse, HealthResponse, JobResponse, OpenBrowserTabRequest, SetViewportRequest, StartJobRequest, ViewportState
from .runner import BrowserUseRunner


settings = Settings.from_environment()
manager = JobManager(settings, BrowserUseRunner(settings))
bearer = HTTPBearer(auto_error=False)


@asynccontextmanager
async def lifespan(_: FastAPI):
	settings.data_dir.mkdir(parents=True, exist_ok=True)
	yield
	await manager.shutdown()


app = FastAPI(
	title="Agentize browser-use sidecar",
	version="1.0.0",
	docs_url=None,
	redoc_url=None,
	openapi_url=None,
	lifespan=lifespan,
)


def require_auth(credentials: HTTPAuthorizationCredentials | None = Depends(bearer)) -> None:
	if credentials is None or credentials.scheme.lower() != "bearer":
		raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="missing bearer token")
	if not hmac.compare_digest(credentials.credentials, settings.service_token):
		raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="invalid bearer token")


def require_session(
	session_id: str = Header(alias="X-Agentize-Session-ID", min_length=1, max_length=256),
) -> str:
	return session_id


@app.get("/health", response_model=HealthResponse)
async def health() -> HealthResponse:
	return HealthResponse()


@app.post(
	"/v1/jobs",
	response_model=JobResponse,
	status_code=status.HTTP_202_ACCEPTED,
	dependencies=[Depends(require_auth)],
)
async def create_job(
	request: StartJobRequest,
	session_id: str = Depends(require_session),
) -> JobResponse:
	return await manager.create(session_id, request)


@app.get(
	"/v1/debug/jobs",
	response_model=BrowserDebugResponse,
	dependencies=[Depends(require_auth)],
)
async def debug_jobs(
	limit: int = Query(default=20, ge=1, le=100),
	load_limit: int = Query(default=50, ge=0, le=250),
	session_limit: int = Query(default=50, ge=1, le=100),
) -> BrowserDebugResponse:
	return await manager.debug(limit, load_limit, session_limit)


@app.get(
	"/v1/debug/jobs/{job_id}/logs",
	response_model=BrowserJobLogsResponse,
	dependencies=[Depends(require_auth)],
)
async def debug_job_logs(
	job_id: str,
	limit: int = Query(default=200, ge=1, le=500),
) -> BrowserJobLogsResponse:
	return await manager.job_logs(job_id, limit)


@app.post(
	"/v1/debug/jobs/{job_id}/cancel",
	response_model=JobResponse,
	dependencies=[Depends(require_auth)],
)
async def debug_cancel_job(job_id: str) -> JobResponse:
	return await manager.admin_cancel(job_id)


@app.get(
	"/v1/debug/sessions",
	response_model=list[DebugSessionResponse],
	dependencies=[Depends(require_auth)],
)
async def debug_sessions(
	limit: int = Query(default=50, ge=1, le=100),
) -> list[DebugSessionResponse]:
	snapshot = await manager.debug(job_limit=1, load_limit=0, session_limit=limit)
	return snapshot.sessions


@app.get(
	"/v1/debug/sessions/{session_id}",
	response_model=DebugSessionResponse,
	dependencies=[Depends(require_auth)],
)
async def debug_session(session_id: str) -> DebugSessionResponse:
	return await manager.debug_session(session_id)


@app.post(
	"/v1/debug/sessions/{session_id}/kill",
	response_model=DebugSessionResponse,
	dependencies=[Depends(require_auth)],
)
async def debug_kill_session(session_id: str) -> DebugSessionResponse:
	return await manager.kill_session(session_id)


@app.post(
	"/v1/debug/sessions/{session_id}/tabs/{tab_id}/close",
	response_model=BrowserTabsResponse,
	dependencies=[Depends(require_auth)],
)
async def debug_close_tab(session_id: str, tab_id: str) -> BrowserTabsResponse:
	return BrowserTabsResponse(tabs=await manager.admin_close_tab(session_id, tab_id))


@app.get(
	"/v1/jobs/{job_id}",
	response_model=JobResponse,
	dependencies=[Depends(require_auth)],
)
async def get_job(
	job_id: str,
	wait_seconds: float = Query(default=0, ge=0, le=60),
	session_id: str = Depends(require_session),
) -> JobResponse:
	return await manager.get(session_id, job_id, wait_seconds)


@app.post(
	"/v1/jobs/{job_id}/cancel",
	response_model=JobResponse,
	dependencies=[Depends(require_auth)],
)
async def cancel_job(
	job_id: str,
	session_id: str = Depends(require_session),
) -> JobResponse:
	return await manager.cancel(session_id, job_id)


@app.get(
	"/v1/jobs/{job_id}/screenshot",
	dependencies=[Depends(require_auth)],
	response_class=Response,
)
async def get_job_screenshot(
	job_id: str,
	session_id: str = Depends(require_session),
) -> Response:
	data = await manager.screenshot(session_id, job_id)
	return Response(
		content=data,
		media_type="image/png",
		headers={"Content-Disposition": f'inline; filename="browser-{job_id}.png"'},
	)


@app.get(
	"/v1/jobs/{job_id}/downloads",
	response_model=BrowserDownloadsResponse,
	dependencies=[Depends(require_auth)],
)
async def list_job_downloads(
	job_id: str,
	session_id: str = Depends(require_session),
) -> BrowserDownloadsResponse:
	return BrowserDownloadsResponse(files=await manager.downloads(session_id, job_id))


@app.get(
	"/v1/jobs/{job_id}/downloads/{name}",
	dependencies=[Depends(require_auth)],
	response_class=Response,
)
async def get_job_download(
	job_id: str,
	name: str,
	session_id: str = Depends(require_session),
) -> Response:
	download, data = await manager.download(session_id, job_id, name)
	return Response(content=data, media_type=download.mime_type)


@app.get(
	"/v1/tabs",
	response_model=BrowserTabsResponse,
	dependencies=[Depends(require_auth)],
)
async def list_tabs(
	session_id: str = Depends(require_session),
) -> BrowserTabsResponse:
	return BrowserTabsResponse(tabs=await manager.tabs(session_id), viewport=manager.viewport(session_id))


@app.post(
	"/v1/tabs/open",
	response_model=BrowserTabsResponse,
	dependencies=[Depends(require_auth)],
)
async def open_tab(
	request: OpenBrowserTabRequest,
	session_id: str = Depends(require_session),
) -> BrowserTabsResponse:
	return BrowserTabsResponse(tabs=await manager.open_tab(session_id, request.url), viewport=manager.viewport(session_id))


@app.get(
	"/v1/tabs/{tab_id}/screenshot",
	dependencies=[Depends(require_auth)],
	response_class=Response,
)
async def get_tab_screenshot(
	tab_id: str,
	session_id: str = Depends(require_session),
) -> Response:
	data = await manager.tab_screenshot(session_id, tab_id)
	return Response(
		content=data,
		media_type="image/png",
		headers={"Cache-Control": "no-store", "Content-Disposition": f'inline; filename="browser-tab-{tab_id}.png"'},
	)


@app.get(
	"/v1/tabs/{tab_id}/inspect",
	response_model=BrowserTabInspectionResponse,
	dependencies=[Depends(require_auth)],
)
async def inspect_tab(
	tab_id: str,
	session_id: str = Depends(require_session),
) -> BrowserTabInspectionResponse:
	return await manager.inspect_tab(session_id, tab_id)


@app.post(
	"/v1/tabs/{tab_id}/actions",
	response_model=BrowserTabActionResponse,
	dependencies=[Depends(require_auth)],
)
async def act_on_tab(
	tab_id: str,
	request: BrowserTabActionRequest,
	session_id: str = Depends(require_session),
) -> BrowserTabActionResponse:
	return await manager.act_on_tab(session_id, tab_id, request)


@app.post(
	"/v1/tabs/{tab_id}/close",
	response_model=BrowserTabsResponse,
	dependencies=[Depends(require_auth)],
)
async def close_tab(
	tab_id: str,
	session_id: str = Depends(require_session),
) -> BrowserTabsResponse:
	return BrowserTabsResponse(tabs=await manager.close_tab(session_id, tab_id), viewport=manager.viewport(session_id))


@app.get(
	"/v1/session/viewport",
	response_model=ViewportState,
	dependencies=[Depends(require_auth)],
)
async def get_session_viewport(
	session_id: str = Depends(require_session),
) -> ViewportState:
	return manager.viewport(session_id)


@app.put(
	"/v1/session/viewport",
	response_model=ViewportState,
	dependencies=[Depends(require_auth)],
)
async def put_session_viewport(
	request: SetViewportRequest,
	session_id: str = Depends(require_session),
) -> ViewportState:
	return await manager.set_viewport(session_id, request.quality)
