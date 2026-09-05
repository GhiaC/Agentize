package engine

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// errManageFiles is a sentinel marking a manage_files action that did not
// succeed (the user-facing reason is carried in the returned string). It exists
// so the dispatcher can record an accurate ok|error status metric per action.
var errManageFiles = errors.New("manage_files: operation did not succeed")

// maxFileReadChars caps how much text content the read action returns to the LLM
// so a large file cannot blow up the context window.
const maxFileReadChars = 8000

// maxFileListItems caps how many files the list action enumerates.
const maxFileListItems = 50

// maxGrepMatches caps how many matching lines the grep action returns.
const maxGrepMatches = 100

// FileOpener interface defines the contract for file operations
// This allows the Engine to be used for file operations without direct coupling
type FileOpener interface {
	OpenFile(sessionID string, path string) (string, error)
	CloseFile(sessionID string, path string) error
}

// Ensure Engine implements FileOpener
var _ FileOpener = (*Engine)(nil)

// RegisterFileTools registers the node-context operations. Legacy file-named
// aliases remain executable for persisted tool calls during migration.
func (e *Engine) RegisterFileTools(registry *model.FunctionRegistry) {
	if registry == nil {
		return
	}

	openNode, closeNode := e.createOpenFileFunction(), e.createCloseFileFunction()
	registry.RegisterOrReplace("open_node", "Open Node", openNode)
	registry.RegisterOrReplace("close_node", "Close Node", closeNode)
	registry.RegisterOrReplace("open_file", "Open Node (legacy alias)", openNode)
	registry.RegisterOrReplace("close_file", "Close Node (legacy alias)", closeNode)
}

// createOpenFileFunction creates the open_file tool function
func (e *Engine) createOpenFileFunction() model.ToolFunction {
	return func(args map[string]interface{}) (string, error) {
		path, err := getStringArg(args, "path")
		if err != nil {
			return "", err
		}

		// Get session ID from injected context
		sessionID, _ := args["__session_id__"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("session ID not available")
		}

		content, err := e.openFile(stringArg(args, "__user_id__"), sessionID, path)
		metrics.KnowledgeOpen(metrics.Status(err))
		if err != nil {
			return fmt.Sprintf("Error opening node: %v", err), nil
		}

		payload := map[string]interface{}{"path": path, "opened": true, "content": content}
		if e.Repo != nil {
			if node, loadErr := e.Repo.LoadNode(path); loadErr == nil {
				payload["title"] = node.Title
				payload["description"] = node.Description
				payload["summary"] = node.Summary
				payload["activated_tools"] = activeNodeToolNames(node)
			}
		}
		if session, getErr := e.loadOwnedSession(stringArg(args, "__user_id__"), sessionID); getErr == nil && session != nil {
			openPaths := make([]string, 0, len(session.NodeDigests))
			for _, digest := range session.NodeDigests {
				if digest.Path != "" {
					openPaths = append(openPaths, digest.Path)
				}
			}
			payload["open_nodes"] = openPaths
			payload["closed_previous"] = false
		}
		return boundedKnowledgeJSON(payload), nil
	}
}

// createCloseFileFunction creates the close_file tool function
func (e *Engine) createCloseFileFunction() model.ToolFunction {
	return func(args map[string]interface{}) (string, error) {
		path, err := getStringArg(args, "path")
		if err != nil {
			return "", err
		}

		// Get session ID from injected context
		sessionID, _ := args["__session_id__"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("session ID not available")
		}

		err = e.closeFile(stringArg(args, "__user_id__"), sessionID, path)
		if err != nil {
			return fmt.Sprintf("Error closing node: %v", err), nil
		}

		return boundedKnowledgeJSON(map[string]interface{}{"path": path, "opened": false}), nil
	}
}

