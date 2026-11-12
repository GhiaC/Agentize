package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/ghiac/agentize/mcpauth"
	"github.com/ghiac/agentize/mcpserver"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/toolruntime"
	"github.com/google/uuid"
)

// MCPProvider exposes the current user's executable registered tools, chats and
// files. It shares the Engine's store, quota callback and approval manager.
func (e *Engine) MCPProvider() mcpserver.Provider { return &engineMCP{e} }

type engineMCP struct{ e *Engine }
type mcpEntry struct {
	tool     mcpserver.Tool
	node     string
	function string
}

func mcpFault(message string) error { return &toolruntime.Fault{Code: "forbidden", Message: message} }
func mcpTool(name, description string, properties map[string]any, required ...string) mcpserver.Tool {
	if required == nil {
		required = []string{}
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false})
	return mcpserver.Tool{Name: name, Description: description, InputSchema: schema}
}
func strProp() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
func (p *engineMCP) catalog(ctx context.Context, principal mcpauth.Principal) (map[string]mcpEntry, error) {
	if p.e == nil || p.e.Sessions == nil || principal.UserID == "" || principal.CredentialID == "" {
		return nil, errors.New("MCP engine unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries := map[string]mcpEntry{}
	add := func(t mcpserver.Tool) { entries[t.Name] = mcpEntry{tool: t} }
	if mcpauth.HasScope(principal, "conversations:read") {
		add(mcpTool("agentize_list_conversations", "List your conversations.", map[string]any{"offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}))
		add(mcpTool("agentize_get_conversation", "Read one of your conversations and its messages.", map[string]any{"conversation_id": strProp()}, "conversation_id"))
	}
	if mcpauth.HasScope(principal, "conversations:write") {
		add(mcpTool("agentize_create_conversation", "Create a durable conversation. Use its id for tools and messages.", map[string]any{"title": strProp()}))
		if mcpauth.HasScope(principal, "inference:execute") {
			add(mcpTool("agentize_send_message", "Send a message through Agentize using your quota and conversation history.", map[string]any{"conversation_id": strProp(), "message": strProp()}, "conversation_id", "message"))
		}
	}
	if p.e.Files != nil && mcpauth.HasScope(principal, "files:read") {
		add(mcpTool("agentize_list_files", "List your uploaded and generated files.", map[string]any{"offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}))
		add(mcpTool("agentize_read_file", "Read your file as base64 (maximum 512 KiB).", map[string]any{"file_id": strProp()}, "file_id"))
	}
	if p.e.Files != nil && mcpauth.HasScope(principal, "files:write") {
		add(mcpTool("agentize_upload_file", "Upload base64 file bytes to your conversation (maximum 512 KiB).", map[string]any{"conversation_id": strProp(), "name": strProp(), "mime_type": strProp(), "data_base64": strProp()}, "conversation_id", "name", "data_base64"))
	}
	// Registered functions may call models themselves; require the inference grant
	// rather than allowing a function to launder a restricted credential into inference.
	if !mcpauth.HasScope(principal, "tools:execute") || !mcpauth.HasScope(principal, "inference:execute") {
		return entries, nil
	}
	session := &model.Session{UserID: principal.UserID}
	if p.e.Repo != nil {
		var walk func(string) error
		walk = func(path string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			node, err := p.e.executableKnowledgeNode(principal.UserID, path)
			if err != nil {
				return nil
			}
			session.NodeDigests = append(session.NodeDigests, summarizeNode(node))
			children, err := p.e.Repo.GetChildren(path)
			if err != nil {
				return err
			}
			for _, child := range children {
				if err := walk(child); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk("root"); err != nil {
			return nil, err
		}
	}
	for _, t := range p.e.GetTools(session) {
		if t.Function == nil || len(t.Function.Name) > 114 || p.e.Functions == nil || !p.e.Functions.Has(t.Function.Name) {
			continue
		}
		if t.Function.Name == "manage_files" && (!mcpauth.HasScope(principal, "files:read") || !mcpauth.HasScope(principal, "files:write")) {
			continue
		}
		raw, err := json.Marshal(t.Function.Parameters)
		if err != nil {
			return nil, err
		}
		var schema map[string]any
		if json.Unmarshal(raw, &schema) != nil {
			return nil, errors.New("invalid tool schema")
		}
		props, _ := schema["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
		}
		if _, exists := props["agentize_conversation_id"]; exists {
			return nil, errors.New("reserved conversation field")
		}
		props["agentize_conversation_id"] = map[string]any{"type": "string", "minLength": 1, "description": "Your conversation ID from agentize_create_conversation or agentize_list_conversations."}
		schema["properties"] = props
		required, _ := schema["required"].([]any)
		required = append(required, "agentize_conversation_id")
		schema["required"] = required
		raw, _ = json.Marshal(schema)
		if _, exists := entries[t.Function.Name]; exists {
			return nil, errors.New("reserved MCP tool name")
		}
		entry := mcpEntry{function: t.Function.Name, tool: mcpserver.Tool{Name: "agentize_tool_" + t.Function.Name, Description: t.Function.Description, InputSchema: raw}}
		for _, digest := range session.NodeDigests {
			node, err := p.e.executableKnowledgeNode(principal.UserID, digest.Path)
			if err != nil {
				continue
			}
			for _, tool := range node.Tools {
				if tool.Name == t.Function.Name && tool.Status == model.ToolStatusActive {
					entry.node = digest.Path
				}
			}
		}
		entries[entry.tool.Name] = entry
	}
	return entries, nil
}
func (p *engineMCP) List(ctx context.Context, principal mcpauth.Principal) ([]mcpserver.Tool, error) {
	entries, err := p.catalog(ctx, principal)
	if err != nil {
		return nil, err
	}
	out := make([]mcpserver.Tool, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (p *engineMCP) Call(ctx context.Context, principal mcpauth.Principal, name string, raw json.RawMessage) (resultValue any, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	entries, err := p.catalog(ctx, principal)
	if err != nil {
		return nil, err
	}
	entry, ok := entries[name]
	if !ok {
		return nil, mcpFault("Tool is unavailable with this access.")
	}
	args, err := toolruntime.ValidateInput(entry.tool.InputSchema, raw)
	if err != nil {
		return nil, err
	}
	ctx = model.WithUserID(ctx, principal.UserID)
	value := func(key string) string { s, _ := args[key].(string); return s }
	switch name {
	case "agentize_create_conversation":
		return p.e.CreateConversation(CreateConversationInput{UserID: principal.UserID, Title: value("title")})
	case "agentize_list_conversations":
		conversations, err := p.e.ListConversations(principal.UserID)
		if err != nil {
			return nil, err
		}
		offset, limit := 0, 20
		if n, ok := args["offset"].(float64); ok {
			if n > float64(len(conversations)) {
				n = float64(len(conversations))
			}
			offset = int(n)
		}
		if n, ok := args["limit"].(float64); ok {
			limit = int(n)
		}
		end := offset + limit
		if end > len(conversations) {
			end = len(conversations)
		}
		return map[string]any{"conversations": conversations[offset:end], "next_offset": end, "total": len(conversations)}, nil
	case "agentize_get_conversation":
		conv, err := p.e.GetConversation(principal.UserID, value("conversation_id"))
		if err != nil || conv == nil {
			return nil, mcpFault("Conversation is unavailable.")
		}
		session, err := p.e.loadOwnedSession(principal.UserID, conv.SessionID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"conversation": conv, "messages": session.Msgs}, nil
	case "agentize_send_message":
		conv, err := p.e.GetConversation(principal.UserID, value("conversation_id"))
		if err != nil || conv == nil || conv.Archived {
			return nil, mcpFault("Conversation is unavailable or archived.")
		}
		response, tokens, err := p.e.ProcessConversation(ctx, principal.UserID, value("conversation_id"), value("message"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"message": response, "tokens": tokens}, nil
	case "agentize_upload_file":
		conv, err := p.e.GetConversation(principal.UserID, value("conversation_id"))
		if err != nil || conv == nil {
			return nil, mcpFault("Conversation is unavailable.")
		}
		bytes, err := base64.StdEncoding.DecodeString(value("data_base64"))
		if err != nil || len(bytes) > 512<<10 {
			return nil, mcpFault("Invalid file or file exceeds 512 KiB.")
		}
		file, err := p.e.RecordUserFileInSession(principal.UserID, conv.SessionID, value("name"), value("mime_type"), model.FileSourceUploaded, bytes)
		if err != nil {
			return nil, err
		}
		return mcpFileView(file), nil
	case "agentize_list_files":
		files, err := p.e.ListUserFiles(principal.UserID)
		if err != nil {
			return nil, err
		}
		offset, limit := 0, 20
		if n, ok := args["offset"].(float64); ok {
			if n > float64(len(files)) {
				n = float64(len(files))
			}
			offset = int(n)
		}
		if n, ok := args["limit"].(float64); ok {
			limit = int(n)
		}
		end := offset + limit
		if end > len(files) {
			end = len(files)
		}
		views := []map[string]any{}
		for _, file := range files[offset:end] {
			views = append(views, mcpFileView(file))
		}
		return map[string]any{"files": views, "next_offset": end, "total": len(files)}, nil
	case "agentize_read_file":
		meta, err := p.e.Sessions.GetUserFileForUser(principal.UserID, value("file_id"))
		if err != nil || meta == nil {
			return nil, mcpFault("File is unavailable.")
		}
		reader, err := p.e.Files.Open(meta.StorageKey)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		bytes, err := io.ReadAll(io.LimitReader(reader, (512<<10)+1))
		if err != nil || len(bytes) > 512<<10 {
			return nil, mcpFault("File exceeds 512 KiB.")
		}
		return map[string]any{"file": mcpFileView(meta), "data_base64": base64.StdEncoding.EncodeToString(bytes)}, nil
	}
	if len(raw) > toolruntime.MaxArgumentsBytes {
		return nil, mcpFault("Tool arguments exceed the size limit.")
	}
	name = entry.function
	conv, err := p.e.GetConversation(principal.UserID, value("agentize_conversation_id"))
	if err != nil || conv == nil || conv.Archived {
		return nil, mcpFault("Conversation is unavailable.")
	}
	delete(args, "agentize_conversation_id")
	lock := p.e.getSessionMutex(p.e.sessionKey(ctx, conv.SessionID))
	if !lock.TryLock() {
		return nil, mcpFault("Conversation is busy; try again when its current turn finishes.")
	}
	defer lock.Unlock()
	claimer, ok := p.e.Sessions.(interface {
		ClaimMCPToolRun(context.Context, string, string, string) (*model.SessionRun, error)
	})
	if !ok {
		return nil, mcpFault("Durable tool admission is unavailable for this store.")
	}
	run, err := claimer.ClaimMCPToolRun(ctx, principal.UserID, conv.SessionID, name)
	if err != nil {
		return nil, mcpFault("Conversation is busy or durable admission is unavailable.")
	}
	defer func() {
		if recover() != nil {
			resultValue = nil
			resultErr = mcpFault("Command outcome must be reconciled; do not retry automatically.")
		}
		status := model.SessionRunSucceeded
		if resultErr != nil {
			status = model.SessionRunFailed
		}
		if err := p.e.Sessions.FinalizeSessionRun(principal.UserID, conv.SessionID, run.RunID, run.Fence, status, ""); err != nil {
			resultValue = nil
			resultErr = mcpFault("Command outcome must be reconciled; do not retry automatically.")
		}
	}()
	session, err := p.e.loadOwnedSession(principal.UserID, conv.SessionID)
	if err != nil {
		return nil, err
	}
	if entry.node != "" {
		node, err := p.e.executableKnowledgeNode(principal.UserID, entry.node)
		if err != nil {
			return nil, err
		}
		found := false
		for _, d := range session.NodeDigests {
			if d.Path == entry.node {
				found = true
			}
		}
		if !found {
			session.NodeDigests = append(session.NodeDigests, summarizeNode(node))
			if err := p.e.Sessions.Put(session); err != nil {
				return nil, err
			}
		}
	}
	if err := p.e.CheckKnowledgeToolAccess(principal.UserID, conv.SessionID, name); err != nil {
		return nil, err
	}
	args["__user_id__"] = principal.UserID
	args["__session_id__"] = conv.SessionID
	if name == "manage_files" {
		args["__file_context__"] = ctx
	}
	event := &UsageEvent{UserID: principal.UserID, SessionID: conv.SessionID, EventType: EventToolCall, Name: name, Metadata: toolActionMetadata(args)}
	if p.e.Callback != nil {
		if err := p.e.Callback.BeforeAction(ctx, event); err != nil {
			return nil, err
		}
	}
	started := time.Now()
	if _, err := AwaitToolApproval(ctx, p.e.ToolApprovalManager, ToolApprovalRequest{RefID: uuid.NewString(), UserID: principal.UserID, SessionID: conv.SessionID, AgentType: session.AgentType, ToolName: name, Arguments: string(raw)}); err != nil {
		return nil, err
	}
	if p.e.Executor == nil {
		return nil, errors.New("tool executor is unavailable")
	}
	result, err := p.e.Executor(name, args)
	if p.e.Callback != nil {
		event.Duration = time.Since(started)
		event.Error = err
		if usage, ok := args[usageArgKey].(*model.ImageEditResult); ok && usage != nil {
			event.Model = usage.Model
			event.InputTokens = usage.InputTokens
			event.OutputTokens = usage.OutputTokens
			event.Tokens = usage.InputTokens + usage.OutputTokens
			if event.Metadata == nil {
				event.Metadata = map[string]any{}
			}
			event.Metadata["media"] = "image"
		}
		p.e.Callback.AfterAction(ctx, event)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": result}, nil
}
func mcpFileView(file *model.UserFile) map[string]any {
	return map[string]any{"file_id": file.FileID, "name": file.Name, "mime_type": file.MIMEType, "size": file.Size}
}
