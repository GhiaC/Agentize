package toolruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func principal() Principal {
	return Principal{UserID: "alice", CredentialID: "test-credential", Scopes: []string{"records:read"}}
}
func invocation(action string) Invocation {
	return Invocation{Name: "records", Profile: "research", Arguments: json.RawMessage(`{"action":"` + action + `"}`)}
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var fault *Fault
	if !errors.As(err, &fault) || fault.Code != code {
		t.Fatalf("got %v, want code %s", err, code)
	}
}
func runtimeFixture(t *testing.T, policy Policy, binding Binding, recorder Recorder) *Runtime {
	t.Helper()
	r, err := New(fixtureCatalog(t, recordsSchema, policy), map[string]Binding{"records": binding}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRuntimeRejectsArgumentsBeforeSideEffects(t *testing.T) {
	var calls atomic.Int64
	r := runtimeFixture(t, testPolicy(), Binding{Handler: func(context.Context, Call) (any, error) { calls.Add(1); return true, nil }}, nil)
	for _, args := range []string{
		`null`, `[]`, `{"action":"read"}{}`, `{"action":"read","action":"write"}`,
		`{"action":"unsupported"}`, `{"action":"read","value":1.5}`, `{"action":"read","value":11}`,
		`{"action":"read","filter":{"__user_id__":"bob"}}`, `{"action":"read","filter":{"allow_internal_llm":true}}`,
		`{"action":"read","filter":{"x":{"__forward_bound__":true}}}`, `{"action":"read","filter":{"owner":"bob"}}`,
		`{"action":"read","filter":{"k":1,"k":2}}`, `{"action":"read","extra":true}`,
		`{"action":"read","filter":` + strings.Repeat(`{"x":`, 65) + `null` + strings.Repeat(`}`, 65) + `}`,
		`{"action":"read","filter":{"x":"` + strings.Repeat("x", MaxArgumentsBytes) + `"}}`,
	} {
		_, err := r.Execute(context.Background(), principal(), Invocation{Name: "records", Profile: "research", Arguments: json.RawMessage(args)})
		requireCode(t, err, "validation_error")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid arguments reached handler")
	}
}

func TestRuntimeIdentityProfileAndContextIsolation(t *testing.T) {
	p := testPolicy()
	p.Context = []string{"account_id"}
	r := runtimeFixture(t, p, Binding{
		AuthorizeContext: func(_ context.Context, p Principal, c ExecutionContext) error {
			if c.AccountID != "account-"+p.UserID {
				return errors.New("not owned")
			}
			return nil
		},
		Handler: func(ctx context.Context, c Call) (any, error) {
			if InferenceAllowed(ctx) {
				return nil, errors.New("unexpected inference permission")
			}
			return map[string]any{"owner": c.Principal.UserID, "account": c.Context.AccountID}, nil
		},
	}, nil)
	_, err := r.Execute(context.Background(), Principal{}, invocation("read"))
	requireCode(t, err, "unauthenticated")
	_, err = r.Execute(context.Background(), principal(), invocation("read"))
	requireCode(t, err, "context_required")
	bad := invocation("read")
	bad.Context.AccountID = "account-bob"
	_, err = r.Execute(context.Background(), principal(), bad)
	requireCode(t, err, "forbidden")
	bad.Profile = "admin"
	_, err = r.Execute(context.Background(), principal(), bad)
	requireCode(t, err, "forbidden")
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := principal()
			p.UserID = fmt.Sprintf("user-%d", i)
			inv := invocation("read")
			inv.Context.AccountID = "account-" + p.UserID
			result, err := r.Execute(context.Background(), p, inv)
			if err != nil {
				t.Error(err)
				return
			}
			var body map[string]string
			if json.Unmarshal(result.Data, &body) != nil || body["owner"] != p.UserID || body["account"] != inv.Context.AccountID {
				t.Errorf("cross-call context: %s", result.Data)
			}
		}(i)
	}
	wg.Wait()
}

type testRecorder struct {
	admission Admission
	beginErr  error
	receipt   *testReceipt
}

func (r *testRecorder) Begin(_ context.Context, a Admission) (Receipt, error) {
	r.admission = a
	return r.receipt, r.beginErr
}

type testReceipt struct {
	codes []string
	err   error
}

func (r *testReceipt) Finish(_ context.Context, code string) error {
	r.codes = append(r.codes, code)
	return r.err
}

func actionPolicy(args map[string]any) (Requirements, error) {
	switch args["action"] {
	case "write":
		return Requirements{Scopes: []string{"records:write"}, Effects: Effects{Write: true}}, nil
	case "prompt":
		return Requirements{Scopes: []string{"records:write"}, Effects: Effects{Write: true, Inference: InferenceDeferred}}, nil
	default:
		return Requirements{}, nil
	}
}

