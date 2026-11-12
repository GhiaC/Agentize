// Package mcpserver serves authenticated, stateless MCP over a single HTTP endpoint.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"

	"time"

	"github.com/ghiac/agentize/mcpauth"
	"github.com/ghiac/agentize/toolruntime"
	"github.com/google/uuid"
)

const maxBody = 1 << 20

type principalKey struct{}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Provider discovers and executes capabilities using the authenticated principal.
// Implementations must enforce ownership, current ACLs, scopes and approval gates.
type Provider interface {
	List(context.Context, mcpauth.Principal) ([]Tool, error)
	Call(context.Context, mcpauth.Principal, string, json.RawMessage) (any, error)
}

type Server struct{ provider Provider }

func NewProvider(provider Provider) (*Server, error) {
	if provider == nil {
		return nil, errors.New("mcp server: provider is required")
	}
	return &Server{provider: provider}, nil
}

// New preserves the original research profile for existing hosts.
func New(rt *toolruntime.Runtime) (*Server, error) { return NewProfile(rt, "research") }
func NewProfile(rt *toolruntime.Runtime, profile string) (*Server, error) {
	if rt == nil || profile == "" {
		return nil, errors.New("mcp server: runtime and profile are required")
	}
	return NewProvider(runtimeProvider{rt, profile})
}

// RuntimeProvider adapts a reviewed transport-independent catalog for composition.
func RuntimeProvider(rt *toolruntime.Runtime, profile string) Provider {
	return runtimeProvider{rt, profile}
}

type runtimeProvider struct {
	rt      *toolruntime.Runtime
	profile string
}

func (p runtimeProvider) List(_ context.Context, principal mcpauth.Principal) ([]Tool, error) {
	tools := []Tool{}
	if p.rt == nil || p.profile == "" {
		return nil, errors.New("runtime unavailable")
	}
	for _, d := range p.rt.List(runtimePrincipal(principal), p.profile) {
		tools = append(tools, Tool{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema})
	}
	return tools, nil
}
func (p runtimeProvider) Call(ctx context.Context, principal mcpauth.Principal, name string, args json.RawMessage) (any, error) {
	result, err := p.rt.Execute(ctx, runtimePrincipal(principal), toolruntime.Invocation{Name: name, Profile: p.profile, Arguments: args})
	if err != nil {
		return nil, err
	}
	var data any
	if len(result.Data) > 0 {
		if err := json.Unmarshal(result.Data, &data); err != nil {
			return nil, err
		}
	}
	return data, nil
}
func runtimePrincipal(p mcpauth.Principal) toolruntime.Principal {
	return toolruntime.Principal{UserID: p.UserID, CredentialID: p.CredentialID, Scopes: p.Scopes, AllowInference: mcpauth.HasScope(p, "inference:execute")}
}

type rpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params"`
}

