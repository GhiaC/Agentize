package toolruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/xeipuuv/gojsonschema"
)

const MaxArgumentsBytes = 256 << 10

// Principal must be constructed by a trusted adapter after authentication.
// None of its fields are decoded from a tool's arguments.
type Principal struct {
	UserID         string
	CredentialID   string
	Scopes         []string
	AllowInference bool
}

type ExecutionContext struct {
	AccountID       string `json:"account_id,omitempty"`
	ConversationID  string `json:"conversation_id,omitempty"`
	SystemID        string `json:"system_id,omitempty"`
	SystemVersionID string `json:"system_version_id,omitempty"`
	WorkspaceID     string `json:"workspace_id,omitempty"`
}

type Invocation struct {
	Name           string
	Profile        string
	Arguments      json.RawMessage
	Context        ExecutionContext
	IdempotencyKey string
}

type Call struct {
	Principal      Principal
	Arguments      map[string]any
	Context        ExecutionContext
	IdempotencyKey string
	RequestID      string
}

// Requirements can only add restrictions to the capability's base policy.
// This supports mixed read/write commands without trusting the caller's name.
type Requirements struct {
	Scopes  []string
	Effects Effects
}

type Binding struct {
	Handler          func(context.Context, Call) (any, error)
	Normalize        func(context.Context, map[string]any) (map[string]any, error)
	Evaluate         func(map[string]any) (Requirements, error)
	AuthorizeContext func(context.Context, Principal, ExecutionContext) error
}

// Recorder must durably admit a command before its handler is called. Domain
// idempotency and mutation transactions remain the domain service's job.
type Recorder interface {
	Begin(context.Context, Admission) (Receipt, error)
}
type Receipt interface {
	Finish(context.Context, string) error
}

type Admission struct {
	RequestID        string
	UserID           string
	CredentialID     string
	Tool             string
	ToolVersion      string
	CatalogVersion   string
	PayloadHash      string
	IdempotencyKey   string
	InferenceAllowed bool
}

type Runtime struct {
	catalog  *Catalog
	bindings map[string]Binding
	recorder Recorder
}

// New validates the whole active catalog before accepting any invocation.
// No authentication transport or storage fallback is provided by this package.
func New(catalog *Catalog, bindings map[string]Binding, recorder Recorder) (*Runtime, error) {
	if catalog == nil {
		return nil, fmt.Errorf("tool runtime: catalog is required")
	}
	r := &Runtime{catalog: catalog, bindings: make(map[string]Binding), recorder: recorder}
	for name, binding := range bindings {
		entry, ok := catalog.entries[name]
		if !ok || len(entry.definition.Policy.Profiles) == 0 {
			return nil, fmt.Errorf("tool runtime: binding without active capability: %s", name)
		}
		if entry.definition.Policy.AliasOf != "" {
			return nil, fmt.Errorf("tool runtime: aliases must use their canonical binding: %s", name)
		}
		if binding.Handler == nil {
			return nil, fmt.Errorf("tool runtime: missing handler: %s", name)
		}
		if len(entry.definition.Policy.Context) > 0 && binding.AuthorizeContext == nil {
			return nil, fmt.Errorf("tool runtime: missing context authorizer: %s", name)
		}
		r.bindings[name] = binding
	}
	for _, name := range catalog.names {
		if len(catalog.entries[name].definition.Policy.Profiles) > 0 {
			if target := catalog.entries[name].definition.Policy.AliasOf; target != "" {
				canonical, ok := r.bindings[target]
				if !ok {
					return nil, fmt.Errorf("tool runtime: missing canonical binding for %s", name)
				}
				r.bindings[name] = canonical
			}
			if _, ok := r.bindings[name]; !ok {
				return nil, fmt.Errorf("tool runtime: missing binding: %s", name)
			}
		}
	}
	return r, nil
}

// Fault is safe to expose to a client. Raw handler, validator, database and
// upstream errors are never embedded in it.
type Fault struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f *Fault) Error() string         { return f.Message }
func fault(code, message string) error { return &Fault{Code: code, Message: message} }

type Result struct {
	Data json.RawMessage `json:"data,omitempty"`
	Meta Metadata        `json:"meta"`
}
type Metadata struct {
	RequestID      string `json:"request_id"`
	CatalogVersion string `json:"catalog_version"`
	ToolVersion    string `json:"tool_version"`
}

type executionPolicyKey struct{}
type executionPolicy struct {
	allowed   bool
	requestID string
}

// InferenceAllowed is false for a missing origin as well as an explicit deny.
// Adapters must not turn a missing context into an unrestricted background job.
func InferenceAllowed(ctx context.Context) bool {
	p, ok := ctx.Value(executionPolicyKey{}).(executionPolicy)
	return ok && p.allowed
}

func (r *Runtime) CatalogVersion() string { return r.catalog.Version() }

