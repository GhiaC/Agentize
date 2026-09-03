package pages

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/ghiac/agentize/browseruse"
	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

const browserDebugPath = "/agentize/debug/browser"

// RenderBrowserDebug renders the browser manager overview.
func RenderBrowserDebug(snapshot *browseruse.DebugSnapshot, fetchErr error) string {
	configured := snapshot != nil || fetchErr == nil
	return RenderBrowserDebugWithStatus(snapshot, configured, fetchErr)
}

// RenderBrowserDebugWithStatus renders the browser debugger with explicit config status.
func RenderBrowserDebugWithStatus(
	snapshot *browseruse.DebugSnapshot,
	configured bool,
	fetchErr error,
) string {
	content := ui.ContainerStart()
	content += browserToolOverview(configured, fetchErr)
	content += `<div class="alert alert-info" role="alert">
			<strong>Network metadata only.</strong> Request/response bodies and headers are intentionally omitted.
			Live tabs refresh every 5 seconds when auto-refresh is enabled.
		</div>`

	if fetchErr != nil {
		content += fmt.Sprintf(
			`<div class="alert alert-warning" role="alert"><strong>Browser sidecar unavailable:</strong> %s</div>`,
			template.HTMLEscapeString(fetchErr.Error()),
		)
		content += ui.ContainerEnd()
		return ui.Header("Agentize Debug - Browser") +
			ui.NavbarAndBody(browserDebugPath, content) + ui.Footer()
	}
	if snapshot == nil {
		content += components.InfoAlert("Browser-use is not configured.")
		content += ui.ContainerEnd()
		return ui.Header("Agentize Debug - Browser") +
			ui.NavbarAndBody(browserDebugPath, content) + ui.Footer()
	}

	content += browserStats(snapshot)
	content += browserControls(snapshot.Jobs)
	content += browserSessionsSection(snapshot.Sessions)
	content += browserJobsSection(snapshot.Jobs)
	content += browserDebugScript()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Browser") +
		ui.NavbarAndBody(browserDebugPath, content) + ui.Footer()
}

// RenderBrowserJobDetail renders one job with persisted logs.
func RenderBrowserJobDetail(job *browseruse.DebugJob, logs *browseruse.JobLogs, fetchErr error) string {
	content := ui.ContainerStart()
	content += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Browser", URL: browserDebugPath},
		{Label: job.ID, Active: true},
	})

	if fetchErr != nil {
		content += fmt.Sprintf(
			`<div class="alert alert-warning" role="alert">%s</div>`,
			template.HTMLEscapeString(fetchErr.Error()),
		)
	}
	content += browserJobCard(job)
	if logs != nil && len(logs.Logs) > 0 {
		content += browserJobLogsTable(logs)
	} else {
		content += components.InfoAlert("No persisted logs for this job yet.")
	}
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Browser Job") +
		ui.NavbarAndBody(browserDebugPath, content) + ui.Footer()
}

// RenderBrowserSessionDetail renders one live browser session.
func RenderBrowserSessionDetail(session *browseruse.DebugSession, jobs []browseruse.DebugJob) string {
	content := ui.ContainerStart()
	content += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Browser", URL: browserDebugPath},
		{Label: model.DisplayID(session.SessionID), Active: true},
	})
	content += browserSessionCard(session, true)
	if len(jobs) > 0 {
		content += `<section class="card mb-3"><div class="card-header fw-semibold">Recent jobs</div><div class="card-body p-0">`
		for i := range jobs {
			content += browserJobRow(&jobs[i])
		}
		content += `</div></section>`
	}
	content += browserDebugScript()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Browser Session") +
		ui.NavbarAndBody(browserDebugPath, content) + ui.Footer()
}