// Handler authenticates the bearer token and then serves one JSON-RPC message per POST.
// Both the usage ledger and pre-execution recorder are required. The handler
// fails closed when durable admission is unavailable.
func Handler(access *mcpauth.Service, server *Server, usage mcpauth.UsageStore) http.Handler {
	slots := make(chan struct{}, 64)
	var ownerMu sync.Mutex
	owners := map[string]int{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if access == nil || server == nil || usage == nil {
			writeAuth(w, http.StatusServiceUnavailable, "MCP is not configured.")
			return
		}
		if r.Header.Get("Origin") != "" {
			writeAuth(w, http.StatusForbidden, "Browser origins are not accepted on this bearer endpoint.")
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			writeAuth(w, http.StatusTooManyRequests, "MCP is busy.")
			return
		}

		token, ok := bearer(r.Header.Get("Authorization"))
		if !ok || len(token) > 512 {
			writeAuth(w, http.StatusUnauthorized, "A private MCP bearer token is required.")
			return
		}
		principal, err := access.Authenticate(r.Context(), token)
		if err != nil {
			switch {
			case errors.Is(err, mcpauth.ErrForbidden):
				writeAuth(w, http.StatusForbidden, "This credential cannot be used.")
			case errors.Is(err, mcpauth.ErrUnavailable):
				writeAuth(w, http.StatusServiceUnavailable, "Credential verification is unavailable.")
			default:
				writeAuth(w, http.StatusUnauthorized, "The MCP credential is invalid.")
			}
			return
		}
		ownerMu.Lock()
		if owners[principal.UserID] >= 4 {
			ownerMu.Unlock()
			writeAuth(w, http.StatusTooManyRequests, "Too many concurrent requests for this account.")
			return
		}
		owners[principal.UserID]++
		ownerMu.Unlock()
		defer func() {
			ownerMu.Lock()
			owners[principal.UserID]--
			if owners[principal.UserID] == 0 {
				delete(owners, principal.UserID)
			}
			ownerMu.Unlock()
		}()
		media, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mediaErr != nil || media != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		if !acceptsJSON(r.Header.Values("Accept")) {
			http.Error(w, "Accept must contain application/json", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil || len(body) > maxBody {
			http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
			return
		}
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
			writeRPC(w, nil, nil, &rpcError{Code: -32700, Message: "Parse error"})
			return
		}
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		ctx = context.WithValue(ctx, principalKey{}, principal)
		started := time.Now()
		var receipt toolruntime.Receipt
		if req.Method == "tools/call" {
			recorder, ok := usage.(toolruntime.Recorder)
			if !ok {
				writeRPC(w, req.ID, nil, &rpcError{Code: -32000, Message: "Durable audit is unavailable."})
				return
			}
			var call struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &call)
			// Tool names in admission are bounded metadata, never raw arguments.
			if len(call.Name) > 128 {
				writeRPC(w, req.ID, nil, &rpcError{Code: -32602, Message: "Invalid tool name."})
				return
			}
			offered, listErr := server.provider.List(ctx, principal)
			if listErr != nil {
				writeRPC(w, req.ID, nil, &rpcError{Code: -32000, Message: "Tool catalog is unavailable."})
				return
			}
			available := false
			for _, tool := range offered {
				if tool.Name == call.Name {
					available = true
					break
				}
			}
			if !available {
				writeRPC(w, req.ID, nil, &rpcError{Code: -32602, Message: "Tool is not available with this access."})
				return
			}
			receipt, err = recorder.Begin(ctx, toolruntime.Admission{RequestID: uuid.NewString(), UserID: principal.UserID, CredentialID: principal.CredentialID, Tool: call.Name, ToolVersion: "1", CatalogVersion: "dynamic", InferenceAllowed: mcpauth.HasScope(principal, "inference:execute")})
			if err != nil || receipt == nil {
				writeRPC(w, req.ID, nil, &rpcError{Code: -32000, Message: "Durable audit is unavailable."})
				return
			}
		}
		result, rpcErr := server.dispatch(ctx, req)
		auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
		defer auditCancel()
		if receipt != nil {
			outcome := "ok"
			if rpcErr != nil || toolFailed(result) {
				outcome = "error"
			}
			if err := receipt.Finish(auditCtx, outcome); err != nil {
				writeRPC(w, req.ID, nil, &rpcError{Code: -32000, Message: "Command outcome must be reconciled; do not retry automatically."})
				return
			}
		}
		if usage != nil {
			if err := usage.RecordUsage(auditCtx, measurement(principal, req, result, rpcErr, time.Since(started))); err != nil {
				writeRPC(w, req.ID, nil, &rpcError{Code: -32000, Message: "Usage could not be recorded."})
				return
			}
		}
		writeRPC(w, req.ID, result, rpcErr)
	})
}

func measurement(principal mcpauth.Principal, req rpcRequest, result any, rpcErr *rpcError, elapsed time.Duration) mcpauth.Usage {
	outcome := "ok"
	if rpcErr != nil {
		outcome = "error"
	} else if toolFailed(result) {
		outcome = "tool_error"
	}
	method := req.Method
	switch method {
	case "initialize", "ping", "tools/list", "tools/call":
	default:
		method = "unknown"
	}
	tool := "protocol:" + method
	if req.Method == "tools/call" && rpcErr == nil {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &body)
		if body.Name != "" {
			tool = body.Name
		}
	}
	ms := elapsed.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return mcpauth.Usage{
		UserID: principal.UserID, CredentialID: principal.CredentialID, Tool: tool, Outcome: outcome,
		JobID: linkedJobID(result), Detail: usageDetail(result, rpcErr), DurationMS: ms, InferenceUnits: 0,
	}
}

