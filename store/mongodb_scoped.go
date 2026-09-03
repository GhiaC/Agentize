package store

import (
	"fmt"

	"github.com/ghiac/agentize/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (s *MongoDBStore) GetUserSession(userID, sessionID string) (*model.Session, error) {
	session, err := s.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if session.UserID != userID {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return session, nil
}

func (s *MongoDBStore) GetUserConversation(userID, conversationID string) (*model.Conversation, error) {
	conv, err := s.GetConversation(conversationID)
	if err != nil {
		return nil, err
	}
	if conv.UserID != userID {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	return conv, nil
}

func (s *MongoDBStore) GetUserConversationBySession(userID, sessionID string) (*model.Conversation, error) {
	conv, err := s.GetConversationBySession(sessionID)
	if err != nil || conv == nil {
		return conv, err
	}
	if conv.UserID != userID {
		return nil, nil
	}
	return conv, nil
}

func (s *MongoDBStore) GetUserMessagesBySessionPage(userID, sessionID string, limit, offset int) ([]*model.Message, error) {
	if userID == "" {
		return s.GetMessagesBySessionPage(sessionID, limit, offset)
	}
	if limit <= 0 {
		limit = messagesPageSize
	}
	if offset < 0 {
		offset = 0
	}
	ctx, cancel := s.opCtx()
	defer cancel()

	cursor, err := s.messagesCollection.Find(ctx, bson.M{"user_id": userID, "session_id": sessionID},
		options.Find().
			SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "seq_id", Value: -1}}).
			SetSkip(int64(offset)).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer cursor.Close(ctx)
	return decodeMessageCursor(ctx, cursor)
}

func (s *MongoDBStore) GetUserFileForUser(userID, fileID string) (*model.UserFile, error) {
	f, err := s.GetUserFile(fileID)
	if err != nil || f == nil {
		return f, err
	}
	if f.UserID != userID {
		return nil, nil
	}
	return f, nil
}

func (s *MongoDBStore) GetUserWorkflowRun(userID, workflowID string) (*model.WorkflowRun, error) {
	workflow, err := s.GetWorkflowRun(workflowID)
	if err != nil || workflow == nil {
		return workflow, err
	}
	if workflow.UserID != userID {
		return nil, nil
	}
	return workflow, nil
}

func (s *MongoDBStore) GetUserTaskSchedule(userID, scheduleID string) (*model.TaskSchedule, error) {
	schedule, err := s.GetTaskSchedule(scheduleID)
	if err != nil || schedule == nil {
		return schedule, err
	}
	if schedule.UserID != userID {
		return nil, nil
	}
	return schedule, nil
}