func browserToolOverview(configured bool, fetchErr error) string {
	status := `<span class="badge text-bg-secondary">Not configured</span>`
	statusDetail := `Create a browseruse.Client and pass it through agentize.Options{BrowserUse: client} ` +
		`or call ag.UseBrowserUse(client).`
	if configured && fetchErr != nil {
		status = `<span class="badge text-bg-warning">Configured; debug unavailable</span>`
		statusDetail = `The browser_use schema is wired, but Agentize could not read debug metadata. ` +
			`Rebuild/restart the sidecar so it exposes GET /v1/debug/jobs.`
	} else if configured {
		status = `<span class="badge text-bg-success">Ready</span>`
		statusDetail = `Live sessions, tabs, job logs, and operator actions are available when the sidecar is running.`
	}

	return fmt.Sprintf(`<section class="card mb-3">
		<div class="card-header d-flex flex-wrap gap-2 justify-content-between align-items-center">
			<div><strong>Tool:</strong> <code>browser_use</code></div>
			%s
		</div>
		<div class="card-body">
			<p class="mb-3">%s</p>
			<div class="row g-3">
				<div class="col-lg-5">
					<div class="small text-muted mb-1">Supported actions</div>
					<div class="d-flex flex-wrap gap-2">
						<code>run</code><code>status</code><code>tabs</code><code>close_tab</code><code>screenshot</code><code>cancel</code>
					</div>
				</div>
				<div class="col-lg-7">
					<div class="small text-muted mb-1">Create debug data</div>
					<pre class="bg-light border rounded p-2 mb-2"><code>{"action":"run","task":"Open example.com and report the page title"}</code></pre>
					<div class="small text-muted mb-1">See open tabs</div>
					<pre class="bg-light border rounded p-2 mb-0"><code>{"action":"tabs"}</code></pre>
				</div>
			</div>
			<div class="small text-muted mt-3">
				See <a href="/agentize/debug/tool-calls">Tool Calls</a> for persisted invocations.
				Documentation: <code>docs/BROWSER_DEBUG.md</code>.
			</div>
		</div>
	</section>`, status, template.HTMLEscapeString(statusDetail))
}

func browserStats(snapshot *browseruse.DebugSnapshot) string {
	loads, failures, bytes := browserLoadTotals(snapshot.Jobs)
	return fmt.Sprintf(`<div class="row g-3 mb-3">
		<div class="col-6 col-lg"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Jobs retained</div><div class="fs-4 fw-semibold">%d / %d</div>
		</div></div></div>
		<div class="col-6 col-lg"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Running</div><div class="fs-4 fw-semibold" id="browser-running-jobs">%d</div>
		</div></div></div>
		<div class="col-6 col-lg"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Queued</div><div class="fs-4 fw-semibold" id="browser-queued-jobs">%d</div>
		</div></div></div>
		<div class="col-6 col-lg"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Live sessions</div><div class="fs-4 fw-semibold" id="browser-live-sessions">%d</div>
		</div></div></div>
		<div class="col-6 col-lg"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Open tabs</div><div class="fs-4 fw-semibold" id="browser-live-tabs">%d</div>
		</div></div></div>
		<div class="col-6 col-lg"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Concurrency</div><div class="fs-4 fw-semibold">%d</div>
		</div></div></div>
		<div class="col-6 col-lg"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Network shown</div><div class="fs-5 fw-semibold">%d</div><div class="small text-muted">%s · %d failed</div>
		</div></div></div>
	</div>`,
		snapshot.TotalJobs,
		snapshot.MaxJobs,
		snapshot.RunningJobs,
		snapshot.QueuedJobs,
		snapshot.LiveSessions,
		snapshot.TotalTabs,
		snapshot.MaxConcurrentJobs,
		loads,
		formatBytes(bytes),
		failures,
	)
}