// List returns only the tools that this principal may call in this fixed
// profile. Search/discovery never changes profile membership or permissions.
func (r *Runtime) List(p Principal, profile string) []Definition {
	out := make([]Definition, 0)
	if !validPrincipal(p) {
		return out
	}
	for _, name := range r.catalog.names {
		d, _ := r.catalog.Definition(name)
		if contains(d.Policy.Profiles, profile) && hasScopes(p, d.Policy.Scopes) && inferencePermitted(p, d.Policy.Effects.Inference) {
			out = append(out, d)
		}
	}
	return out
}

var submissionKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func (r *Runtime) Execute(parent context.Context, p Principal, inv Invocation) (Result, error) {
	result := Result{Meta: Metadata{RequestID: uuid.NewString(), CatalogVersion: r.catalog.Version()}}
	if !validPrincipal(p) {
		return result, fault("unauthenticated", "Authenticated user context is required.")
	}
	p.Scopes = append([]string(nil), p.Scopes...)
	if err := parent.Err(); err != nil {
		return result, safeError(err)
	}
	entry, ok := r.catalog.entries[inv.Name]
	if !ok {
		return result, fault("not_found", "Capability not found.")
	}
	policy := entry.definition.Policy
	result.Meta.ToolVersion = policy.Version
	if !contains(policy.Profiles, inv.Profile) || !hasScopes(p, policy.Scopes) {
		return result, fault("forbidden", "Capability is not available with this profile and access.")
	}
	if !inferencePermitted(p, policy.Effects.Inference) {
		return result, fault("internal_inference_denied", "Internal inference is not permitted.")
	}
	binding := r.bindings[inv.Name]
	ctx, cancel := context.WithTimeout(parent, time.Duration(policy.TimeoutMS)*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, executionPolicyKey{}, executionPolicy{requestID: result.Meta.RequestID})
	data := inv.Arguments
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	if len(data) > MaxArgumentsBytes {
		return result, fault("validation_error", "Tool arguments exceed the size limit.")
	}
	var args map[string]any
	if err := decodeStrict(data, &args); err != nil || args == nil {
		return result, fault("validation_error", "Tool arguments must be an unambiguous JSON object.")
	}
	if reservedArgument(args) {
		return result, fault("validation_error", "Internal identity and policy arguments are not accepted.")
	}
	if err := validateArguments(entry.schema, args); err != nil {
		return result, err
	}
	if binding.Normalize != nil {
		normalized, err := binding.Normalize(ctx, args)
		if err != nil || normalized == nil {
			return result, fault("validation_error", "Tool arguments could not be normalized.")
		}
		// Normalize through JSON again to prevent shared caller-owned maps and
		// non-JSON values from crossing the handler boundary.
		normalizedJSON, err := json.Marshal(normalized)
		if err != nil || len(normalizedJSON) > MaxArgumentsBytes {
			return result, fault("validation_error", "Normalized arguments are invalid.")
		}
		if err := decodeStrict(normalizedJSON, &args); err != nil || reservedArgument(args) {
			return result, fault("validation_error", "Normalized arguments are invalid.")
		}
		if err := validateArguments(entry.schema, args); err != nil {
			return result, err
		}
	}
	if missingContext(inv.Context, policy.Context) {
		return result, fault("context_required", "Explicit execution context is required.")
	}
	if inv.Context.SystemVersionID != "" && inv.Context.SystemID == "" {
		return result, fault("validation_error", "A system version requires an explicit system.")
	}
	if inv.Context != (ExecutionContext{}) || len(policy.Context) > 0 {
		if binding.AuthorizeContext == nil {
			return result, fault("forbidden", "Execution context is not supported by this capability.")
		}
		if err := binding.AuthorizeContext(ctx, p, inv.Context); err != nil {
			return result, fault("forbidden", "Execution context is not accessible.")
		}
	}
	effects := policy.Effects
	if binding.Evaluate != nil {
		policyJSON, _ := json.Marshal(args)
		var policyArgs map[string]any
		if err := decodeStrict(policyJSON, &policyArgs); err != nil {
			return result, fault("validation_error", "Tool arguments are invalid.")
		}
		required, err := binding.Evaluate(policyArgs)
		if err != nil {
			return result, fault("validation_error", "Tool action is invalid.")
		}
		if !hasScopes(p, required.Scopes) {
			return result, fault("forbidden", "Tool action requires additional access.")
		}
		if required.Effects.Inference != "" && !inferencePermitted(p, required.Effects.Inference) {
			return result, fault("internal_inference_denied", "Tool action can create internal inference.")
		}
		effects.Write = effects.Write || required.Effects.Write
		effects.ExternalAction = effects.ExternalAction || required.Effects.ExternalAction
		if required.Effects.Inference != "" && required.Effects.Inference != InferenceNone {
			effects.Inference = required.Effects.Inference
		}
	}
	ctx = context.WithValue(ctx, executionPolicyKey{}, executionPolicy{allowed: effects.Inference != InferenceNone && inferencePermitted(p, effects.Inference), requestID: result.Meta.RequestID})
	if key, exists := args["idempotency_key"]; exists {
		value, ok := key.(string)
		if !ok || (inv.IdempotencyKey != "" && value != inv.IdempotencyKey) {
			return result, fault("conflict", "Idempotency keys do not match.")
		}
		inv.IdempotencyKey = value
	}
	command := effects.Write || effects.ExternalAction || effects.Inference != InferenceNone
	if command && !submissionKeyPattern.MatchString(inv.IdempotencyKey) {
		return result, fault("validation_error", "A stable idempotency key of 8 to 128 letters, digits, underscores or hyphens is required.")
	}
	var receipt Receipt
	if command {
		if r.recorder == nil {
			return result, fault("capability_unavailable", "Durable command admission is not configured.")
		}
		payload, err := json.Marshal(struct {
			Arguments map[string]any
			Context   ExecutionContext
			Version   string
		}{args, inv.Context, policy.Version})
		if err != nil {
			return result, fault("validation_error", "Tool arguments are invalid.")
		}
		sum := sha256.Sum256(payload)
		receipt, err = r.recorder.Begin(ctx, Admission{RequestID: result.Meta.RequestID, UserID: p.UserID, CredentialID: p.CredentialID, Tool: inv.Name, ToolVersion: policy.Version, CatalogVersion: r.catalog.version, PayloadHash: hex.EncodeToString(sum[:]), IdempotencyKey: inv.IdempotencyKey, InferenceAllowed: InferenceAllowed(ctx)})
		if err != nil || receipt == nil {
			return result, fault("capability_unavailable", "Durable command admission failed.")
		}
	}
	if err := ctx.Err(); err != nil {
		return result, finish(parent, receipt, safeError(err))
	}
	value, err := binding.Handler(ctx, Call{Principal: p, Arguments: args, Context: inv.Context, IdempotencyKey: inv.IdempotencyKey, RequestID: result.Meta.RequestID})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		failure := safeError(err)
		var domainFault *Fault
		if receipt != nil && !errors.As(err, &domainFault) {
			failure = fault("outcome_unknown", "Command outcome must be reconciled using its receipt.")
		}
		return result, finish(parent, receipt, failure)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return result, finish(parent, receipt, fault("invalid_result", "Tool result could not be encoded."))
	}
	if len(encoded) > policy.MaxResultBytes {
		return result, finish(parent, receipt, fault("result_too_large", "Tool result exceeds the response limit; request a smaller page or an export."))
	}
	if err := finish(parent, receipt, nil); err != nil {
		return result, err
	}
	result.Data = encoded
	return result, nil
}

