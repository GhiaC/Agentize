package store

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
)

// meteredStore wraps a Store and records the latency of each database-backed
// operation as agentize_store_query_duration_seconds{operation,backend}.
//
// It embeds the Store interface, so every method is a straight pass-through; the
// methods below add only a timing observation. The in-memory visited-node
// helpers and the trivial accessors (Close, BackendInfo) are intentionally NOT
// timed — they do no I/O — and fall through to the embedded Store unchanged.
type meteredStore struct {
	Store
	backend string
	mu      sync.Mutex
	nextID  uint64
	active  map[uint64]storeOperation
}

type storeOperation struct {
	id      uint64
	op      string
	started time.Time
	warn    *time.Timer
}

const slowStoreOperationWarning = 5 * time.Second

// NewMetered wraps s so its database operations are timed. The backend label is
// derived from s.BackendInfo().Type (e.g. "sqlite", "mongodb"). Wrapping an
// already-metered store returns it unchanged, so instrumentation is never
// doubled. A nil store is returned as-is.
func NewMetered(s Store) Store {
	if s == nil {
		return nil
	}
	if _, ok := s.(*meteredStore); ok {
		return s
	}
	backend := strings.ToLower(strings.TrimSpace(s.BackendInfo().Type))
	if backend == "" {
		backend = "unknown"
	}
	return &meteredStore{Store: s, backend: backend, active: make(map[uint64]storeOperation)}
}

// observe records one operation's latency. The intended use is
//
//	defer m.observe(m.begin("Get"))
//
// begin runs immediately, before the wrapped call, and observe runs when it
// returns.
func (m *meteredStore) begin(op string) storeOperation {
	started := time.Now()
	m.mu.Lock()
	m.nextID++
	operation := storeOperation{id: m.nextID, op: op, started: started}
	operation.warn = time.AfterFunc(slowStoreOperationWarning, func() {
		log.Log.Warnf("store operation still in flight | operation=%s backend=%s duration=%s", op, m.backend, time.Since(started).Round(time.Second))
	})
	m.active[operation.id] = operation
	m.mu.Unlock()
	metrics.StoreQueryStart(op, m.backend)
	return operation
}

func (m *meteredStore) observe(operation storeOperation) {
	if operation.warn != nil {
		operation.warn.Stop()
	}
	m.mu.Lock()
	delete(m.active, operation.id)
	m.mu.Unlock()
	metrics.StoreQueryDone(operation.op, m.backend)
	metrics.StoreQuery(operation.op, m.backend, time.Since(operation.started))
}

// OperationalHealth is a lock-independent snapshot for a host debug API. It
// never calls the wrapped store, so it remains available during a store lockup.
func (m *meteredStore) OperationalHealth() map[string]any {
	now := time.Now()
	type aggregate struct {
		count  int
		oldest time.Time
	}
	m.mu.Lock()
	grouped := make(map[string]aggregate)
	for _, operation := range m.active {
		item := grouped[operation.op]
		item.count++
		if item.oldest.IsZero() || operation.started.Before(item.oldest) {
			item.oldest = operation.started
		}
		grouped[operation.op] = item
	}
	total := len(m.active)
	m.mu.Unlock()
	operations := make([]map[string]any, 0, len(grouped))
	for op, item := range grouped {
		operations = append(operations, map[string]any{
			"operation":      op,
			"in_flight":      item.count,
			"oldest_seconds": now.Sub(item.oldest).Seconds(),
		})
	}
	sort.Slice(operations, func(i, j int) bool {
		return operations[i]["operation"].(string) < operations[j]["operation"].(string)
	})
	return map[string]any{"backend": m.backend, "in_flight": total, "operations": operations}
}

// --- Sessions (model.SessionStore) ---

func (m *meteredStore) Get(sessionID string) (*model.Session, error) {
	defer m.observe(m.begin("Get"))
	return m.Store.Get(sessionID)
}

func (m *meteredStore) GetUserSession(userID, sessionID string) (*model.Session, error) {
	defer m.observe(m.begin("GetUserSession"))
	return m.Store.GetUserSession(userID, sessionID)
}

func (m *meteredStore) Put(session *model.Session) error {
	defer m.observe(m.begin("Put"))
	return m.Store.Put(session)
}

func (m *meteredStore) Delete(sessionID string) error {
	defer m.observe(m.begin("Delete"))
	return m.Store.Delete(sessionID)
}

func (m *meteredStore) DeleteUserSession(userID, sessionID string) error {
	defer m.observe(m.begin("DeleteUserSession"))
	return m.Store.DeleteUserSession(userID, sessionID)
}