// CreateFileToolsWithOpener creates file tool functions using a custom FileOpener
// This is useful for integrating with different file management systems
func CreateFileToolsWithOpener(opener FileOpener) (openFile, closeFile model.ToolFunction) {
	openFile = func(args map[string]interface{}) (string, error) {
		path, err := getStringArg(args, "path")
		if err != nil {
			return "", err
		}

		sessionID, _ := args["__session_id__"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("session ID not available")
		}

		content, err := opener.OpenFile(sessionID, path)
		if err != nil {
			return fmt.Sprintf("Error opening file: %v", err), nil
		}

		return fmt.Sprintf("File opened successfully. Content length: %d characters. The file is now available in your context.", len(content)), nil
	}

	closeFile = func(args map[string]interface{}) (string, error) {
		path, err := getStringArg(args, "path")
		if err != nil {
			return "", err
		}

		sessionID, _ := args["__session_id__"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("session ID not available")
		}

		err = opener.CloseFile(sessionID, path)
		if err != nil {
			return fmt.Sprintf("Error closing file: %v", err), nil
		}

		return fmt.Sprintf("File closed successfully: %s", path), nil
	}

	return openFile, closeFile
}

// Helper function to extract string argument from tool args
func getStringArg(args map[string]interface{}, key string) (string, error) {
	val, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument: %s", key)
	}

	strVal, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("argument %s must be a string", key)
	}

	return strVal, nil
}

// GetFileToolDefinitions returns the JSON schema definitions for open_file and close_file tools
// These can be added to a node's tools.json
func GetFileToolDefinitions() []model.Tool {
	return []model.Tool{
		{
			Name:        "open_node",
			Description: "Opens a knowledge-tree node by path and adds its content and tools to this session. Previously opened nodes stay open; this does not close them.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "The path to the file/node in the knowledge tree (e.g., 'root/kubernetes/pods')",
					},
				},
				"required": []string{"path"},
			},
			Status: "active",
		},
		{
			Name:        "close_node",
			Description: "Closes a previously opened knowledge-tree node and removes its content and node-owned tools from this session context.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "The path to the file/node to close",
					},
				},
				"required": []string{"path"},
			},
			Status: "active",
		},
	}
}

func OpenNodeToolDefinition() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name:        "open_node",
		Description: "Open a knowledge-tree node by path. Returns that node's content and activates its tools in addition to any nodes already open. Previously opened nodes stay open; this does not close them. A compact usage catalog of every currently open node is kept in the system prompt. Full node content is returned here, not copied into the prompt. This is the knowledge tree, not the user's product memory/journal.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Node path such as root/trading or root/market-data.",
				},
			},
			"required": []string{"path"},
		},
	}}
}

func CloseNodeToolDefinition() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name:        "close_node",
		Description: "Close a previously opened knowledge-tree node and deactivate its node-owned tools. Root cannot be closed.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Node path to close.",
				},
			},
			"required": []string{"path"},
		},
	}}
}

// ============================================================================
// manage_files — single user-file tool for the agent
// ============================================================================

// injectImageArgKey is the shared-args key a tool uses to hand an image back to
// the engine loop so it can be injected into the conversation as a vision message.
const injectImageArgKey = "__inject_image__"

// InjectToolImage attaches image bytes to the current host tool call so the next
// LLM request sees them as a vision message. Call it on the same args map the
// tool received. The image is not written to the file store and is not persisted
// in transcript history. Empty data is a no-op.
func InjectToolImage(args map[string]any, name, mimeType string, data []byte) {
	if args == nil || len(data) == 0 {
		return
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" || !strings.HasPrefix(mimeType, "image/") {
		mimeType = "image/png"
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "image"
	}
	args[injectImageArgKey] = &injectedImage{
		DataURL: fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data)),
		Name:    name,
	}
}

// HasInjectedToolImage reports whether InjectToolImage (or manage_files image
// read) stashed a vision payload on args.
func HasInjectedToolImage(args map[string]any) bool {
	if args == nil {
		return false
	}
	inj, ok := args[injectImageArgKey].(*injectedImage)
	return ok && inj != nil && inj.DataURL != ""
}

// usageArgKey is the shared-args key a tool uses to hand model/token usage back
// to executeTool so the tool's billing/usage event carries the real cost (used
// by edit_image, whose cost is the underlying image-model call).
const usageArgKey = "__usage__"

// injectedImage carries an image to be added to the LLM request as a multimodal
// message (so vision models can see it). It is never persisted to history.
type injectedImage struct {
	DataURL string
	Name    string
}