func browserControls(jobs []browseruse.DebugJob) string {
	counts := map[browseruse.JobStatus]int{}
	for _, job := range jobs {
		counts[job.Status]++
	}
	button := func(label string, status browseruse.JobStatus, count int, active bool) string {
		activeClass := "btn-outline-secondary"
		if active {
			activeClass = "btn-secondary"
		}
		return fmt.Sprintf(`<button type="button" class="btn btn-sm %s" data-browser-status-filter="%s">%s <span class="badge text-bg-light ms-1">%d</span></button>`,
			activeClass,
			template.HTMLEscapeString(string(status)),
			template.HTMLEscapeString(label),
			count,
		)
	}

	return `<section class="card mb-3"><div class="card-body py-3">
		<div class="d-flex flex-wrap gap-2 justify-content-between align-items-center mb-2">
			<div class="fw-semibold">Jobs <span id="browser-debug-visible-count" class="text-muted small">Showing ` + fmt.Sprintf("%d", len(jobs)) + `</span></div>
			<div class="d-flex gap-2 align-items-center">
				<label class="form-check form-switch small mb-0"><input id="browser-debug-auto-refresh" class="form-check-input" type="checkbox" checked> Auto-refresh</label>
				<a href="` + browserDebugPath + `" class="btn btn-sm btn-outline-primary">Refresh now</a>
			</div>
		</div>
		<div class="d-flex flex-wrap gap-2 align-items-center mb-2">` +
		button("All", "", len(jobs), true) +
		button("Queued", browseruse.JobQueued, counts[browseruse.JobQueued], false) +
		button("Running", browseruse.JobRunning, counts[browseruse.JobRunning], false) +
		button("Failed", browseruse.JobFailed, counts[browseruse.JobFailed], false) +
		button("Succeeded", browseruse.JobSucceeded, counts[browseruse.JobSucceeded], false) +
		button("Cancelled", browseruse.JobCancelled, counts[browseruse.JobCancelled], false) +
		`</div>
		<div class="d-flex flex-wrap gap-2 align-items-center">
			<label for="browser-debug-filter" class="form-label mb-0 fw-semibold">Search</label>
			<input id="browser-debug-filter" class="form-control form-control-sm" style="max-width:420px"
				placeholder="job, session, task, URL, MIME type…" oninput="filterBrowserDebug(this.value)">
			<button type="button" class="btn btn-sm btn-outline-secondary" onclick="resetBrowserDebugFilters()">Clear</button>
		</div>
		<div id="browser-debug-no-results" class="text-muted small mt-2 d-none">No jobs match these filters.</div>
	</div></section>`
}

func browserSessionsSection(sessions []browseruse.DebugSession) string {
	out := `<section class="card mb-3"><div class="card-header d-flex justify-content-between align-items-center">
		<span class="fw-semibold">Browser sessions</span>
		<span class="text-muted small">Live Chromium profiles</span>
	</div><div class="card-body" id="browser-sessions-root">`
	if len(sessions) == 0 {
		out += components.InfoAlert("No browser sessions yet. Run a browser_use job or open a tab to create a persistent profile.")
	} else {
		for i := range sessions {
			out += browserSessionCard(&sessions[i], false)
		}
	}
	return out + `</div></section>`
}

func browserSessionCard(session *browseruse.DebugSession, expanded bool) string {
	statusBadge := `<span class="badge text-bg-secondary">Idle</span>`
	if session.Persistent {
		statusBadge = `<span class="badge text-bg-success">Live</span>`
	}
	if session.ActiveJobs > 0 {
		statusBadge += ` ` + components.Badge(fmt.Sprintf("%d running", session.ActiveJobs), "primary")
	}

	lastActivity := "—"
	if session.LastActivity != nil {
		lastActivity = debuger.FormatTimeAgo(*session.LastActivity)
	}

	detailURL := browserDebugPath + "/sessions/" + template.URLQueryEscaper(session.SessionID)
	killForm := ""
	if session.Persistent {
		killForm = fmt.Sprintf(`<form method="post" action="%s/sessions/%s/kill" class="d-inline" onsubmit="return confirm('Kill Chromium for this session?');">
			<button type="submit" class="btn btn-sm btn-outline-danger">Kill browser</button></form>`,
			browserDebugPath,
			template.URLQueryEscaper(session.SessionID),
		)
	}

	tabsHTML := browserTabBar(session)

	return fmt.Sprintf(`<article class="border rounded mb-3 browser-session-card" data-session-id="%s">
		<div class="d-flex flex-wrap gap-2 justify-content-between align-items-center px-3 py-2 bg-light border-bottom">
			<div class="d-flex flex-wrap gap-2 align-items-center">
				<strong class="text-break">%s</strong> %s
				<span class="text-muted small">%d tabs · %d jobs · %s</span>
			</div>
			<div class="d-flex gap-2">
				<a href="%s" class="btn btn-sm btn-outline-primary">Details</a>
				%s
			</div>
		</div>
		<div class="browser-tab-bar px-2 py-2 border-bottom bg-white">%s</div>
	</article>`,
		template.HTMLEscapeString(session.SessionID),
		components.EntityID(session.SessionID),
		statusBadge,
		session.TabCount,
		session.TotalJobs,
		template.HTMLEscapeString(lastActivity),
		template.HTMLEscapeString(detailURL),
		killForm,
		tabsHTML,
	)
}