func (m *meteredStore) List(userID string) ([]*model.Session, error) {
	defer m.observe(m.begin("List"))
	return m.Store.List(userID)
}

func (m *meteredStore) GetNextSessionSeq(userID string, agentType model.AgentType) (int, error) {
	defer m.observe(m.begin("GetNextSessionSeq"))
	return m.Store.GetNextSessionSeq(userID, agentType)
}

// --- Core (per-user singleton) sessions ---

func (m *meteredStore) GetCoreSession(userID string) (*model.Session, error) {
	defer m.observe(m.begin("GetCoreSession"))
	return m.Store.GetCoreSession(userID)
}

func (m *meteredStore) PutCoreSession(session *model.Session) error {
	defer m.observe(m.begin("PutCoreSession"))
	return m.Store.PutCoreSession(session)
}

// --- Users / messages / files / tool calls / traces (writers) ---

func (m *meteredStore) PutUser(user *model.User) error {
	defer m.observe(m.begin("PutUser"))
	return m.Store.PutUser(user)
}

func (m *meteredStore) GetOrCreateUser(userID string) (*model.User, error) {
	defer m.observe(m.begin("GetOrCreateUser"))
	return m.Store.GetOrCreateUser(userID)
}

func (m *meteredStore) PutMessage(message *model.Message) error {
	defer m.observe(m.begin("PutMessage"))
	return m.Store.PutMessage(message)
}

func (m *meteredStore) PutMessages(messages []*model.Message) error {
	defer m.observe(m.begin("PutMessages"))
	return m.Store.PutMessages(messages)
}

func (m *meteredStore) GetMessagesBySessionPage(sessionID string, limit, offset int) ([]*model.Message, error) {
	defer m.observe(m.begin("GetMessagesBySessionPage"))
	return m.Store.GetMessagesBySessionPage(sessionID, limit, offset)
}

func (m *meteredStore) GetUserMessagesBySessionPage(userID, sessionID string, limit, offset int) ([]*model.Message, error) {
	defer m.observe(m.begin("GetUserMessagesBySessionPage"))
	return m.Store.GetUserMessagesBySessionPage(userID, sessionID, limit, offset)
}

func (m *meteredStore) GetUserMessagesBySession(userID, sessionID string) ([]*model.Message, error) {
	defer m.observe(m.begin("GetUserMessagesBySession"))
	return m.Store.GetUserMessagesBySession(userID, sessionID)
}

func (m *meteredStore) AddOpenedFile(openedFile *model.OpenedFile) error {
	defer m.observe(m.begin("AddOpenedFile"))
	return m.Store.AddOpenedFile(openedFile)
}

func (m *meteredStore) CloseOpenedFile(sessionID string, filePath string) error {
	defer m.observe(m.begin("CloseOpenedFile"))
	return m.Store.CloseOpenedFile(sessionID, filePath)
}

func (m *meteredStore) GetCurrentlyOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	defer m.observe(m.begin("GetCurrentlyOpenedFilesBySession"))
	return m.Store.GetCurrentlyOpenedFilesBySession(sessionID)
}

func (m *meteredStore) PutUserFile(f *model.UserFile) error {
	defer m.observe(m.begin("PutUserFile"))
	return m.Store.PutUserFile(f)
}

func (m *meteredStore) GetUserFile(fileID string) (*model.UserFile, error) {
	defer m.observe(m.begin("GetUserFile"))
	return m.Store.GetUserFile(fileID)
}

func (m *meteredStore) GetUserFileForUser(userID, fileID string) (*model.UserFile, error) {
	defer m.observe(m.begin("GetUserFileForUser"))
	return m.Store.GetUserFileForUser(userID, fileID)
}

func (m *meteredStore) DeleteUserFile(fileID string) error {
	defer m.observe(m.begin("DeleteUserFile"))
	return m.Store.DeleteUserFile(fileID)
}

func (m *meteredStore) DeleteUserFileForUser(userID, fileID string) error {
	defer m.observe(m.begin("DeleteUserFileForUser"))
	return m.Store.DeleteUserFileForUser(userID, fileID)
}

func (m *meteredStore) PutToolCall(toolCall *model.ToolCall) error {
	defer m.observe(m.begin("PutToolCall"))
	return m.Store.PutToolCall(toolCall)
}

func (m *meteredStore) UpdateToolCallResponse(toolID string, response string, execErr error) error {
	defer m.observe(m.begin("UpdateToolCallResponse"))
	return m.Store.UpdateToolCallResponse(toolID, response, execErr)
}