// message builds the multimodal user message that carries the image.
func (im *injectedImage) message() openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		MultiContent: []openai.ChatMessagePart{
			{Type: openai.ChatMessagePartTypeText, Text: fmt.Sprintf("[Opened image %q for inspection/editing:]", im.Name)},
			{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: im.DataURL, Detail: openai.ImageURLDetailAuto}},
		},
	}
}

// ManageFilesToolDefinition returns the OpenAI tool schema for the manage_files
// tool: one tool with an action parameter covering the full file lifecycle.
func ManageFilesToolDefinition() openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name: "manage_files",
			Description: "Manage the current user's files (documents they sent or that were generated for them). Actions:\n" +
				"- list: filter, sort, and paginate the user's files (file_id, name, type, size, summary).\n" +
				"- read: load a file by file_id. Text files return their content (use offset/limit lines for large files); image files are loaded into your vision context so you can see them.\n" +
				"- grep: search text files for 'query' (regex) and return matching lines with line numbers; pass file_id to search one file, omit it to search all the user's text files.\n" +
				"- save: store 'content' as a new text file named 'name'; returns the new file_id.\n" +
				"- edit: edit a text file in place — replace text, replace a line range, or overwrite the whole file.\n" +
				"- edit_image: edit an image file (file_id) per 'instruction' using an image model; the edited image is saved as a NEW independent file and its file_id is returned.\n" +
				"- delete: permanently delete an owned file's bytes and metadata.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"list", "read", "grep", "save", "edit", "edit_image", "delete"},
						"description": "The operation to perform.",
					},
					"file_id": map[string]interface{}{
						"type":        "string",
						"description": "Target file id. Required for read, edit, edit_image, and delete; optional for grep.",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Filename. Used for save (required) and optionally edit_image (name of the new file).",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "UTF-8 text content. Used for save, or for edit to overwrite the whole file.",
					},
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search pattern (regex, falls back to literal) for action=grep.",
					},
					"old_string": map[string]interface{}{
						"type":        "string",
						"description": "Exact text to find for action=edit (search/replace).",
					},
					"new_string": map[string]interface{}{
						"type":        "string",
						"description": "Replacement text for action=edit.",
					},
					"replace_all": map[string]interface{}{
						"type":        "boolean",
						"description": "Replace all occurrences in action=edit (default false: old_string must be unique).",
					},
					"instruction": map[string]interface{}{
						"type":        "string",
						"description": "Edit instruction for action=edit_image (e.g. 'remove the background').",
					},
					"offset": map[string]interface{}{
						"type":        "integer",
						"description": "Starting line (1-based) for action=read on large text files.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Number of lines to read from offset for action=read.",
					},
					"filter": map[string]interface{}{
						"type": "string", "description": "Case-insensitive name/type/source/summary filter for action=list.",
					},
					"sort_by": map[string]interface{}{
						"type": "string", "enum": []string{"created_at", "name", "size", "type"}, "description": "Sort field for action=list (default created_at).",
					},
					"sort_order": map[string]interface{}{
						"type": "string", "enum": []string{"asc", "desc"}, "description": "Sort direction for action=list (default desc).",
					},
					"page": map[string]interface{}{
						"type": "integer", "description": "1-based page for action=list.",
					},
					"page_size": map[string]interface{}{
						"type": "integer", "description": "Items per page for action=list (1-50).",
					},
					"start_line": map[string]interface{}{
						"type": "integer", "description": "First 1-based line to replace for action=edit.",
					},
					"end_line": map[string]interface{}{
						"type": "integer", "description": "Last 1-based line to replace for action=edit.",
					},
				},
				"required": []string{"action"},
			},
		},
	}
}

// RegisterManageFilesTool registers the manage_files tool function on the
// engine's function registry. No-op if the registry is not configured.
func (e *Engine) RegisterManageFilesTool() {
	if e.Functions == nil {
		return
	}
	_ = e.Functions.RegisterOrReplace("manage_files", "مدیریت فایل‌ها", e.manageFilesFunction())
}

