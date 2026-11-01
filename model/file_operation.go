package model

import (
	"context"
	"time"
)

// FileDetails records provenance independently of the virtual path and byte store.
type FileDetails struct {
	ConversationID    string `json:"conversation_id,omitempty"`
	IngestionKey      string `json:"ingestion_key,omitempty"`
	SHA256            string `json:"sha256,omitempty"`
	LineCount         *int   `json:"line_count,omitempty"`
	InspectionVersion int    `json:"inspection_version,omitempty"`
}

// FileOperation uses revision fencing across process restarts and replicas.
type FileOperation struct {
	UserID    string
	Key       string
	Kind      string
	ParentKey string
	State     string
	Revision  int64
	UpdatedAt time.Time
	File      *UserFile
	Payload   string
	Receipt   string
	ErrorCode string
}

type FileOperationStore interface {
	NextUserFileID(context.Context, string) (string, error)
	CompleteFileUpload(context.Context, *FileOperation, int64, *UserFile) (bool, error)
	ClaimFileOperation(context.Context, *FileOperation) (bool, *FileOperation, error)
	UpdateFileOperation(context.Context, *FileOperation, int64) (bool, error)
	ListFileOperations(context.Context, string) ([]*FileOperation, error)
}