func (m *meteredStore) UpdateUserToolCallResponse(userID, sessionID, toolID string, response string, execErr error) error {
	defer m.observe(m.begin("UpdateUserToolCallResponse"))
	return m.Store.UpdateUserToolCallResponse(userID, sessionID, toolID, response, execErr)
}

func (m *meteredStore) UpdateMessageToolCallResponse(userID, sessionID, messageID, toolID string, response string, execErr error) error {
	defer m.observe(m.begin("UpdateMessageToolCallResponse"))
	return m.Store.UpdateMessageToolCallResponse(userID, sessionID, messageID, toolID, response, execErr)
}

func (m *meteredStore) PutRouteTrace(trace *model.RouteTrace) error {
	defer m.observe(m.begin("PutRouteTrace"))
	return m.Store.PutRouteTrace(trace)
}

func (m *meteredStore) PutReviewRequest(r *model.ReviewRequest) error {
	defer m.observe(m.begin("PutReviewRequest"))
	return m.Store.PutReviewRequest(r)
}

func (m *meteredStore) GetReviewRequest(id string) (*model.ReviewRequest, error) {
	defer m.observe(m.begin("GetReviewRequest"))
	return m.Store.GetReviewRequest(id)
}

func (m *meteredStore) ListPendingReviews(userID string) ([]*model.ReviewRequest, error) {
	defer m.observe(m.begin("ListPendingReviews"))
	return m.Store.ListPendingReviews(userID)
}

func (m *meteredStore) PutTaskSchedule(schedule *model.TaskSchedule) error {
	defer m.observe(m.begin("PutTaskSchedule"))
	return m.Store.PutTaskSchedule(schedule)
}

func (m *meteredStore) GetTaskSchedule(scheduleID string) (*model.TaskSchedule, error) {
	defer m.observe(m.begin("GetTaskSchedule"))
	return m.Store.GetTaskSchedule(scheduleID)
}

func (m *meteredStore) GetUserTaskSchedule(userID, scheduleID string) (*model.TaskSchedule, error) {
	defer m.observe(m.begin("GetUserTaskSchedule"))
	return m.Store.GetUserTaskSchedule(userID, scheduleID)
}

func (m *meteredStore) ListTaskSchedules(userID string) ([]*model.TaskSchedule, error) {
	defer m.observe(m.begin("ListTaskSchedules"))
	return m.Store.ListTaskSchedules(userID)
}

func (m *meteredStore) DeleteTaskSchedule(scheduleID string) error {
	defer m.observe(m.begin("DeleteTaskSchedule"))
	return m.Store.DeleteTaskSchedule(scheduleID)
}

func (m *meteredStore) PutTaskScheduleRun(run *model.TaskScheduleRun) error {
	defer m.observe(m.begin("PutTaskScheduleRun"))
	return m.Store.PutTaskScheduleRun(run)
}

func (m *meteredStore) ListTaskScheduleRuns(scheduleID string, limit int) ([]*model.TaskScheduleRun, error) {
	defer m.observe(m.begin("ListTaskScheduleRuns"))
	return m.Store.ListTaskScheduleRuns(scheduleID, limit)
}

func (m *meteredStore) PutWorkflowRun(workflow *model.WorkflowRun) error {
	defer m.observe(m.begin("PutWorkflowRun"))
	return m.Store.PutWorkflowRun(workflow)
}

func (m *meteredStore) GetConversation(conversationID string) (*model.Conversation, error) {
	defer m.observe(m.begin("GetConversation"))
	return m.Store.GetConversation(conversationID)
}

func (m *meteredStore) GetUserConversation(userID, conversationID string) (*model.Conversation, error) {
	defer m.observe(m.begin("GetUserConversation"))
	return m.Store.GetUserConversation(userID, conversationID)
}

func (m *meteredStore) PutConversation(conversation *model.Conversation) error {
	defer m.observe(m.begin("PutConversation"))
	return m.Store.PutConversation(conversation)
}

func (m *meteredStore) DeleteConversation(conversationID string) error {
	defer m.observe(m.begin("DeleteConversation"))
	return m.Store.DeleteConversation(conversationID)
}

func (m *meteredStore) DeleteUserConversation(userID, conversationID string) error {
	defer m.observe(m.begin("DeleteUserConversation"))
	return m.Store.DeleteUserConversation(userID, conversationID)
}

func (m *meteredStore) ListConversations(userID string) ([]*model.Conversation, error) {
	defer m.observe(m.begin("ListConversations"))
	return m.Store.ListConversations(userID)
}

func (m *meteredStore) ListAllConversations() ([]*model.Conversation, error) {
	defer m.observe(m.begin("ListAllConversations"))
	return m.Store.ListAllConversations()
}