// manageFilesFunction builds the manage_files tool implementation.
func (e *Engine) manageFilesFunction() model.ToolFunction {
	return func(args map[string]interface{}) (string, error) {
		userID, _ := args["__user_id__"].(string)
		sessionID, _ := args["__session_id__"].(string)

		action, err := getStringArg(args, "action")
		if err != nil {
			return "", err
		}

		start := time.Now()
		switch strings.ToLower(strings.TrimSpace(action)) {
		case "list":
			s, opErr := e.manageFilesList(userID, args)
			return recordFileOp("list", start, s, opErr)
		case "read":
			// manageFilesRead records its own metric (it returns an image, not an error).
			text, inject := e.manageFilesRead(userID, args)
			if inject != nil {
				args[injectImageArgKey] = inject
			}
			return text, nil
		case "grep":
			s, opErr := e.manageFilesGrep(userID, args)
			return recordFileOp("grep", start, s, opErr)
		case "save":
			s, opErr := e.manageFilesSave(sessionID, args)
			return recordFileOp("save", start, s, opErr)
		case "edit":
			s, opErr := e.manageFilesEdit(userID, args)
			return recordFileOp("edit", start, s, opErr)
		case "edit_image":
			s, opErr := e.manageFilesEditImage(userID, sessionID, args)
			return recordFileOp("edit_image", start, s, opErr)
		case "delete":
			s, opErr := e.manageFilesDelete(userID, args)
			return recordFileOp("delete", start, s, opErr)
		default:
			return fmt.Sprintf("Unknown action %q. Valid actions: list, read, grep, save, edit, edit_image, delete.", action), nil
		}
	}
}

// recordFileOp emits the file-operation metric and returns the user-facing string
// to the LLM with a nil error (the friendly reason is already inside result).
func recordFileOp(op string, start time.Time, result string, opErr error) (string, error) {
	metrics.FileOp(op, metrics.Status(opErr), time.Since(start))
	return result, nil
}