func TestRuntimeActionScopesInferenceAndDurableAdmission(t *testing.T) {
	calls := 0
	closed := &testReceipt{}
	recorder := &testRecorder{receipt: closed}
	binding := Binding{Evaluate: actionPolicy, Handler: func(ctx context.Context, c Call) (any, error) {
		calls++
		return map[string]any{"inference_allowed": InferenceAllowed(ctx), "key": c.IdempotencyKey}, nil
	}}
	r := runtimeFixture(t, testPolicy(), binding, recorder)
	write := invocation("write")
	write.IdempotencyKey = "request-123"
	_, err := r.Execute(context.Background(), principal(), write)
	requireCode(t, err, "forbidden")
	p := principal()
	p.Scopes = append(p.Scopes, "records:write")
	prompt := invocation("prompt")
	prompt.IdempotencyKey = "request-123"
	_, err = r.Execute(context.Background(), p, prompt)
	requireCode(t, err, "internal_inference_denied")
	if calls != 0 || recorder.admission.RequestID != "" {
		t.Fatal("denied action was admitted")
	}
	result, err := r.Execute(context.Background(), p, write)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(closed.codes) != 1 || closed.codes[0] != "ok" || recorder.admission.UserID != "alice" || len(recorder.admission.PayloadHash) != 64 || recorder.admission.InferenceAllowed {
		t.Fatal("write admission did not preserve origin or outcome")
	}
	if recorder.admission.RequestID != result.Meta.RequestID || !strings.Contains(string(result.Data), "request-123") {
		t.Fatal("receipt correlation missing")
	}
	noRecorder := runtimeFixture(t, testPolicy(), binding, nil)
	_, err = noRecorder.Execute(context.Background(), p, write)
	requireCode(t, err, "capability_unavailable")
	if calls != 1 {
		t.Fatal("write ran without durable admission")
	}
	recorder.beginErr = errors.New("private upstream detail")
	_, err = r.Execute(context.Background(), p, write)
	requireCode(t, err, "capability_unavailable")
	if calls != 1 {
		t.Fatal("write ran after failed admission")
	}
}

func TestReadDoesNotInheritInferenceFromBroadCredential(t *testing.T) {
	r := runtimeFixture(t, testPolicy(), Binding{Handler: func(ctx context.Context, c Call) (any, error) {
		if InferenceAllowed(ctx) {
			t.Fatal("read gained inference rights")
		}
		return true, nil
	}}, nil)
	p := principal()
	p.AllowInference = true
	p.Scopes = append(p.Scopes, "inference:execute")
	if _, err := r.Execute(context.Background(), p, invocation("read")); err != nil {
		t.Fatal(err)
	}
	if InferenceAllowed(context.Background()) {
		t.Fatal("missing origin must deny inference")
	}
}

func TestRuntimeNormalizersAndPoliciesCannotRewriteTrustedData(t *testing.T) {
	p := principal()
	scopesBefore := append([]string(nil), p.Scopes...)
	r := runtimeFixture(t, testPolicy(), Binding{
		Evaluate: func(args map[string]any) (Requirements, error) { args["action"] = "write"; return Requirements{}, nil },
		Handler: func(_ context.Context, c Call) (any, error) {
			if c.Arguments["action"] != "read" {
				t.Fatal("policy mutated handler arguments")
			}
			c.Principal.Scopes[0] = "admin"
			return true, nil
		},
	}, nil)
	if _, err := r.Execute(context.Background(), p, invocation("read")); err != nil {
		t.Fatal(err)
	}
	if p.Scopes[0] != scopesBefore[0] {
		t.Fatal("handler mutated caller principal")
	}
	r = runtimeFixture(t, testPolicy(), Binding{
		Normalize: func(_ context.Context, args map[string]any) (map[string]any, error) {
			args["__user_id__"] = "bob"
			return args, nil
		},
		Handler: func(context.Context, Call) (any, error) {
			t.Fatal("forged normalized arguments reached handler")
			return nil, nil
		},
	}, nil)
	_, err := r.Execute(context.Background(), principal(), invocation("read"))
	requireCode(t, err, "validation_error")
}

func TestRuntimeDeadlineAndSafeErrors(t *testing.T) {
	p := testPolicy()
	p.TimeoutMS = 5
	r := runtimeFixture(t, p, Binding{Handler: func(ctx context.Context, _ Call) (any, error) { <-ctx.Done(); return nil, ctx.Err() }}, nil)
	_, err := r.Execute(context.Background(), principal(), invocation("read"))
	requireCode(t, err, "deadline_exceeded")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.Execute(canceled, principal(), invocation("read"))
	requireCode(t, err, "canceled")
	r = runtimeFixture(t, testPolicy(), Binding{Handler: func(context.Context, Call) (any, error) { return nil, errors.New("sensitive-upstream-marker") }}, nil)
	_, err = r.Execute(context.Background(), principal(), invocation("read"))
	requireCode(t, err, "upstream_unavailable")
	if strings.Contains(err.Error(), "sensitive-upstream-marker") {
		t.Fatal("upstream detail escaped")
	}
}

