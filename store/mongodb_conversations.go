package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghiac/agentize/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type conversationDocument struct {
	ConversationID string    `bson:"_id"`
	UserID         string    `bson:"user_id"`
	SessionID      string    `bson:"session_id"`
	Seq            int       `bson:"conversation_seq"`
	Title          string    `bson:"title"`
	Model          string    `bson:"model"`
	Archived       bool      `bson:"archived"`
	Data           string    `bson:"data"`
	CreatedAt      time.Time `bson:"created_at"`
	UpdatedAt      time.Time `bson:"updated_at"`
}

func decodeConversation(doc conversationDocument) (*model.Conversation, error) {
	c := &model.Conversation{}
	if err := unmarshalJSONOrBSON(doc.Data, c); err != nil {
		return nil, fmt.Errorf("failed to unmarshal conversation: %w", err)
	}
	c.CreatedAt = doc.CreatedAt
	c.UpdatedAt = doc.UpdatedAt
	return c, nil
}

func (s *MongoDBStore) GetConversation(conversationID string) (*model.Conversation, error) {
	ctx, cancel := s.opCtx()
	defer cancel()

	var doc conversationDocument
	err := s.conversationsCollection.FindOne(ctx, bson.M{"_id": conversationID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query conversation: %w", err)
	}
	return decodeConversation(doc)
}

func (s *MongoDBStore) PutConversation(conversation *model.Conversation) error {
	fillConversationIDs(conversation)
	if err := validateConversation(conversation); err != nil {
		return err
	}
	if _, err := s.GetOrCreateUser(conversation.UserID); err != nil {
		return fmt.Errorf("ensure user for conversation: %w", err)
	}
	if conversation.CreatedAt.IsZero() {
		conversation.CreatedAt = time.Now()
	}
	if conversation.UpdatedAt.IsZero() {
		conversation.UpdatedAt = time.Now()
	}
	data, err := json.Marshal(conversation)
	if err != nil {
		return fmt.Errorf("failed to marshal conversation: %w", err)
	}
	doc := conversationDocument{
		ConversationID: conversation.ConversationID,
		UserID:         conversation.UserID,
		SessionID:      conversation.SessionID,
		Seq:            conversation.Seq,
		Title:          conversation.Title,
		Model:          conversation.Model,
		Archived:       conversation.Archived,
		Data:           string(data),
		CreatedAt:      conversation.CreatedAt,
		UpdatedAt:      conversation.UpdatedAt,
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	opts := options.Replace().SetUpsert(true)
	_, err = s.conversationsCollection.ReplaceOne(ctx, bson.M{"_id": conversation.ConversationID}, doc, opts)
	if err != nil {
		return fmt.Errorf("failed to store conversation: %w", err)
	}
	return nil
}

func (s *MongoDBStore) DeleteConversation(conversationID string) error {
	ctx, cancel := s.opCtx()
	defer cancel()
	_, err := s.conversationsCollection.DeleteOne(ctx, bson.M{"_id": conversationID})
	if err != nil {
		return fmt.Errorf("failed to delete conversation: %w", err)
	}
	auditDeletion("conversation", conversationID, "")
	return nil
}

func (s *MongoDBStore) ListConversations(userID string) ([]*model.Conversation, error) {
	ctx, cancel := s.opCtx()
	defer cancel()
	cursor, err := s.conversationsCollection.Find(ctx, bson.M{"user_id": userID}, options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}
	defer cursor.Close(ctx)
	return decodeConversationCursor(ctx, cursor)
}

func (s *MongoDBStore) ListAllConversations() ([]*model.Conversation, error) {
	ctx, cancel := s.opCtx()
	defer cancel()
	cursor, err := s.conversationsCollection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to list all conversations: %w", err)
	}
	defer cursor.Close(ctx)
	return decodeConversationCursor(ctx, cursor)
}

func decodeConversationCursor(ctx context.Context, cursor *mongo.Cursor) ([]*model.Conversation, error) {
	out := make([]*model.Conversation, 0)
	for cursor.Next(ctx) {
		var doc conversationDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode conversation: %w", err)
		}
		c, err := decodeConversation(doc)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *MongoDBStore) GetConversationBySession(sessionID string) (*model.Conversation, error) {
	ctx, cancel := s.opCtx()
	defer cancel()
	var doc conversationDocument
	err := s.conversationsCollection.FindOne(ctx, bson.M{"session_id": sessionID}).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query conversation by session: %w", err)
	}
	return decodeConversation(doc)
}

func (s *MongoDBStore) GetNextConversationSeq(userID string) (int, error) {
	ctx, cancel := s.opCtx()
	defer cancel()
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"user_id": userID}}},
		{{Key: "$group", Value: bson.M{
			"_id":     nil,
			"max_seq": bson.M{"$max": "$conversation_seq"},
		}}},
	}
	cursor, err := s.conversationsCollection.Aggregate(ctx, pipeline)
	if err != nil {
		return 0, fmt.Errorf("failed to aggregate conversations: %w", err)
	}
	defer cursor.Close(ctx)
	var result struct {
		MaxSeq *int `bson:"max_seq"`
	}
	if cursor.Next(ctx) {
		if err := cursor.Decode(&result); err != nil {
			return 0, fmt.Errorf("failed to decode conversation seq: %w", err)
		}
	}
	if result.MaxSeq != nil {
		return *result.MaxSeq + 1, nil
	}
	return 1, nil
}

func (s *MongoDBStore) TouchConversationBySession(sessionID string) error {
	conv, err := s.GetConversationBySession(sessionID)
	if err != nil || conv == nil {
		return err
	}
	bumpConversationActivity(conv)
	return s.PutConversation(conv)
}