func browserTabBar(session *browseruse.DebugSession) string {
	if len(session.Tabs) == 0 {
		return `<span class="text-muted small">No open tabs</span>`
	}
	var tabs strings.Builder
	for _, tab := range session.Tabs {
		active := ""
		if tab.Active {
			active = " active"
		}
		title := tab.Title
		if title == "" {
			title = tab.URL
		}
		if title == "" {
			title = tab.ID
		}
		closeBtn := ""
		if session.Persistent {
			closeBtn = fmt.Sprintf(`<form method="post" action="%s/sessions/%s/tabs/%s/close" class="browser-tab-close">
				<button type="submit" class="btn btn-sm btn-link text-danger p-0 ms-1" title="Close tab">×</button></form>`,
				browserDebugPath,
				template.URLQueryEscaper(session.SessionID),
				template.URLQueryEscaper(tab.ID),
			)
		}
		tabs.WriteString(fmt.Sprintf(
			`<div class="browser-tab-pill%s" title="%s">
				<span class="browser-tab-title">%s</span>
				<span class="browser-tab-url text-muted">%s</span>%s
			</div>`,
			active,
			template.HTMLEscapeString(tab.URL),
			template.HTMLEscapeString(truncateRunes(title, 40)),
			template.HTMLEscapeString(truncateRunes(tab.URL, 60)),
			closeBtn,
		))
	}
	return `<div class="d-flex flex-wrap gap-2">` + tabs.String() + `</div>`
}

func browserJobsSection(jobs []browseruse.DebugJob) string {
	out := `<section class="card mb-3"><div class="card-header fw-semibold">Jobs</div><div class="card-body p-0" id="browser-jobs-root">`
	if len(jobs) == 0 {
		out += components.InfoAlert("No browser jobs have been recorded yet. Invoke browser_use with action \"run\" first.")
	} else {
		for i := range jobs {
			out += browserJobCard(&jobs[i])
		}
	}
	return out + `</div></section>`
}

func browserJobRow(job *browseruse.DebugJob) string {
	detailURL := browserDebugPath + "/jobs/" + template.URLQueryEscaper(job.ID)
	return fmt.Sprintf(`<div class="border-bottom px-3 py-2 d-flex justify-content-between align-items-center">
		<div><a href="%s">%s</a> %s <span class="text-muted small">%s</span></div>
		<span class="text-muted small">%s</span>
	</div>`,
		template.HTMLEscapeString(detailURL),
		template.HTMLEscapeString(job.ID),
		browserStatusBadge(job.Status),
		template.HTMLEscapeString(truncateRunes(job.Task, 80)),
		template.HTMLEscapeString(string(job.Status)),
	)
}