func validateArguments(schema *gojsonschema.Schema, args map[string]any) error {
	result, err := schema.Validate(gojsonschema.NewGoLoader(args))
	if err != nil || !result.Valid() {
		return fault("validation_error", "Tool arguments do not match the capability schema.")
	}
	return nil
}

func finish(parent context.Context, receipt Receipt, err error) error {
	if receipt == nil {
		return err
	}
	code := "ok"
	var f *Fault
	if errors.As(err, &f) {
		code = f.Code
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	defer cancel()
	if finishErr := receipt.Finish(ctx, code); finishErr != nil {
		return fault("outcome_unknown", "Command outcome must be reconciled using its receipt.")
	}
	return err
}

func safeError(err error) error {
	if errors.Is(err, context.Canceled) {
		return fault("canceled", "Tool request was canceled.")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fault("deadline_exceeded", "Tool request exceeded its deadline.")
	}
	// Only already-safe typed faults cross the public boundary. Handler adapters
	// are responsible for translating known domain errors into these codes.
	var f *Fault
	if errors.As(err, &f) {
		return &Fault{Code: f.Code, Message: f.Message}
	}
	return fault("upstream_unavailable", "Tool operation could not be completed.")
}

func validPrincipal(p Principal) bool {
	return strings.TrimSpace(p.UserID) != "" && strings.TrimSpace(p.CredentialID) != ""
}
func contains(items []string, want string) bool {
	if want == "" {
		return false
	}
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
func hasScopes(p Principal, required []string) bool {
	for _, scope := range required {
		if !contains(p.Scopes, scope) {
			return false
		}
	}
	return true
}
func inferencePermitted(p Principal, inference Inference) bool {
	switch inference {
	case InferenceNone:
		return true
	case InferenceDirect, InferenceDeferred:
		return p.AllowInference && hasScopes(p, []string{"inference:execute"})
	default:
		return false
	}
}
func missingContext(c ExecutionContext, required []string) bool {
	fields := map[string]string{"account_id": c.AccountID, "conversation_id": c.ConversationID, "system_id": c.SystemID, "system_version_id": c.SystemVersionID, "workspace_id": c.WorkspaceID}
	for _, name := range required {
		if strings.TrimSpace(fields[name]) == "" {
			return true
		}
	}
	return false
}
func reservedArgument(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for name, child := range v {
			switch name {
			case "owner", "user_id", "credential_id", "principal", "allow_internal_llm", "allow_inference", "scopes", "origin":
				return true
			}
			if strings.HasPrefix(name, "__") || reservedArgument(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if reservedArgument(child) {
				return true
			}
		}
	}
	return false
}
