package model

import (
	"strings"
	"time"
)

const (
	MessageMetaKindSchedule = "schedule"
	MessageMetaKindAlert    = "alert"
	MessageMetaKindChart    = "chart"
	MessageMetaKindPosition = "position"

	MessageWidgetStatus    = "status"
	MessageWidgetLitechart = "litechart"
)

const scheduleMetaTextLimit = 2000

// MessageMetaString reads a string field from message metadata.
func MessageMetaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	value, _ := meta[key].(string)
	return strings.TrimSpace(value)
}

// MessageKind returns the compact renderer kind stored on the message.
func MessageKind(message *Message) string {
	if message == nil {
		return ""
	}
	return strings.ToLower(MessageMetaString(message.Metadata, "kind"))
}

// NewScheduleMessageMeta stores the full schedule snapshot next to a one-line
// chat summary so hosts can render a collapsed chip and later mount widgets
// (chart, position, ...) from the same metadata object.
func NewScheduleMessageMeta(schedule *TaskSchedule) map[string]any {
	if schedule == nil {
		return nil
	}
	status := strings.TrimSpace(string(schedule.LastRunStatus))
	if status == "" {
		status = strings.TrimSpace(string(schedule.Status))
	}
	last := strings.TrimSpace(schedule.LastConclusion)
	if last == "" {
		last = strings.TrimSpace(schedule.LastOutput)
	}
	if schedule.LastError != "" {
		last = strings.TrimSpace(schedule.LastError)
	}
	if last == "" && schedule.LastRunStatus == TaskRunRunning {
		last = "Starting…"
	}
	last = truncateMetaText(last, scheduleMetaTextLimit)
	meta := map[string]any{
		"kind":      MessageMetaKindSchedule,
		"widget":    MessageWidgetStatus,
		"title":     strings.TrimSpace(schedule.Name),
		"summary":   status,
		"status":    status,
		"origin_id": strings.TrimSpace(schedule.ScheduleID),
		"schedule": map[string]any{
			"id":               schedule.ScheduleID,
			"name":             schedule.Name,
			"status":           string(schedule.Status),
			"last_run_status":  string(schedule.LastRunStatus),
			"run_count":        schedule.RunCount,
			"max_runs":         schedule.MaxRuns,
			"interval_seconds": schedule.IntervalSeconds,
			"last_error":       strings.TrimSpace(schedule.LastError),
			"last_conclusion":  last,
			"last_output":      truncateMetaText(schedule.LastOutput, scheduleMetaTextLimit),
			"prompt":           truncateMetaText(schedule.Prompt, 500),
		},
	}
	if !schedule.LastRunAt.IsZero() {
		meta["schedule"].(map[string]any)["last_run_at"] = schedule.LastRunAt.UTC().Format(time.RFC3339)
	}
	if !schedule.NextRunAt.IsZero() {
		meta["schedule"].(map[string]any)["next_run_at"] = schedule.NextRunAt.UTC().Format(time.RFC3339)
	}
	return meta
}

// NewAlertMessageMeta stores a compact alert chip plus the extra fields a host
// may later use for widgets (symbol, interval, evidence, ...).
func NewAlertMessageMeta(title, summary, detail string, extra map[string]any) map[string]any {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Alert"
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "fired"
	}
	detail = truncateMetaText(detail, scheduleMetaTextLimit)
	alert := map[string]any{
		"name":            title,
		"last_conclusion": detail,
	}
	for key, value := range extra {
		if strings.TrimSpace(key) == "" || value == nil {
			continue
		}
		alert[key] = value
	}
	return map[string]any{
		"kind":    MessageMetaKindAlert,
		"widget":  MessageWidgetStatus,
		"title":   title,
		"summary": summary,
		"status":  summary,
		"alert":   alert,
	}
}

func truncateMetaText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