func browserJobCard(job *browseruse.DebugJob) string {
	searchParts := []string{job.ID, job.SessionID, job.Task, string(job.Status), job.Error}
	for _, load := range job.Loads {
		searchParts = append(searchParts, load.URL, load.MIMEType, load.Method)
	}
	search := template.HTMLEscapeString(strings.ToLower(strings.Join(searchParts, " ")))
	started := job.CreatedAt
	if job.StartedAt != nil {
		started = *job.StartedAt
	}
	duration := "—"
	if job.CompletedAt != nil {
		duration = debuger.FormatDurationMs(job.CompletedAt.Sub(started).Milliseconds())
	} else if job.Status == browseruse.JobRunning {
		duration = debuger.FormatDurationMs(time.Since(started).Milliseconds())
	}

	screenshot := `<span class="text-muted">Not available</span>`
	if job.ScreenshotAvailable {
		screenshotURL := browserDebugPath + "/" + template.URLQueryEscaper(job.ID) +
			"/screenshot?session_id=" + url.QueryEscape(job.SessionID)
		screenshot = fmt.Sprintf(
			`<a href="%s" target="_blank" class="btn btn-sm btn-outline-primary">Screenshot</a>`,
			template.HTMLEscapeString(screenshotURL),
		)
	}

	errorBlock := ""
	if job.Error != "" {
		errorBlock = fmt.Sprintf(
			`<div class="alert alert-danger py-2 mt-3 mb-0"><strong>Error:</strong> %s</div>`,
			template.HTMLEscapeString(job.Error),
		)
	}

	actions := fmt.Sprintf(
		`<a href="%s/jobs/%s" class="btn btn-sm btn-outline-secondary">Logs</a>`,
		browserDebugPath,
		template.URLQueryEscaper(job.ID),
	)
	if job.Status == browseruse.JobRunning || job.Status == browseruse.JobQueued {
		actions += fmt.Sprintf(
			` <form method="post" action="%s/jobs/%s/cancel" class="d-inline">
				<button type="submit" class="btn btn-sm btn-outline-danger">Cancel</button></form>`,
			browserDebugPath,
			template.URLQueryEscaper(job.ID),
		)
	}

	return fmt.Sprintf(`<section class="card mb-3 border-0 border-bottom rounded-0" data-browser-debug-job data-browser-status="%s" data-browser-search="%s">
		<div class="card-header d-flex flex-wrap gap-2 justify-content-between align-items-center bg-white">
			<div><strong>%s</strong> %s</div>
			<div class="d-flex gap-2 align-items-center">
				<button type="button" class="btn btn-sm btn-outline-secondary" data-browser-copy="%s" title="Copy job ID">Copy</button>
				%s %s
			</div>
		</div>
		<div class="card-body">
			<div class="row g-3">
				<div class="col-lg-7">
					<div class="small text-muted mb-1">Task</div>
					<div class="text-break">%s</div>
				</div>
				<div class="col-lg-5">
					<table class="table table-sm table-borderless mb-0">
						<tr><th>Session</th><td class="text-break"><a href="%s/sessions/%s">%s</a></td></tr>
						<tr><th>Created</th><td>%s</td></tr>
						<tr><th>Duration</th><td>%s</td></tr>
						<tr><th>Network loads</th><td>%d</td></tr>
					</table>
				</div>
			</div>
			%s
			%s
			%s
		</div>
	</section>`,
		template.HTMLEscapeString(string(job.Status)),
		search,
		template.HTMLEscapeString(job.ID),
		browserStatusBadge(job.Status),
		template.HTMLEscapeString(job.ID),
		screenshot,
		actions,
		template.HTMLEscapeString(job.Task),
		browserDebugPath,
		template.URLQueryEscaper(job.SessionID),
		components.EntityIDText(job.SessionID),
		debuger.FormatTime(job.CreatedAt),
		duration,
		job.LoadCount,
		errorBlock,
		browserResultDetails(job),
		browserLoadsTable(job),
	)
}

func browserJobLogsTable(logs *browseruse.JobLogs) string {
	out := `<section class="card mb-3"><div class="card-header fw-semibold">Job logs</div>
		<div class="table-responsive"><table class="table table-sm table-hover mb-0">
		<thead><tr><th>Time</th><th>Level</th><th>Message</th></tr></thead><tbody>`
	for _, entry := range logs.Logs {
		levelClass := "secondary"
		switch strings.ToLower(entry.Level) {
		case "error":
			levelClass = "danger"
		case "warn", "warning":
			levelClass = "warning text-dark"
		case "info":
			levelClass = "info text-dark"
		}
		out += fmt.Sprintf(`<tr>
			<td class="text-nowrap">%s</td>
			<td>%s</td>
			<td class="text-break">%s</td>
		</tr>`,
			debuger.FormatTime(entry.CreatedAt),
			components.Badge(entry.Level, levelClass),
			template.HTMLEscapeString(entry.Message),
		)
	}
	return out + `</tbody></table></div></section>`
}