func (m *meteredStore) GetConversationBySession(sessionID string) (*model.Conversation, error) {
	defer m.observe(m.begin("GetConversationBySession"))
	return m.Store.GetConversationBySession(sessionID)
}

func (m *meteredStore) GetUserConversationBySession(userID, sessionID string) (*model.Conversation, error) {
	defer m.observe(m.begin("GetUserConversationBySession"))
	return m.Store.GetUserConversationBySession(userID, sessionID)
}

func (m *meteredStore) GetNextConversationSeq(userID string) (int, error) {
	defer m.observe(m.begin("GetNextConversationSeq"))
	return m.Store.GetNextConversationSeq(userID)
}

func (m *meteredStore) TouchConversationBySession(sessionID string) error {
	defer m.observe(m.begin("TouchConversationBySession"))
	return m.Store.TouchConversationBySession(sessionID)
}

func (m *meteredStore) TouchUserConversationBySession(userID, sessionID string) error {
	defer m.observe(m.begin("TouchUserConversationBySession"))
	return m.Store.TouchUserConversationBySession(userID, sessionID)
}

// --- Debug reads + aggregates (debuger.DebugStore) ---

func (m *meteredStore) GetAllSessions() (map[string][]*model.Session, error) {
	defer m.observe(m.begin("GetAllSessions"))
	return m.Store.GetAllSessions()
}

func (m *meteredStore) GetAllUsers() ([]*model.User, error) {
	defer m.observe(m.begin("GetAllUsers"))
	return m.Store.GetAllUsers()
}

func (m *meteredStore) GetAllMessages() ([]*model.Message, error) {
	defer m.observe(m.begin("GetAllMessages"))
	return m.Store.GetAllMessages()
}

func (m *meteredStore) GetAllOpenedFiles() ([]*model.OpenedFile, error) {
	defer m.observe(m.begin("GetAllOpenedFiles"))
	return m.Store.GetAllOpenedFiles()
}

func (m *meteredStore) GetOpenedFilesByUser(userID string) ([]*model.OpenedFile, error) {
	defer m.observe(m.begin("GetOpenedFilesByUser"))
	return m.Store.GetOpenedFilesByUser(userID)
}

func (m *meteredStore) GetAllUserFiles() ([]*model.UserFile, error) {
	defer m.observe(m.begin("GetAllUserFiles"))
	return m.Store.GetAllUserFiles()
}

func (m *meteredStore) GetUserFilesByUser(userID string) ([]*model.UserFile, error) {
	defer m.observe(m.begin("GetUserFilesByUser"))
	return m.Store.GetUserFilesByUser(userID)
}

func (m *meteredStore) GetUserFilesBySession(sessionID string) ([]*model.UserFile, error) {
	defer m.observe(m.begin("GetUserFilesBySession"))
	return m.Store.GetUserFilesBySession(sessionID)
}

func (m *meteredStore) GetMessagesBySession(sessionID string) ([]*model.Message, error) {
	defer m.observe(m.begin("GetMessagesBySession"))
	return m.Store.GetMessagesBySession(sessionID)
}

func (m *meteredStore) GetMessagesByUser(userID string) ([]*model.Message, error) {
	defer m.observe(m.begin("GetMessagesByUser"))
	return m.Store.GetMessagesByUser(userID)
}

func (m *meteredStore) GetOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	defer m.observe(m.begin("GetOpenedFilesBySession"))
	return m.Store.GetOpenedFilesBySession(sessionID)
}

func (m *meteredStore) GetUser(userID string) (*model.User, error) {
	defer m.observe(m.begin("GetUser"))
	return m.Store.GetUser(userID)
}

func (m *meteredStore) GetSession(sessionID string) (*model.Session, error) {
	defer m.observe(m.begin("GetSession"))
	return m.Store.GetSession(sessionID)
}

func (m *meteredStore) GetAllToolCalls() ([]*model.ToolCall, error) {
	defer m.observe(m.begin("GetAllToolCalls"))
	return m.Store.GetAllToolCalls()
}

func (m *meteredStore) GetToolCallsBySession(sessionID string) ([]*model.ToolCall, error) {
	defer m.observe(m.begin("GetToolCallsBySession"))
	return m.Store.GetToolCallsBySession(sessionID)
}

func (m *meteredStore) GetUserToolCallsBySession(userID, sessionID string) ([]*model.ToolCall, error) {
	defer m.observe(m.begin("GetUserToolCallsBySession"))
	return m.Store.GetUserToolCallsBySession(userID, sessionID)
}

