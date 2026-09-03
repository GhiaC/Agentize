package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/ghiac/agentize/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func scopedMongoID(userID, id string) string {
	userID = strings.TrimSpace(userID)
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if model.IsLegacyConcatID(id) || !model.IsNumericID(id) {
		return id
	}
	if userID == "" {
		return id
	}
	return userID + "/" + id
}

func scopedChildMongoID(userID, parentID, id string) string {
	id = strings.TrimSpace(id)
	if model.IsLegacyConcatID(id) || !model.IsNumericID(id) {
		return id
	}
	return scopedMongoID(userID, parentID) + "/" + id
}

func (s *MongoDBStore) errIfAmbiguous(ctx context.Context, coll *mongo.Collection, field, kind, id string) error {
	n, err := coll.CountDocuments(ctx, bson.M{field: id})
	if err != nil {
		return fmt.Errorf("failed to check %s uniqueness: %w", kind, err)
	}
	if n > 1 {
		return errAmbiguousID(kind, id)
	}
	return nil
}

func (s *MongoDBStore) GetUserSession(userID, sessionID string) (*model.Session, error) {
	ctx, cancel := s.opCtx()
	defer cancel()

	var doc sessionDocument
	err := s.collection.FindOne(ctx, bson.M{"user_id": userID, "_id": scopedMongoID(userID, sessionID)}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		err = s.collection.FindOne(ctx, bson.M{"user_id": userID, "_id": sessionID}).Decode(&doc)
	}
	if err == mongo.ErrNoDocuments {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query session: %w", err)
	}

	session := &model.Session{}
	if err := unmarshalJSONOrBSON(doc.Data, session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}
	session.CreatedAt = doc.CreatedAt
	session.UpdatedAt = doc.UpdatedAt
	maxSeqID := s.getMaxSeqIDForSession(ctx, sessionID)
	if maxSeqID > session.MessageSeq {
		session.MessageSeq = maxSeqID
	}
	maxToolSeq := s.getMaxToolSeqForSession(ctx, sessionID)
	if maxToolSeq > session.ToolSeq {
		session.ToolSeq = maxToolSeq
	}
	return session, nil
}

func (s *MongoDBStore) GetUserConversation(userID, conversationID string) (*model.Conversation, error) {
	ctx, cancel := s.opCtx()
	defer cancel()

	var doc conversationDocument
	err := s.conversationsCollection.FindOne(ctx, bson.M{"user_id": userID, "_id": scopedMongoID(userID, conversationID)}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		err = s.conversationsCollection.FindOne(ctx, bson.M{"user_id": userID, "_id": conversationID}).Decode(&doc)
	}
	if err == mongo.ErrNoDocuments {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query conversation: %w", err)
	}
	return decodeConversation(doc)
}

func (s *MongoDBStore) GetUserConversationBySession(userID, sessionID string) (*model.Conversation, error) {
	ctx, cancel := s.opCtx()
	defer cancel()

	var doc conversationDocument
	err := s.conversationsCollection.FindOne(ctx, bson.M{"user_id": userID, "session_id": sessionID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query conversation by session: %w", err)
	}
	return decodeConversation(doc)
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

func (s *MongoDBStore) GetUserMessagesBySession(userID, sessionID string) ([]*model.Message, error) {
	var all []*model.Message
	for offset := 0; ; offset += messagesPageSize {
		page, err := s.GetUserMessagesBySessionPage(userID, sessionID, messagesPageSize, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < messagesPageSize {
			return all, nil
		}
	}
}

func (s *MongoDBStore) GetUserFileForUser(userID, fileID string) (*model.UserFile, error) {
	ctx, cancel := s.opCtx()
	defer cancel()

	var doc userFileDocument
	err := s.userFilesCollection.FindOne(ctx, bson.M{"user_id": userID, "_id": scopedMongoID(userID, fileID)}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		err = s.userFilesCollection.FindOne(ctx, bson.M{"user_id": userID, "_id": fileID}).Decode(&doc)
	}
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user file: %w", err)
	}
	f := &model.UserFile{}
	if err := unmarshalJSONOrBSON(doc.Data, f); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user file: %w", err)
	}
	return f, nil
}

func (s *MongoDBStore) GetUserWorkflowRun(userID, workflowID string) (*model.WorkflowRun, error) {
	ctx, cancel := s.opCtx()
	defer cancel()
	var doc workflowRunDocument
	err := s.workflowRunsCollection.FindOne(ctx, bson.M{"user_id": userID, "_id": scopedMongoID(userID, workflowID)}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		err = s.workflowRunsCollection.FindOne(ctx, bson.M{"user_id": userID, "_id": workflowID}).Decode(&doc)
	}
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query workflow run: %w", err)
	}
	workflow := &model.WorkflowRun{}
	if err := unmarshalJSONOrBSON(doc.Data, workflow); err != nil {
		return nil, fmt.Errorf("failed to unmarshal workflow run: %w", err)
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