func browserResultDetails(job *browseruse.DebugJob) string {
	if job.Result == nil {
		return ""
	}
	result := job.Result
	success := "Not reported"
	if result.Successful != nil {
		if *result.Successful {
			success = "Yes"
		} else {
			success = "No"
		}
	}
	out := fmt.Sprintf(`<details class="mt-3"><summary class="fw-semibold">Run outcome</summary>
		<div class="row g-3 mt-1">
			<div class="col-md-5"><table class="table table-sm table-borderless mb-0">
				<tr><th>Completed</th><td>%t</td></tr><tr><th>Successful</th><td>%s</td></tr>
				<tr><th>Steps</th><td>%d</td></tr><tr><th>Runner duration</th><td>%s</td></tr>
			</table></div>
			<div class="col-md-7">`, result.Done, success, result.Steps, debuger.FormatDurationMs(int64(result.DurationSeconds*1000)))
	if result.FinalResult != "" {
		out += `<div class="small text-muted mb-1">Final result</div><pre class="bg-light border rounded p-2 text-break mb-2" style="white-space:pre-wrap">` + template.HTMLEscapeString(result.FinalResult) + `</pre>`
	}
	if len(result.VisitedURLs) > 0 {
		out += `<div class="small text-muted mb-1">Visited URLs</div><ul class="small mb-2">`
		for _, visitedURL := range result.VisitedURLs {
			out += `<li class="text-break">` + template.HTMLEscapeString(visitedURL) + `</li>`
		}
		out += `</ul>`
	}
	if len(result.ActionNames) > 0 {
		out += `<div class="small text-muted mb-1">Actions</div><div class="d-flex flex-wrap gap-1 mb-2">`
		for _, action := range result.ActionNames {
			out += components.InlineCode(template.HTMLEscapeString(action))
		}
		out += `</div>`
	}
	if len(result.Errors) > 0 {
		out += `<div class="alert alert-warning py-2 mb-2"><strong>Runner notes:</strong><ul class="mb-0">`
		for _, resultError := range result.Errors {
			out += `<li class="text-break">` + template.HTMLEscapeString(resultError) + `</li>`
		}
		out += `</ul></div>`
	}
	if len(result.Actions) > 0 {
		if actionsJSON, err := json.Marshal(result.Actions); err == nil {
			out += `<details><summary class="small">Action trace (` + fmt.Sprintf("%d", len(result.Actions)) + `)</summary><pre class="bg-light border rounded p-2 mt-2 text-break mb-0" style="white-space:pre-wrap">` + template.HTMLEscapeString(string(actionsJSON)) + `</pre></details>`
		}
	}
	return out + `</div></div></details>`
}

func browserLoadsTable(job *browseruse.DebugJob) string {
	if len(job.Loads) == 0 {
		return `<div class="text-muted small mt-3">No completed network metadata is available yet.</div>`
	}
	hidden := job.LoadCount - len(job.Loads)
	summary := fmt.Sprintf("%d most recent loads", len(job.Loads))
	if hidden > 0 {
		summary += fmt.Sprintf(" (%d older omitted)", hidden)
	}
	_, failures, bytes := browserLoadTotals([]browseruse.DebugJob{*job})
	out := fmt.Sprintf(`<details class="mt-3"><summary class="fw-semibold">%s</summary>
		<div class="small text-muted mt-1">%s transferred · %d failed</div>
		<div class="table-responsive mt-2"><table class="table table-sm table-hover align-middle">
		<thead><tr><th>Time</th><th>Method</th><th>Status</th><th>Type</th><th>Size</th><th>Duration</th><th>URL</th></tr></thead><tbody>`,
		template.HTMLEscapeString(summary),
		formatBytes(bytes),
		failures,
	)
	for _, load := range job.Loads {
		when := "—"
		if load.StartedAt != nil {
			when = debuger.FormatTime(*load.StartedAt)
		}
		status := fmt.Sprintf("%d", load.Status)
		statusClass := "secondary"
		switch {
		case load.Failed || load.Status == 0 || load.Status >= 500:
			statusClass = "danger"
		case load.Status >= 400:
			statusClass = "warning text-dark"
		case load.Status >= 300:
			statusClass = "info text-dark"
		case load.Status >= 200:
			statusClass = "success"
		}
		out += fmt.Sprintf(`<tr>
			<td class="text-nowrap">%s</td>
			<td>%s</td>
			<td>%s</td>
			<td class="text-break">%s</td>
			<td class="text-nowrap">%s</td>
			<td class="text-nowrap">%s</td>
			<td class="text-break" style="min-width:320px">%s</td>
		</tr>`,
			when,
			components.InlineCode(template.HTMLEscapeString(load.Method)),
			components.Badge(status, statusClass),
			template.HTMLEscapeString(load.MIMEType),
			formatBytes(load.Bytes),
			debuger.FormatDurationMs(int64(load.DurationMs)),
			template.HTMLEscapeString(load.URL),
		)
	}
	return out + `</tbody></table></div></details>`
}

