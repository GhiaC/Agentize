package filemanager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ghiac/agentize/model"
)

type FileReceipt struct{ ID string }
type FileSender interface {
	SendFile(context.Context, string, *model.UserFile, io.Reader) (FileReceipt, error)
}
type FileSenderFunc func(context.Context, string, *model.UserFile, io.Reader) (FileReceipt, error)

func (f FileSenderFunc) SendFile(ctx context.Context, user string, file *model.UserFile, r io.Reader) (FileReceipt, error) {
	return f(ctx, user, file, r)
}

type DeliveryError struct {
	Code     string
	Definite bool
}

func (e *DeliveryError) Error() string { return e.Code }

type DeliveryResult struct {
	FileID    string `json:"file_id"`
	State     string `json:"state"`
	Receipt   string `json:"receipt,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

type runtimeKey struct{}
type Runtime struct {
	Service      *ChatService
	Scope        ChatScope
	TurnID       string
	Worker       model.AgentType
	Introduction string
	Sender       FileSender
	mu           sync.Mutex
	results      []DeliveryResult
}

func WithRuntime(ctx context.Context, r *Runtime) context.Context {
	return context.WithValue(ctx, runtimeKey{}, r)
}
func RuntimeFrom(ctx context.Context) *Runtime { r, _ := ctx.Value(runtimeKey{}).(*Runtime); return r }
func (r *Runtime) Results() []DeliveryResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]DeliveryResult(nil), r.results...)
}
func (r *Runtime) Send(ctx context.Context, userID, fileID string) (DeliveryResult, error) {
	if r == nil || r.Sender == nil || r.Scope.UserID != userID || r.TurnID == "" {
		return DeliveryResult{}, fmt.Errorf("file delivery is unavailable for this run")
	}
	result, err := r.Service.Send(ctx, r.Scope, OperationKey("send", r.TurnID, fileID), fileID, r.Sender)
	r.mu.Lock()
	r.results = append(r.results, result)
	r.mu.Unlock()
	return result, err
}
func (s *ChatService) Send(ctx context.Context, scope ChatScope, key, fileID string, sender FileSender) (DeliveryResult, error) {
	result := DeliveryResult{FileID: fileID, State: "failed"}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if owner := model.UserIDFrom(ctx); owner != "" && owner != scope.UserID {
		return result, fmt.Errorf("file owner context mismatch")
	}
	if sender == nil {
		return result, fmt.Errorf("file sender is unavailable")
	}
	if err := s.ValidateScope(scope); err != nil {
		return result, err
	}
	file, err := s.Store.GetUserFileForUser(scope.UserID, fileID)
	if err != nil || file == nil || file.MIMEType == FolderMIME {
		return result, fmt.Errorf("file is unavailable")
	}
	op := &model.FileOperation{UserID: scope.UserID, Key: key, Kind: "delivery", State: "sending", File: file}
	claimed, previous, err := s.Store.ClaimFileOperation(ctx, op)
	if err != nil {
		return result, err
	}
	if !claimed {
		if previous.Kind != "delivery" || previous.File == nil || previous.File.FileID != fileID {
			return result, fmt.Errorf("delivery key does not match file")
		}
		result.State = previous.State
		result.Receipt = previous.Receipt
		result.ErrorCode = previous.ErrorCode
		if previous.State == "sending" {
			result.State = "unknown"
		}
		return result, nil
	}
	result.State = "failed"
	stream, err := s.Files.Open(file.StorageKey)
	if err != nil {
		result.ErrorCode = "read_failed"
	} else {
		defer stream.Close()
		if err := ctx.Err(); err != nil {
			result.ErrorCode = "canceled"
		} else {
			receipt, sendErr := sender.SendFile(ctx, scope.UserID, file, stream)
			if sendErr == nil && receipt.ID != "" {
				result.State = "sent"
				result.Receipt = receipt.ID
			} else {
				result.State = "unknown"
				result.ErrorCode = "delivery_unconfirmed"
				if definite, ok := sendErr.(*DeliveryError); ok && definite.Definite {
					result.State = "failed"
					result.ErrorCode = "transport_rejected"
				}
			}
		}
	}
	op.State = result.State
	op.Receipt = result.Receipt
	op.ErrorCode = result.ErrorCode
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if ok, err := s.Store.UpdateFileOperation(finish, op, op.Revision); err != nil || !ok {
		return DeliveryResult{FileID: fileID, State: "unknown", ErrorCode: "receipt_not_persisted"}, fmt.Errorf("delivery receipt could not be persisted")
	}
	return result, nil
}
func DeliveryJSON(result DeliveryResult) string { raw, _ := json.Marshal(result); return string(raw) }
