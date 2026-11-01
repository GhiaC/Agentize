package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghiac/agentize/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func fileOperationMongoKey(userID, key string) string {
	sum := sha256.Sum256([]byte(userID + "\x00" + key))
	return hex.EncodeToString(sum[:])
}

func (s *MongoDBStore) NextUserFileID(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("file owner is required")
	}
	files, err := s.GetUserFilesByUser(userID)
	if err != nil {
		return "", err
	}
	seed := fileSequenceSeed(files) - 1
	if user, err := s.GetUser(userID); err != nil {
		return "", err
	} else if user != nil && int64(user.FileSeq) > seed {
		seed = int64(user.FileSeq)
	}
	collection := s.database.Collection("file_counters")
	if _, err := collection.UpdateOne(ctx, bson.M{"_id": userID}, bson.M{"$max": bson.M{"seq": seed}, "$set": bson.M{"user_id": userID}}, options.Update().SetUpsert(true)); err != nil {
		return "", err
	}
	var result struct {
		Seq int `bson:"seq"`
	}
	err = collection.FindOneAndUpdate(ctx, bson.M{"_id": userID}, bson.M{"$inc": bson.M{"seq": 1}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&result)
	return model.FormatID(result.Seq), err
}
func (s *MongoDBStore) ClaimFileOperation(ctx context.Context, op *model.FileOperation) (bool, *model.FileOperation, error) {
	if !validFileOperation(op) {
		return false, nil, fmt.Errorf("invalid file operation")
	}
	op.Revision = 1
	op.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(op)
	if err != nil {
		return false, nil, err
	}
	collection := s.database.Collection("file_operations")
	_, err = collection.InsertOne(ctx, bson.M{"_id": fileOperationMongoKey(op.UserID, op.Key), "user_id": op.UserID, "key": op.Key, "revision": op.Revision, "data": string(raw)})
	if err == nil {
		return true, op, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return false, nil, err
	}
	var doc struct {
		Data string `bson:"data"`
	}
	if err := collection.FindOne(ctx, bson.M{"_id": fileOperationMongoKey(op.UserID, op.Key), "user_id": op.UserID}).Decode(&doc); err != nil {
		return false, nil, err
	}
	var old model.FileOperation
	if err := json.Unmarshal([]byte(doc.Data), &old); err != nil {
		return false, nil, err
	}
	return false, &old, nil
}
func (s *MongoDBStore) UpdateFileOperation(ctx context.Context, op *model.FileOperation, revision int64) (bool, error) {
	if !validFileOperation(op) || revision < 1 {
		return false, fmt.Errorf("invalid file operation revision")
	}
	copy := *op
	copy.Revision = revision + 1
	copy.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(&copy)
	if err != nil {
		return false, err
	}
	result, err := s.database.Collection("file_operations").UpdateOne(ctx, bson.M{"_id": fileOperationMongoKey(op.UserID, op.Key), "user_id": op.UserID, "revision": revision}, bson.M{"$set": bson.M{"revision": copy.Revision, "data": string(raw)}})
	if err != nil {
		return false, err
	}
	if result.ModifiedCount == 1 {
		*op = copy
	}
	return result.ModifiedCount == 1, nil
}
func (s *MongoDBStore) ListFileOperations(ctx context.Context, userID string) ([]*model.FileOperation, error) {
	if userID == "" {
		return nil, fmt.Errorf("file owner is required")
	}
	cursor, err := s.database.Collection("file_operations").Find(ctx, bson.M{"user_id": userID}, options.Find().SetSort(bson.D{{Key: "key", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var out []*model.FileOperation
	for cursor.Next(ctx) {
		var doc struct {
			Data string `bson:"data"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		var op model.FileOperation
		if err := json.Unmarshal([]byte(doc.Data), &op); err != nil {
			return nil, err
		}
		out = append(out, &op)
	}
	return out, cursor.Err()
}

func (s *MongoDBStore) CompleteFileUpload(ctx context.Context, op *model.FileOperation, revision int64, file *model.UserFile) (bool, error) {
	if !validFileOperation(op) || file == nil || file.UserID != op.UserID || file.Details.IngestionKey != op.Key {
		return false, fmt.Errorf("invalid upload finalization")
	}
	if err := s.checkUserFileQuota(ctx, file); err != nil {
		return false, err
	}
	session, err := s.client.StartSession()
	if err != nil {
		return false, err
	}
	defer session.EndSession(ctx)
	copy := *op
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (interface{}, error) {
		copy = *op
		copy.State = "ready"
		copy.File = file
		ok, err := s.UpdateFileOperation(sc, &copy, revision)
		if err != nil {
			return nil, err
		}
		if !ok {
			return false, nil
		}
		raw, err := json.Marshal(file)
		if err != nil {
			return nil, err
		}
		doc := userFileDocument{ID: scopedMongoID(file.UserID, file.FileID), UserID: file.UserID, SessionID: file.SessionID, Data: string(raw), CreatedAt: file.CreatedAt}
		_, err = s.userFilesCollection.ReplaceOne(sc, bson.M{"_id": scopedMongoID(file.UserID, file.FileID)}, doc, options.Replace().SetUpsert(true))
		return true, err
	})
	if err != nil {
		return false, err
	}
	if copy.Revision != revision+1 {
		return false, nil
	}
	*op = copy
	return true, nil
}