func usageDetail(result any, rpcErr *rpcError) string {
	if rpcErr != nil {
		return rpcErr.Message
	}
	if toolFailed(result) {
		return "Tool operation failed."
	}
	return ""
}
func structuredContent(result any) map[string]any {
	body, _ := result.(map[string]any)
	data, _ := body["structuredContent"].(map[string]any)
	return data
}
func toolFailed(result any) bool {
	body, ok := result.(map[string]any)
	if !ok {
		return false
	}
	failed, _ := body["isError"].(bool)
	return failed
}

func linkedJobID(result any) string {
	data := structuredContent(result)
	if data == nil {
		return ""
	}
	if job, ok := data["job"].(map[string]any); ok {
		if id, _ := job["id"].(string); strings.HasPrefix(id, "job_") {
			return id
		}
	}
	for _, key := range []string{"job_id", "id"} {
		if id, _ := data[key].(string); strings.HasPrefix(id, "job_") {
			return id
		}
	}
	return ""
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return initializeResult(req.Params), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		principal, _ := ctx.Value(principalKey{}).(mcpauth.Principal)
		tools, err := s.provider.List(ctx, principal)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: "Tool catalog is unavailable."}
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		return s.call(ctx, req.Params)
	default:
		return nil, &rpcError{Code: -32601, Message: "Method not found"}
	}
}

func initializeResult(params json.RawMessage) map[string]any {
	var body struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &body)
	version := "2025-03-26"
	switch body.ProtocolVersion {
	case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
		version = body.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": "agentize", "version": "1"},
	}
}

func (s *Server) call(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var body struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &body); err != nil || body.Name == "" {
		return nil, &rpcError{Code: -32602, Message: "Invalid params"}
	}
	principal, _ := ctx.Value(principalKey{}).(mcpauth.Principal)
	// Re-list for every invocation; a stale client catalog never authorizes execution.
	tools, err := s.provider.List(ctx, principal)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "Tool catalog is unavailable."}
	}
	allowed := false
	var schema json.RawMessage
	for _, tool := range tools {
		if tool.Name == body.Name {
			allowed = true
			schema = tool.InputSchema
			break
		}
	}
	if !allowed {
		return nil, &rpcError{Code: -32602, Message: "Tool is not available with this access."}
	}
	if _, err := toolruntime.ValidateInput(schema, body.Arguments); err != nil {
		return nil, &rpcError{Code: -32602, Message: "Invalid tool arguments."}
	}
	data, err := s.provider.Call(ctx, principal, body.Name, body.Arguments)
	if err != nil {
		message := "Tool operation could not be completed."
		var fault *toolruntime.Fault
		if errors.As(err, &fault) {
			message = fault.Message
		}
		return map[string]any{"content": []map[string]string{{"type": "text", "text": message}}, "isError": true}, nil
	}
	raw, err := json.Marshal(data)
	if err != nil || len(raw) > 4<<20 {
		return nil, &rpcError{Code: -32000, Message: "Tool result exceeds response limits."}
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil || object == nil {
		data = map[string]any{"result": data}
	}
	return map[string]any{"content": []map[string]string{{"type": "text", "text": string(raw)}}, "structuredContent": data, "isError": false}, nil
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPC(w http.ResponseWriter, id *json.RawMessage, result any, rpcErr *rpcError) {
	resp := map[string]any{"jsonrpc": "2.0"}
	if id != nil {
		resp["id"] = json.RawMessage(*id)
	} else {
		resp["id"] = nil
	}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func acceptsJSON(values []string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(strings.Split(part, ";")[0]), "application/json") || strings.TrimSpace(part) == "*/*" {
				return true
			}
		}
	}
	return false
}

func bearer(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.Contains(token, " ") {
		return "", false
	}
	return token, true
}

func writeAuth(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="agentize-mcp"`)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