func (m *meteredStore) GetToolCallByID(toolCallID string) (*model.ToolCall, error) {
	defer m.observe(m.begin("GetToolCallByID"))
	return m.Store.GetToolCallByID(toolCallID)
}

func (m *meteredStore) GetToolCallByToolID(toolID string) (*model.ToolCall, error) {
	defer m.observe(m.begin("GetToolCallByToolID"))
	return m.Store.GetToolCallByToolID(toolID)
}

func (m *meteredStore) PutSummarizationLog(log *model.SummarizationLog) error {
	defer m.observe(m.begin("PutSummarizationLog"))
	return m.Store.PutSummarizationLog(log)
}

func (m *meteredStore) GetSummarizationLogsBySession(sessionID string) ([]*model.SummarizationLog, error) {
	defer m.observe(m.begin("GetSummarizationLogsBySession"))
	return m.Store.GetSummarizationLogsBySession(sessionID)
}

func (m *meteredStore) GetAllSummarizationLogs() ([]*model.SummarizationLog, error) {
	defer m.observe(m.begin("GetAllSummarizationLogs"))
	return m.Store.GetAllSummarizationLogs()
}

func (m *meteredStore) GetRouteTraceByID(traceID string) (*model.RouteTrace, error) {
	defer m.observe(m.begin("GetRouteTraceByID"))
	return m.Store.GetRouteTraceByID(traceID)
}

func (m *meteredStore) GetRouteTracesBySession(sessionID string) ([]*model.RouteTrace, error) {
	defer m.observe(m.begin("GetRouteTracesBySession"))
	return m.Store.GetRouteTracesBySession(sessionID)
}

func (m *meteredStore) GetUserRouteTracesBySession(userID, sessionID string) ([]*model.RouteTrace, error) {
	defer m.observe(m.begin("GetUserRouteTracesBySession"))
	return m.Store.GetUserRouteTracesBySession(userID, sessionID)
}

func (m *meteredStore) GetRouteTracesByUser(userID string) ([]*model.RouteTrace, error) {
	defer m.observe(m.begin("GetRouteTracesByUser"))
	return m.Store.GetRouteTracesByUser(userID)
}

func (m *meteredStore) GetAllRouteTraces() ([]*model.RouteTrace, error) {
	defer m.observe(m.begin("GetAllRouteTraces"))
	return m.Store.GetAllRouteTraces()
}

func (m *meteredStore) GetWorkflowRun(workflowID string) (*model.WorkflowRun, error) {
	defer m.observe(m.begin("GetWorkflowRun"))
	return m.Store.GetWorkflowRun(workflowID)
}

func (m *meteredStore) GetUserWorkflowRun(userID, workflowID string) (*model.WorkflowRun, error) {
	defer m.observe(m.begin("GetUserWorkflowRun"))
	return m.Store.GetUserWorkflowRun(userID, workflowID)
}

func (m *meteredStore) ListWorkflowRuns(userID string, limit int) ([]*model.WorkflowRun, error) {
	defer m.observe(m.begin("ListWorkflowRuns"))
	return m.Store.ListWorkflowRuns(userID, limit)
}

func (m *meteredStore) DeleteUserData(userID string) error {
	defer m.observe(m.begin("DeleteUserData"))
	return m.Store.DeleteUserData(userID)
}

// --- Maintainer forwarding + escape hatch ---
//
// The wrapper is returned to callers as a plain Store. So that hosts which
// type-assert it to store.Maintainer (for backups / integrity checks) keep
// working, forward those methods to the wrapped store. They are NOT timed:
// Backup can stream an entire database and would pollute the query-latency
// histogram. Every built-in backend implements Maintainer, so the fallback is
// effectively dead code, kept only for custom stores.

func (m *meteredStore) Backup(w io.Writer) error {
	if mt, ok := m.Store.(Maintainer); ok {
		return mt.Backup(w)
	}
	return fmt.Errorf("store: backup not supported by %T", m.Store)
}

func (m *meteredStore) Verify() ([]Issue, error) {
	if mt, ok := m.Store.(Maintainer); ok {
		return mt.Verify()
	}
	return nil, fmt.Errorf("store: verify not supported by %T", m.Store)
}

// Unwrap returns the wrapped store, for callers that need the concrete backend
// type (e.g. for a further type assertion such as PoolStats).
func (m *meteredStore) Unwrap() Store { return m.Store }

// Compile-time guarantees that the metered wrapper satisfies the full Store
// contract (embedded interface + timed overrides) and the Maintainer contract.
var (
	_ Store      = (*meteredStore)(nil)
	_ Maintainer = (*meteredStore)(nil)
)