func browserStatusBadge(status browseruse.JobStatus) string {
	switch status {
	case browseruse.JobSucceeded:
		return components.Badge("succeeded", "success")
	case browseruse.JobFailed:
		return components.Badge("failed", "danger")
	case browseruse.JobCancelled:
		return components.Badge("cancelled", "secondary")
	case browseruse.JobRunning:
		return components.Badge("running", "primary")
	default:
		return components.Badge(string(status), "warning text-dark")
	}
}

func browserLoadTotals(jobs []browseruse.DebugJob) (total, failures int, bytes int64) {
	for _, job := range jobs {
		for _, load := range job.Loads {
			total++
			bytes += load.Bytes
			if load.Failed || load.Status == 0 || load.Status >= 400 {
				failures++
			}
		}
	}
	return total, failures, bytes
}

func browserDebugScript() string {
	return `<style>
.browser-tab-bar { overflow-x: auto; }
.browser-tab-pill { display:flex; align-items:center; gap:.35rem; border:1px solid #dee2e6; border-radius:.5rem; padding:.25rem .6rem; max-width:320px; background:#fff; }
.browser-tab-pill.active { border-color:#0d6efd; box-shadow: inset 0 -2px 0 #0d6efd; }
.browser-tab-title { font-size:.85rem; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; max-width:140px; }
.browser-tab-url { font-size:.75rem; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; max-width:120px; }
.browser-tab-close { display:inline; margin:0; }
</style>
<script>
var browserDebugPath = "` + browserDebugPath + `";
var browserStatusFilter = '';
function filterBrowserDebug(value) {
	var query = (value || '').toLowerCase().trim();
	var visible = 0;
	document.querySelectorAll('[data-browser-debug-job]').forEach(function (node) {
		var matches = (!query || node.dataset.browserSearch.indexOf(query) !== -1) &&
			(!browserStatusFilter || node.dataset.browserStatus === browserStatusFilter);
		node.style.display = matches ? '' : 'none';
		if (matches) visible++;
	});
	var count = document.getElementById('browser-debug-visible-count');
	if (count) count.textContent = 'Showing ' + visible;
	var empty = document.getElementById('browser-debug-no-results');
	if (empty) empty.classList.toggle('d-none', visible !== 0);
}
function resetBrowserDebugFilters() {
	browserStatusFilter = '';
	var input = document.getElementById('browser-debug-filter');
	if (input) input.value = '';
	document.querySelectorAll('[data-browser-status-filter]').forEach(function (button) {
		button.classList.toggle('btn-secondary', !button.dataset.browserStatusFilter);
		button.classList.toggle('btn-outline-secondary', !!button.dataset.browserStatusFilter);
	});
	filterBrowserDebug('');
}
document.querySelectorAll('[data-browser-status-filter]').forEach(function (button) {
	button.addEventListener('click', function () {
		browserStatusFilter = button.dataset.browserStatusFilter || '';
		document.querySelectorAll('[data-browser-status-filter]').forEach(function (other) {
			other.classList.toggle('btn-secondary', other === button);
			other.classList.toggle('btn-outline-secondary', other !== button);
		});
		var input = document.getElementById('browser-debug-filter');
		filterBrowserDebug(input ? input.value : '');
	});
});
document.querySelectorAll('[data-browser-copy]').forEach(function (button) {
	button.addEventListener('click', function () {
		var original = button.textContent;
		if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(button.dataset.browserCopy);
		button.textContent = 'Copied';
		setTimeout(function () { button.textContent = original; }, 1200);
	});
});
function renderBrowserTabPill(tab, sessionID, persistent) {
	var title = tab.title || tab.url || tab.id || 'tab';
	var active = tab.active ? ' active' : '';
	var closeBtn = '';
	if (persistent) {
		closeBtn = '<form method="post" action="' + browserDebugPath + '/sessions/' + encodeURIComponent(sessionID) + '/tabs/' + encodeURIComponent(tab.id) + '/close" class="browser-tab-close"><button type="submit" class="btn btn-sm btn-link text-danger p-0 ms-1" title="Close tab">×</button></form>';
	}
	return '<div class="browser-tab-pill' + active + '" title="' + (tab.url || '') + '"><span class="browser-tab-title">' + title + '</span><span class="browser-tab-url text-muted">' + (tab.url || '') + '</span>' + closeBtn + '</div>';
}
function renderBrowserSessionCard(session) {
	var status = session.persistent ? '<span class="badge text-bg-success">Live</span>' : '<span class="badge text-bg-secondary">Idle</span>';
	if (session.active_jobs > 0) status += ' <span class="badge text-bg-primary">' + session.active_jobs + ' active</span>';
	var tabs = (session.tabs || []).map(function (tab) { return renderBrowserTabPill(tab, session.session_id, session.persistent); }).join('');
	if (!tabs) tabs = '<span class="text-muted small">No open tabs</span>';
	var kill = session.persistent ? '<form method="post" action="' + browserDebugPath + '/sessions/' + encodeURIComponent(session.session_id) + '/kill" class="d-inline" onsubmit="return confirm(\'Kill Chromium for this session?\');"><button type="submit" class="btn btn-sm btn-outline-danger">Kill browser</button></form>' : '';
	return '<article class="border rounded mb-3 browser-session-card" data-session-id="' + session.session_id + '"><div class="d-flex flex-wrap gap-2 justify-content-between align-items-center px-3 py-2 bg-light border-bottom"><div><strong><code>' + session.session_id + '</code></strong> ' + status + ' <span class="text-muted small">' + (session.tab_count || 0) + ' tabs</span></div><div class="d-flex gap-2"><a href="' + browserDebugPath + '/sessions/' + encodeURIComponent(session.session_id) + '" class="btn btn-sm btn-outline-primary">Details</a>' + kill + '</div></div><div class="browser-tab-bar px-2 py-2 border-bottom bg-white"><div class="d-flex flex-wrap gap-2">' + tabs + '</div></div></article>';
}
function pollBrowserLive() {
	fetch(browserDebugPath + '/api/live', { credentials: 'same-origin' })
		.then(function (response) { return response.ok ? response.json() : null; })
		.then(function (payload) {
			if (!payload) return;
			var sessionsRoot = document.getElementById('browser-sessions-root');
			if (sessionsRoot && payload.sessions) {
				sessionsRoot.innerHTML = payload.sessions.length ? payload.sessions.map(renderBrowserSessionCard).join('') : '<div class="alert alert-info mb-0">No browser sessions yet.</div>';
			}
			var liveSessions = document.getElementById('browser-live-sessions');
			if (liveSessions && payload.live_sessions !== undefined) liveSessions.textContent = payload.live_sessions;
			var liveTabs = document.getElementById('browser-live-tabs');
			if (liveTabs && payload.total_tabs !== undefined) liveTabs.textContent = payload.total_tabs;
			var runningJobs = document.getElementById('browser-running-jobs');
			if (runningJobs && payload.running_jobs !== undefined) runningJobs.textContent = payload.running_jobs;
			var queuedJobs = document.getElementById('browser-queued-jobs');
			if (queuedJobs && payload.queued_jobs !== undefined) queuedJobs.textContent = payload.queued_jobs;
		})
		.catch(function () {});
}
var autoRefresh = document.getElementById('browser-debug-auto-refresh');
if (autoRefresh) {
	autoRefresh.checked = window.sessionStorage.getItem('browserDebugAutoRefresh') !== '0';
	autoRefresh.addEventListener('change', function () {
		window.sessionStorage.setItem('browserDebugAutoRefresh', autoRefresh.checked ? '1' : '0');
	});
	setInterval(function () {
		if (autoRefresh.checked) pollBrowserLive();
	}, 5000);
}
</script>`
}