func TestRuntimeResultBoundsAndUnknownCommandOutcome(t *testing.T) {
	p := testPolicy()
	p.MaxResultBytes = 32
	r := runtimeFixture(t, p, Binding{Handler: func(context.Context, Call) (any, error) { return strings.Repeat("x", 100), nil }}, nil)
	result, err := r.Execute(context.Background(), principal(), invocation("read"))
	requireCode(t, err, "result_too_large")
	if result.Data != nil {
		t.Fatal("oversize response escaped")
	}
	receipt := &testReceipt{err: errors.New("private persistence detail")}
	r = runtimeFixture(t, testPolicy(), Binding{Evaluate: actionPolicy, Handler: func(context.Context, Call) (any, error) { return true, nil }}, &testRecorder{receipt: receipt})
	user := principal()
	user.Scopes = append(user.Scopes, "records:write")
	inv := invocation("write")
	inv.IdempotencyKey = "request-123"
	result, err = r.Execute(context.Background(), user, inv)
	requireCode(t, err, "outcome_unknown")
	if result.Data != nil || result.Meta.RequestID == "" {
		t.Fatal("uncertain command needs request correlation without false success")
	}
}

func TestCommandFailureAfterAdmissionIsUncertain(t *testing.T) {
	for _, handlerErr := range []error{context.DeadlineExceeded, errors.New("private upstream failure")} {
		receipt := &testReceipt{}
		calls := 0
		r := runtimeFixture(t, testPolicy(), Binding{Evaluate: actionPolicy, Handler: func(context.Context, Call) (any, error) {
			calls++
			return nil, handlerErr
		}}, &testRecorder{receipt: receipt})
		p := principal()
		p.Scopes = append(p.Scopes, "records:write")
		inv := invocation("write")
		inv.IdempotencyKey = "request-123"
		result, err := r.Execute(context.Background(), p, inv)
		requireCode(t, err, "outcome_unknown")
		if calls != 1 || len(receipt.codes) != 1 || receipt.codes[0] != "outcome_unknown" || result.Data != nil {
			t.Fatal("uncertain command was retried or reported as successful")
		}
	}
}

func TestRuntimeRequiresExplicitInferenceGrant(t *testing.T) {
	p := principal()
	p.Scopes = append(p.Scopes, "records:write", "inference:execute")
	recorder := &testRecorder{receipt: &testReceipt{}}
	calls := 0
	r := runtimeFixture(t, testPolicy(), Binding{Evaluate: actionPolicy, Handler: func(ctx context.Context, call Call) (any, error) {
		calls++
		if !InferenceAllowed(ctx) {
			t.Fatal("explicit inference policy was lost")
		}
		return true, nil
	}}, recorder)
	inv := invocation("prompt")
	inv.IdempotencyKey = "request-123"
	_, err := r.Execute(context.Background(), p, inv)
	requireCode(t, err, "internal_inference_denied")
	p.AllowInference = true
	if _, err := r.Execute(context.Background(), p, inv); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !recorder.admission.InferenceAllowed {
		t.Fatal("inference grant not recorded")
	}
}

func TestRuntimeListAndMissingContextAuthorizer(t *testing.T) {
	b := Binding{Handler: func(context.Context, Call) (any, error) { return true, nil }}
	c := fixtureCatalog(t, recordsSchema, testPolicy())
	if _, err := New(c, nil, nil); err == nil {
		t.Fatal("missing binding accepted")
	}
	if _, err := New(c, map[string]Binding{"missing": b}, nil); err == nil {
		t.Fatal("orphan binding accepted")
	}
	r, err := New(c, map[string]Binding{"records": b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.List(principal(), "research")) != 1 || len(r.List(Principal{}, "research")) != 0 || len(r.List(principal(), "admin")) != 0 {
		t.Fatal("profile discovery bypassed access policy")
	}
	inv := invocation("read")
	inv.Context.AccountID = "account-alice"
	_, err = r.Execute(context.Background(), principal(), inv)
	requireCode(t, err, "forbidden")
	p := testPolicy()
	p.Context = []string{"account_id"}
	if _, err := New(fixtureCatalog(t, recordsSchema, p), map[string]Binding{"records": b}, nil); err == nil {
		t.Fatal("missing authorizer for context-dependent capability accepted")
	}
}
