package debuger

import "net/url"

// DebugPrefix is the operator UI root. Detail pages nest per-user because
// numeric conversation/session/message/tool/file/workflow ids increment inside
// a parent and are not globally unique.
const DebugPrefix = "/agentize/debug"

func pathEscape(id string) string {
	return url.PathEscape(id)
}

// UserPath is /agentize/debug/users/{userID}.
func UserPath(userID string) string {
	return DebugPrefix + "/users/" + pathEscape(userID)
}

// SessionPath is /agentize/debug/users/{userID}/sessions/{sessionID}.
func SessionPath(userID, sessionID string) string {
	return UserPath(userID) + "/sessions/" + pathEscape(sessionID)
}

// SessionMessagesPath lists messages for one user's session.
func SessionMessagesPath(userID, sessionID string) string {
	return SessionPath(userID, sessionID) + "/messages"
}

// SessionToolCallsPath lists tool calls for one user's session.
func SessionToolCallsPath(userID, sessionID string) string {
	return SessionPath(userID, sessionID) + "/tool-calls"
}

// ToolCallPath is /agentize/debug/users/{userID}/sessions/{sessionID}/tool-calls/{toolID}.
func ToolCallPath(userID, sessionID, toolID string) string {
	return SessionToolCallsPath(userID, sessionID) + "/" + pathEscape(toolID)
}

// WorkflowPath is /agentize/debug/users/{userID}/workflows/{workflowID}.
func WorkflowPath(userID, workflowID string) string {
	return UserPath(userID) + "/workflows/" + pathEscape(workflowID)
}

// SchedulePath is /agentize/debug/users/{userID}/schedules/{scheduleID}.
func SchedulePath(userID, scheduleID string) string {
	return UserPath(userID) + "/schedules/" + pathEscape(scheduleID)
}

// FilePath is /agentize/debug/users/{userID}/files/{fileID}.
func FilePath(userID, fileID string) string {
	return UserPath(userID) + "/files/" + pathEscape(fileID)
}

// FileRawPath streams one user's file bytes.
func FileRawPath(userID, fileID string) string {
	return FilePath(userID, fileID) + "/raw"
}

// RoutePath is /agentize/debug/users/{userID}/sessions/{sessionID}/routes/{traceID}.
func RoutePath(userID, sessionID, traceID string) string {
	return SessionPath(userID, sessionID) + "/routes/" + pathEscape(traceID)
}

// LogPath is /agentize/debug/users/{userID}/sessions/{sessionID}/logs/{logID}.
func LogPath(userID, sessionID, logID string) string {
	return SessionPath(userID, sessionID) + "/logs/" + pathEscape(logID)
}