// manageFilesList returns a compact listing of the user's files.
func (e *Engine) manageFilesList(userID string, args map[string]interface{}) (string, error) {
	files, err := e.ListUserFiles(userID)
	if err != nil {
		return fmt.Sprintf("Error listing files: %v", err), err
	}
	filter := strings.ToLower(strings.TrimSpace(stringArg(args, "filter")))
	if filter != "" {
		filtered := files[:0]
		for _, f := range files {
			haystack := strings.ToLower(strings.Join([]string{f.Name, f.MIMEType, string(f.Source), f.Summary}, " "))
			if strings.Contains(haystack, filter) {
				filtered = append(filtered, f)
			}
		}
		files = filtered
	}
	if len(files) == 0 {
		return "You have no files yet.", nil
	}
	sortBy := stringArg(args, "sort_by")
	if sortBy == "" {
		sortBy = "created_at"
	}
	desc := stringArg(args, "sort_order") != "asc"
	sort.SliceStable(files, func(i, j int) bool {
		cmp := 0
		switch sortBy {
		case "name":
			cmp = strings.Compare(strings.ToLower(files[i].Name), strings.ToLower(files[j].Name))
		case "size":
			if files[i].Size < files[j].Size {
				cmp = -1
			} else if files[i].Size > files[j].Size {
				cmp = 1
			}
		case "type":
			cmp = strings.Compare(files[i].MIMEType, files[j].MIMEType)
		default:
			if files[i].CreatedAt.Before(files[j].CreatedAt) {
				cmp = -1
			} else if files[i].CreatedAt.After(files[j].CreatedAt) {
				cmp = 1
			}
		}
		if cmp == 0 {
			cmp = strings.Compare(files[i].FileID, files[j].FileID)
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
	page, pageSize := intArg(args, "page"), intArg(args, "page_size")
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > maxFileListItems {
		pageSize = maxFileListItems
	}
	total := len(files)
	totalPages := (total + pageSize - 1) / pageSize
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Files page %d (%d-%d of %d):\n", page, start+1, end, total))
	for _, f := range files[start:end] {
		summary := f.Summary
		if summary == "" {
			summary = "-"
		}
		derived := ""
		if f.ParentFileID != "" {
			derived = fmt.Sprintf(" | from=%s", f.ParentFileID)
		}
		b.WriteString(fmt.Sprintf("- id=%s | name=%s | type=%s | %d bytes | %s%s | %s\n",
			f.FileID, f.Name, f.MIMEType, f.Size, f.Source, derived, summary))
	}
	if end < total {
		b.WriteString(fmt.Sprintf("Next page: page=%d (remaining %d).\n", page+1, total-end))
	}
	return b.String(), nil
}

// manageFilesRead returns the content of a file, enforcing ownership. Text files
// return their (optionally line-ranged) content; image files are returned as an
// injected vision message so the model can actually see them.
func (e *Engine) manageFilesRead(userID string, args map[string]interface{}) (string, *injectedImage) {
	start := time.Now()
	status := "ok"
	defer func() { metrics.FileOp("read", status, time.Since(start)) }()

	fileID, _ := args["file_id"].(string)
	if fileID == "" {
		status = "error"
		return "Error: file_id is required for action=read.", nil
	}
	meta, errMsg := e.getOwnedFileMeta(userID, fileID)
	if errMsg != "" {
		status = "error"
		return errMsg, nil
	}
	data, _, err := e.ReadUserFile(fileID)
	if err != nil {
		status = "error"
		return fmt.Sprintf("Error reading file: %v", err), nil
	}

	// Images: hand back as a vision message so the model can see them.
	if strings.HasPrefix(meta.MIMEType, "image/") {
		if e.ImageEditor == nil {
			// Still let the model see it; editing just won't be available.
			log.Log.Infof("[Engine] manage_files read image without editor configured | file=%s", meta.FileID)
		}
		dataURL := fmt.Sprintf("data:%s;base64,%s", meta.MIMEType, base64.StdEncoding.EncodeToString(data))
		msg := fmt.Sprintf("Loaded image %s (%s, %d bytes) into your context. To edit it, use action=edit_image with file_id=%s.",
			meta.Name, meta.MIMEType, meta.Size, meta.FileID)
		return msg, &injectedImage{DataURL: dataURL, Name: meta.Name}
	}

	if !isTextMIME(meta.MIMEType) {
		return fmt.Sprintf("File %s (%s, %d bytes) is a binary file and cannot be shown as text.",
			meta.Name, meta.MIMEType, meta.Size), nil
	}

	content := string(data)

	// Optional line range for large text files.
	offset := intArg(args, "offset")
	limit := intArg(args, "limit")
	if offset > 0 || limit > 0 {
		lines := strings.Split(content, "\n")
		start := offset - 1
		if start < 0 {
			start = 0
		}
		if start > len(lines) {
			start = len(lines)
		}
		end := len(lines)
		if limit > 0 && start+limit < end {
			end = start + limit
		}
		slice := strings.Join(lines[start:end], "\n")
		return fmt.Sprintf("File %s (%s, %d bytes), lines %d-%d of %d:\n\n%s",
			meta.Name, meta.MIMEType, meta.Size, start+1, end, len(lines), slice), nil
	}

	if len(content) > maxFileReadChars {
		return fmt.Sprintf("File %s (%s, %d bytes), showing first %d characters:\n\n%s\n\n[truncated: %d of %d bytes shown — use offset/limit to read more]",
			meta.Name, meta.MIMEType, meta.Size, maxFileReadChars, content[:maxFileReadChars], maxFileReadChars, meta.Size), nil
	}
	return fmt.Sprintf("File %s (%s, %d bytes):\n\n%s", meta.Name, meta.MIMEType, meta.Size, content), nil
}

// manageFilesGrep searches the user's text files for a pattern and returns the
// matching lines with file id and line number.
func (e *Engine) manageFilesGrep(userID string, args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "Error: query is required for action=grep.", errManageFiles
	}

	// Compile as regex; fall back to a literal-substring matcher.
	var matcher func(string) bool
	if re, err := regexp.Compile(query); err == nil {
		matcher = re.MatchString
	} else {
		matcher = func(line string) bool { return strings.Contains(line, query) }
	}

	// Determine the set of files to search.
	var files []*model.UserFile
	if fileID, _ := args["file_id"].(string); fileID != "" {
		meta, errMsg := e.getOwnedFileMeta(userID, fileID)
		if errMsg != "" {
			return errMsg, errManageFiles
		}
		files = []*model.UserFile{meta}
	} else {
		all, err := e.ListUserFiles(userID)
		if err != nil {
			return fmt.Sprintf("Error listing files: %v", err), err
		}
		files = all
	}

	var b strings.Builder
	matches := 0
	for _, f := range files {
		if !isTextMIME(f.MIMEType) {
			continue
		}
		data, _, err := e.ReadUserFile(f.FileID)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if matcher(line) {
				matches++
				b.WriteString(fmt.Sprintf("%s:%d: %s\n", f.FileID, i+1, strings.TrimRight(line, "\r")))
				if matches >= maxGrepMatches {
					b.WriteString(fmt.Sprintf("... (stopped at %d matches)\n", maxGrepMatches))
					return b.String(), nil
				}
			}
		}
	}
	if matches == 0 {
		return fmt.Sprintf("No matches for %q.", query), nil
	}
	return b.String(), nil
}

// manageFilesSave stores new text content as a generated file for the user.
func (e *Engine) manageFilesSave(sessionID string, args map[string]interface{}) (string, error) {
	name, _ := args["name"].(string)
	content, _ := args["content"].(string)
	if name == "" {
		return "Error: name is required for action=save.", errManageFiles
	}
	uf, err := e.RecordUserFile(sessionID, name, "", model.FileSourceGenerated, []byte(content))
	if err != nil {
		return fmt.Sprintf("Error saving file: %v", err), err
	}
	return fmt.Sprintf("Saved file %s (id=%s, %s, %d bytes).", uf.Name, uf.FileID, uf.MIMEType, uf.Size), nil
}

// manageFilesEdit edits a text file in place: search/replace, or full overwrite.
func (e *Engine) manageFilesEdit(userID string, args map[string]interface{}) (string, error) {
	fileID, _ := args["file_id"].(string)
	if fileID == "" {
		return "Error: file_id is required for action=edit.", errManageFiles
	}
	meta, errMsg := e.getOwnedFileMeta(userID, fileID)
	if errMsg != "" {
		return errMsg, errManageFiles
	}
	if !isTextMIME(meta.MIMEType) {
		return fmt.Sprintf("File %s (%s) is not a text file and cannot be edited as text. For images use action=edit_image.", meta.Name, meta.MIMEType), errManageFiles
	}

	data, _, err := e.ReadUserFile(fileID)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err), err
	}
	content := string(data)

	oldStr, _ := args["old_string"].(string)
	newStr, _ := args["new_string"].(string)
	replaceAll, _ := args["replace_all"].(bool)

	var updated string
	switch {
	case oldStr != "":
		count := strings.Count(content, oldStr)
		if count == 0 {
			return "Error: old_string not found in the file.", errManageFiles
		}
		if count > 1 && !replaceAll {
			return fmt.Sprintf("Error: old_string appears %d times; pass replace_all=true or provide a more specific old_string.", count), errManageFiles
		}
		if replaceAll {
			updated = strings.ReplaceAll(content, oldStr, newStr)
		} else {
			updated = strings.Replace(content, oldStr, newStr, 1)
		}
	case intArg(args, "start_line") > 0:
		startLine, endLine := intArg(args, "start_line"), intArg(args, "end_line")
		if endLine == 0 {
			endLine = startLine
		}
		lines := strings.Split(content, "\n")
		if startLine < 1 || endLine < startLine || endLine > len(lines) {
			return fmt.Sprintf("Error: line range %d-%d is outside 1-%d.", startLine, endLine, len(lines)), errManageFiles
		}
		replacement, _ := args["content"].(string)
		replLines := strings.Split(replacement, "\n")
		merged := append([]string{}, lines[:startLine-1]...)
		merged = append(merged, replLines...)
		merged = append(merged, lines[endLine:]...)
		updated = strings.Join(merged, "\n")
	case args["content"] != nil:
		// Full overwrite.
		updated, _ = args["content"].(string)
	default:
		return "Error: provide old_string (+ new_string) for a targeted edit, or content to overwrite the whole file.", errManageFiles
	}

	uf, err := e.UpdateUserFileContent(fileID, []byte(updated))
	if err != nil {
		return fmt.Sprintf("Error writing file: %v", err), err
	}
	return fmt.Sprintf("Edited %s (id=%s, now %d bytes).", uf.Name, uf.FileID, uf.Size), nil
}

