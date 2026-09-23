package model

import (
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const toolActivityLabelMax = 80

// toolLabelFallback is the English chip for tools whose registered display
// name is not usable (empty or non-Latin). Names that already match a
// humanized function name do not need an entry.
var toolLabelFallback = map[string]string{
	"collect_result":   "Extract result",
	"inspect_result":   "Inspect result",
	"manage_schedules": "Schedules",
}

// ToolActivityLabel is the chat chip for one tool call. It keeps the short
// English tool name and appends the arguments that say what this call is
// doing (action, symbol, query, URL, and so on). A non-Latin display name is
// dropped so a stored Persian label cannot surface.
func ToolActivityLabel(displayName, functionName, argumentsJSON string) string {
	base := toolActivityBase(displayName, functionName)
	detail := toolActivityDetail(functionName, argumentsJSON)
	if detail == "" {
		return clipToolLabel(base, toolActivityLabelMax)
	}
	return clipToolLabel(base+" · "+detail, toolActivityLabelMax)
}

// LatinActivityText returns text for a status line. A label that is only
// Arabic-script (a leftover Persian chip) is dropped so the UI can fall
// back to the English phase name. Mixed text is kept: the argument the
// model passed may itself be Persian.
func LatinActivityText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	latin := false
	arabic := false
	for _, r := range text {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			latin = true
		case r >= 0x0600 && r <= 0x06FF:
			arabic = true
		}
	}
	if arabic && !latin {
		return ""
	}
	return text
}

func toolActivityBase(displayName, functionName string) string {
	name := strings.TrimSpace(displayName)
	if i := strings.Index(name, " · "); i >= 0 {
		name = strings.TrimSpace(name[:i])
	}
	if base := latinLabel(name); base != "" {
		return base
	}
	if base, ok := toolLabelFallback[strings.TrimSpace(functionName)]; ok {
		return base
	}
	if base := humanizeToolName(functionName); base != "" {
		return base
	}
	return "Tool"
}

func latinLabel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, r := range s {
		if r > 127 {
			return ""
		}
	}
	return s
}

func humanizeToolName(name string) string {
	name = strings.TrimSpace(name)
	if latinLabel(name) == "" {
		return ""
	}
	parts := strings.Split(name, "_")
	words := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		words = append(words, strings.ToLower(part))
	}
	if len(words) == 0 {
		return ""
	}
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	return strings.Join(words, " ")
}

func toolActivityDetail(functionName, argumentsJSON string) string {
	raw := strings.TrimSpace(argumentsJSON)
	if raw == "" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil || len(args) == 0 {
		return ""
	}
	switch strings.TrimSpace(functionName) {
	case "inspect_result":
		return inspectActivityDetail(args)
	default:
		return genericActivityDetail(args)
	}
}

func inspectActivityDetail(args map[string]any) string {
	action := strings.ToLower(toolLabelScalar(args["action"]))
	switch action {
	case "head", "tail":
		if n := toolLabelScalar(args["lines"]); n != "" && n != "0" {
			return action + " " + n
		}
		return action
	case "slice":
		start := toolLabelScalar(args["start"])
		end := toolLabelScalar(args["end"])
		switch {
		case start != "" && end != "":
			return "slice " + start + "-" + end
		case start != "":
			return "slice " + start
		default:
			return "slice"
		}
	case "read":
		if off := toolLabelScalar(args["offset"]); off != "" && off != "0" {
			return "read @" + off
		}
		return "read"
	case "grep", "count":
		if q := clipToolLabel(toolLabelScalar(args["query"]), 40); q != "" {
			return action + " " + q
		}
		return action
	default:
		if action != "" {
			return action
		}
	}
	return genericActivityDetail(args)
}

func genericActivityDetail(args map[string]any) string {
	action := strings.ToLower(toolLabelScalar(args["action"]))
	side := strings.ToLower(toolLabelScalar(args["side"]))
	subjectKeys := []string{
		"symbol", "pair", "ticker", "name", "title", "query",
		"url", "path", "file", "selector", "text",
		"schedule_id", "order_id", "job_id",
	}
	subject := ""
	subjectKey := ""
	for _, key := range subjectKeys {
		value := toolLabelScalar(args[key])
		if value == "" {
			continue
		}
		if key == "url" {
			value = toolLabelURL(value)
		}
		subject = clipToolLabel(value, 40)
		subjectKey = key
		break
	}
	extra := ""
	if subjectKey != "interval" && subjectKey != "timeframe" {
		extra = toolLabelScalar(args["interval"])
		if extra == "" {
			extra = toolLabelScalar(args["timeframe"])
		}
		extra = clipToolLabel(extra, 16)
	}
	parts := make([]string, 0, 4)
	if action != "" {
		parts = append(parts, action)
	}
	if side != "" {
		parts = append(parts, side)
	}
	if subject != "" {
		parts = append(parts, subject)
	}
	if extra != "" && len(parts) < 4 {
		parts = append(parts, extra)
	}
	return strings.Join(parts, " ")
}

func toolLabelScalar(v any) string {
	switch t := v.(type) {
	case string:
		return strings.Join(strings.Fields(t), " ")
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return ""
		}
		if t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func toolLabelURL(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return clipToolLabel(raw, 40)
	}
	path := parsed.Path
	if path == "/" {
		path = ""
	}
	shown := parsed.Host + path
	if utf8.RuneCountInString(shown) > 40 {
		return parsed.Host
	}
	return shown
}

func clipToolLabel(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	if max <= 3 {
		runes := []rune(s)
		return string(runes[:max])
	}
	runes := []rune(s)
	return string(runes[:max-3]) + "..."
}