func stringArg(args map[string]interface{}, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

// manageFilesEditImage edits an image via the configured ImageEditor and stores
// the result as a new independent file.
func (e *Engine) manageFilesEditImage(userID, sessionID string, args map[string]interface{}) (string, error) {
	if e.ImageEditor == nil {
		return "Image editing is not configured on this deployment.", errManageFiles
	}
	fileID, _ := args["file_id"].(string)
	instruction, _ := args["instruction"].(string)
	if fileID == "" {
		return "Error: file_id is required for action=edit_image.", errManageFiles
	}
	if instruction == "" {
		return "Error: instruction is required for action=edit_image.", errManageFiles
	}
	meta, errMsg := e.getOwnedFileMeta(userID, fileID)
	if errMsg != "" {
		return errMsg, errManageFiles
	}
	if !strings.HasPrefix(meta.MIMEType, "image/") {
		return fmt.Sprintf("File %s is not an image (%s).", meta.Name, meta.MIMEType), errManageFiles
	}
	name, _ := args["name"].(string)

	uf, result, err := e.EditImageFile(sessionID, fileID, instruction, name)
	if err != nil {
		return fmt.Sprintf("Error editing image: %v", err), err
	}
	// Hand the image-model usage to executeTool so this edit_image call's
	// billing event carries the real model + token cost (not a zero-cost tool).
	if result != nil {
		args[usageArgKey] = result
	}
	return fmt.Sprintf("Edited image saved as a NEW file %s (id=%s, %s, %d bytes), derived from %s.",
		uf.Name, uf.FileID, uf.MIMEType, uf.Size, meta.FileID), nil
}

// manageFilesDelete permanently removes an owned file's bytes and metadata.
func (e *Engine) manageFilesDelete(userID string, args map[string]interface{}) (string, error) {
	if e.Files == nil {
		return "File storage is not configured on this deployment.", errManageFiles
	}
	fileID, _ := args["file_id"].(string)
	if fileID == "" {
		return "Error: file_id is required for action=delete.", errManageFiles
	}
	meta, errMsg := e.getOwnedFileMeta(userID, fileID)
	if errMsg != "" {
		return errMsg, errManageFiles
	}
	if err := e.Files.Delete(meta.StorageKey); err != nil {
		return fmt.Sprintf("Error deleting file bytes: %v", err), err
	}
	st, _ := e.userFiles()
	if err := st.DeleteUserFile(meta.FileID); err != nil {
		return fmt.Sprintf("Error deleting file metadata: %v", err), err
	}
	return fmt.Sprintf("Deleted file %s (id=%s).", meta.Name, meta.FileID), nil
}

// getOwnedFileMeta loads a file's metadata and verifies the user owns it.
// Returns a non-empty error string (for the LLM) when not found or not owned.
func (e *Engine) getOwnedFileMeta(userID, fileID string) (*model.UserFile, string) {
	st, ok := e.userFiles()
	if !ok {
		return nil, "user files are not supported on this deployment."
	}
	meta, err := st.GetUserFile(fileID)
	if err != nil {
		return nil, fmt.Sprintf("Error loading file: %v", err)
	}
	if meta == nil {
		return nil, fmt.Sprintf("File not found: %s", fileID)
	}
	// Strict ownership: the file's owner must exactly equal the caller. Both are
	// derived from session.UserID (file owner set at RecordUserFile, caller
	// injected by executeTool), so a legitimate owner always matches — including
	// the single-tenant/no-auth case where both are "". A blanket `userID != ""`
	// escape hatch is intentionally NOT used: it would let a request that arrives
	// with an empty user id read ANY user's file by id.
	if meta.UserID != userID {
		return nil, "Error: this file does not belong to you."
	}
	return meta, ""
}

// intArg extracts an integer from a JSON-decoded arg (numbers arrive as float64).
func intArg(args map[string]interface{}, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
