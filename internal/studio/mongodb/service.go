package mongodb

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/Lumos-Labs-HQ/flash/internal/database"
)

type Service struct {
	adapter database.DatabaseAdapter
	ctx     context.Context
}

type DatabaseInfo struct {
	Name       string `json:"name"`
	SizeOnDisk int64  `json:"sizeOnDisk"`
	Empty      bool   `json:"empty"`
}

type CollectionInfo struct {
	Name          string `json:"name"`
	DocumentCount int64  `json:"document_count"`
	Size          int64  `json:"size"`
	AvgObjSize    int64  `json:"avg_obj_size"`
}

type DocumentResult struct {
	Documents  []map[string]any `json:"documents"`
	TotalCount int64            `json:"total_count"`
	Page       int              `json:"page"`
	Limit      int              `json:"limit"`
}

type IndexInfo struct {
	Name   string         `json:"name"`
	Keys   map[string]any `json:"keys"`
	Unique bool           `json:"unique"`
}

type Stats struct {
	DatabaseName   string `json:"database_name"`
	Collections    int    `json:"collections"`
	TotalSize      int64  `json:"total_size"`
	TotalDocuments int64  `json:"total_documents"`
}

func NewService(adapter database.DatabaseAdapter) *Service {
	return &Service{
		adapter: adapter,
		ctx:     context.Background(),
	}
}

// SwitchDatabase switches to a different database
func (s *Service) SwitchDatabase(dbName string) error {
	mongoAdapter, ok := s.adapter.(interface {
		SwitchDatabase(string) error
	})

	if !ok {
		return fmt.Errorf("adapter does not support SwitchDatabase")
	}

	return mongoAdapter.SwitchDatabase(dbName)
}

// DropDatabase drops a database
func (s *Service) DropDatabase(dbName string) error {
	mongoAdapter, ok := s.adapter.(interface {
		DropDatabase(context.Context, string) error
	})

	if !ok {
		return fmt.Errorf("adapter does not support DropDatabase")
	}

	return mongoAdapter.DropDatabase(s.ctx, dbName)
}

// CreateDatabase creates a new database by creating an initial collection
func (s *Service) CreateDatabase(dbName string) error {
	mongoAdapter, ok := s.adapter.(interface {
		CreateDatabase(context.Context, string) error
	})

	if !ok {
		return fmt.Errorf("adapter does not support CreateDatabase")
	}

	return mongoAdapter.CreateDatabase(s.ctx, dbName)
}

// GetDatabases lists all databases
func (s *Service) GetDatabases() ([]DatabaseInfo, error) {
	mongoAdapter, ok := s.adapter.(interface {
		ListDatabases(context.Context) ([]map[string]any, error)
	})

	if !ok {
		return nil, fmt.Errorf("adapter does not support ListDatabases")
	}

	dbList, err := mongoAdapter.ListDatabases(s.ctx)
	if err != nil {
		return nil, err
	}

	databases := make([]DatabaseInfo, len(dbList))
	for i, db := range dbList {
		var sizeOnDisk int64
		switch v := db["sizeOnDisk"].(type) {
		case int64:
			sizeOnDisk = v
		case float64:
			sizeOnDisk = int64(v)
		case int:
			sizeOnDisk = int64(v)
		default:
			sizeOnDisk = 0
		}

		var name string
		if n, ok := db["name"].(string); ok {
			name = n
		}
		var empty bool
		if e, ok := db["empty"].(bool); ok {
			empty = e
		}
		databases[i] = DatabaseInfo{
			Name:       name,
			SizeOnDisk: sizeOnDisk,
			Empty:      empty,
		}
	}

	return databases, nil
}

// GetCollections returns all collections in the database
func (s *Service) GetCollections(database string) ([]CollectionInfo, error) {
	type MongoCollectionOps interface {
		ListCollectionsInDB(ctx context.Context, database string) ([]string, error)
		CountDocumentsInDB(ctx context.Context, database, collection string, filter bson.M) (int64, error)
	}

	mongoAdapter, ok := s.adapter.(MongoCollectionOps)
	if !ok {
		return nil, fmt.Errorf("adapter does not support MongoDB operations")
	}

	collections, err := mongoAdapter.ListCollectionsInDB(s.ctx, database)
	if err != nil {
		return nil, err
	}

	result := make([]CollectionInfo, len(collections))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, coll := range collections {
		wg.Add(1)
		go func(index int, name string) {
			defer wg.Done()
			count, err := mongoAdapter.CountDocumentsInDB(s.ctx, database, name, bson.M{})
			info := CollectionInfo{
				Name:          name,
				DocumentCount: count,
				Size:          0,
				AvgObjSize:    0,
			}
			if err != nil {
				info.DocumentCount = 0
			}
			mu.Lock()
			result[index] = info
			mu.Unlock()
		}(i, coll)
	}

	wg.Wait()
	return result, nil
}

// GetDocuments returns documents from a collection with pagination
func (s *Service) GetDocuments(database, collection string, page, limit int) (*DocumentResult, error) {
	return s.GetDocumentsWithFilter(database, collection, page, limit, bson.M{})
}

func (s *Service) GetDocumentsWithFilter(database, collection string, page, limit int, filter bson.M) (*DocumentResult, error) {
	type MongoDocumentReader interface {
		FindDocumentsInDB(ctx context.Context, database, collection string, filter bson.M, skip, limit int64) ([]map[string]any, error)
		CountDocumentsInDB(ctx context.Context, database, collection string, filter bson.M) (int64, error)
		EstimatedDocumentCountInDB(ctx context.Context, database, collection string) (int64, error)
	}

	mongoAdapter, ok := s.adapter.(MongoDocumentReader)
	if !ok {
		return nil, fmt.Errorf("adapter does not support MongoDB operations")
	}

	if filter == nil {
		filter = bson.M{}
	}

	skip := int64((page - 1) * limit)

	var documents []map[string]any
	var totalCount int64
	var docErr, countErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		documents, docErr = mongoAdapter.FindDocumentsInDB(s.ctx, database, collection, filter, skip, int64(limit))
	}()

	go func() {
		defer wg.Done()
		if len(filter) == 0 {
			totalCount, countErr = mongoAdapter.EstimatedDocumentCountInDB(s.ctx, database, collection)
		} else {
			totalCount, countErr = mongoAdapter.CountDocumentsInDB(s.ctx, database, collection, filter)
		}
	}()

	wg.Wait()

	if docErr != nil {
		return nil, docErr
	}
	if countErr != nil {
		return nil, countErr
	}

	return &DocumentResult{
		Documents:  documents,
		TotalCount: totalCount,
		Page:       page,
		Limit:      limit,
	}, nil
}

// InsertDocument inserts a new document
func (s *Service) InsertDocument(collection string, document map[string]any) (string, error) {
	type MongoDocumentWriter interface {
		InsertDocument(ctx context.Context, collection string, document any) (string, error)
	}

	mongoAdapter, ok := s.adapter.(MongoDocumentWriter)
	if !ok {
		return "", fmt.Errorf("adapter does not support MongoDB operations")
	}

	s.processDocumentTypes(document)

	return mongoAdapter.InsertDocument(s.ctx, collection, document)
}

// UpdateDocument updates an existing document
func (s *Service) UpdateDocument(collection, id string, document map[string]any) error {
	type MongoDocumentUpdater interface {
		UpdateDocument(ctx context.Context, collection string, id string, update any) error
	}

	mongoAdapter, ok := s.adapter.(MongoDocumentUpdater)
	if !ok {
		return fmt.Errorf("adapter does not support MongoDB operations")
	}

	s.processDocumentTypes(document)

	return mongoAdapter.UpdateDocument(s.ctx, collection, id, bson.M{"$set": document})
}

// DeleteDocument deletes a document by ID
func (s *Service) DeleteDocument(collection, id string) error {
	type MongoDocumentDeleter interface {
		DeleteDocument(ctx context.Context, collection string, id string) error
	}

	mongoAdapter, ok := s.adapter.(MongoDocumentDeleter)
	if !ok {
		return fmt.Errorf("adapter does not support MongoDB operations")
	}

	return mongoAdapter.DeleteDocument(s.ctx, collection, id)
}

// BulkDeleteDocuments deletes multiple documents efficiently using $in operator
func (s *Service) BulkDeleteDocuments(collection string, ids []string) error {
	// Try bulk delete first (more efficient)
	type MongoBulkDeleter interface {
		BulkDeleteDocuments(ctx context.Context, collection string, ids []string) (int64, error)
	}

	if bulkAdapter, ok := s.adapter.(MongoBulkDeleter); ok {
		_, err := bulkAdapter.BulkDeleteDocuments(s.ctx, collection, ids)
		return err
	}

	// Fallback to one-by-one deletion
	type MongoDocumentDeleter interface {
		DeleteDocument(ctx context.Context, collection string, id string) error
	}

	mongoAdapter, ok := s.adapter.(MongoDocumentDeleter)
	if !ok {
		return fmt.Errorf("adapter does not support MongoDB operations")
	}

	for _, id := range ids {
		if err := mongoAdapter.DeleteDocument(s.ctx, collection, id); err != nil {
			return fmt.Errorf("failed to delete document %s: %w", id, err)
		}
	}

	return nil
}

// CreateCollection creates a new collection
func (s *Service) CreateCollection(name string, options map[string]any) error {
	type MongoCollectionCreator interface {
		CreateCollection(ctx context.Context, name string, options any) error
	}

	mongoAdapter, ok := s.adapter.(MongoCollectionCreator)
	if !ok {
		return fmt.Errorf("adapter does not support MongoDB operations")
	}

	return mongoAdapter.CreateCollection(s.ctx, name, options)
}

// DropCollection drops a collection
func (s *Service) DropCollection(name string) error {
	type MongoCollectionDropper interface {
		DropCollection(ctx context.Context, name string) error
	}

	mongoAdapter, ok := s.adapter.(MongoCollectionDropper)
	if !ok {
		return fmt.Errorf("adapter does not support MongoDB operations")
	}

	return mongoAdapter.DropCollection(s.ctx, name)
}

// Aggregate runs an aggregation pipeline
func (s *Service) Aggregate(collection string, pipeline []bson.M) ([]map[string]any, error) {
	type MongoAggregator interface {
		Aggregate(ctx context.Context, collection string, pipeline any) ([]map[string]any, error)
	}

	mongoAdapter, ok := s.adapter.(MongoAggregator)
	if !ok {
		return nil, fmt.Errorf("adapter does not support MongoDB operations")
	}

	return mongoAdapter.Aggregate(s.ctx, collection, pipeline)
}

// GetIndexes returns all indexes for a collection
func (s *Service) GetIndexes(collection string) ([]IndexInfo, error) {
	type MongoIndexReader interface {
		ListIndexes(ctx context.Context, collection string) ([]map[string]any, error)
	}

	mongoAdapter, ok := s.adapter.(MongoIndexReader)
	if !ok {
		return nil, fmt.Errorf("adapter does not support MongoDB operations")
	}

	indexes, err := mongoAdapter.ListIndexes(s.ctx, collection)
	if err != nil {
		return nil, err
	}

	result := make([]IndexInfo, 0, len(indexes))
	for _, idx := range indexes {
		info := IndexInfo{}

		if name, ok := idx["name"].(string); ok {
			info.Name = name
		}
		if key, ok := idx["key"].(map[string]any); ok {
			info.Keys = key
		}
		if unique, ok := idx["unique"].(bool); ok {
			info.Unique = unique
		}

		result = append(result, info)
	}

	return result, nil
}

// CreateIndex creates a new index
func (s *Service) CreateIndex(collection string, keys map[string]any, unique bool) error {
	type MongoIndexCreator interface {
		CreateIndex(ctx context.Context, collection string, keys map[string]any, unique bool) error
	}

	mongoAdapter, ok := s.adapter.(MongoIndexCreator)
	if !ok {
		return fmt.Errorf("adapter does not support MongoDB operations")
	}

	return mongoAdapter.CreateIndex(s.ctx, collection, keys, unique)
}

// DropIndex drops an index
func (s *Service) DropIndex(collection, indexName string) error {
	type MongoIndexDropper interface {
		DropIndex(ctx context.Context, collection string, indexName string) error
	}

	mongoAdapter, ok := s.adapter.(MongoIndexDropper)
	if !ok {
		return fmt.Errorf("adapter does not support MongoDB operations")
	}

	return mongoAdapter.DropIndex(s.ctx, collection, indexName)
}

// Query executes a custom query
func (s *Service) Query(collection string, filter bson.M, limit int) ([]map[string]any, error) {
	type MongoDocumentReader interface {
		FindDocuments(ctx context.Context, collection string, filter bson.M, skip, limit int64) ([]map[string]any, error)
	}

	mongoAdapter, ok := s.adapter.(MongoDocumentReader)
	if !ok {
		return nil, fmt.Errorf("adapter does not support MongoDB operations")
	}

	return mongoAdapter.FindDocuments(s.ctx, collection, filter, 0, int64(limit))
}

// GetStats returns database statistics
func (s *Service) GetStats() (*Stats, error) {
	type MongoStatsReader interface {
		GetDatabaseStats(ctx context.Context) (map[string]any, error)
		ListCollections(ctx context.Context) ([]string, error)
	}

	mongoAdapter, ok := s.adapter.(MongoStatsReader)
	if !ok {
		return nil, fmt.Errorf("adapter does not support MongoDB operations")
	}

	stats, err := mongoAdapter.GetDatabaseStats(s.ctx)
	if err != nil {
		return nil, err
	}

	collections, _ := mongoAdapter.ListCollections(s.ctx)

	result := &Stats{
		Collections: len(collections),
	}

	if dbName, ok := stats["db"].(string); ok {
		result.DatabaseName = dbName
	}
	// MongoDB dbStats returns numeric values as float64 or int, not int64
	if dataSize, ok := stats["dataSize"].(float64); ok {
		result.TotalSize = int64(dataSize)
	} else if dataSize, ok := stats["dataSize"].(int); ok {
		result.TotalSize = int64(dataSize)
	} else if dataSize, ok := stats["dataSize"].(int64); ok {
		result.TotalSize = dataSize
	}
	if objects, ok := stats["objects"].(float64); ok {
		result.TotalDocuments = int64(objects)
	} else if objects, ok := stats["objects"].(int); ok {
		result.TotalDocuments = int64(objects)
	} else if objects, ok := stats["objects"].(int64); ok {
		result.TotalDocuments = objects
	}

	return result, nil
}

// GetCollectionStats returns statistics for a specific collection
func (s *Service) GetCollectionStats(collection string) (map[string]any, error) {
	type MongoCollectionLister interface {
		GetCollectionStats(ctx context.Context, collection string) (map[string]any, error)
	}

	mongoAdapter, ok := s.adapter.(MongoCollectionLister)
	if !ok {
		return nil, fmt.Errorf("adapter does not support MongoDB operations")
	}

	return mongoAdapter.GetCollectionStats(s.ctx, collection)
}

// SchemaField represents inferred schema information for a field
type SchemaField struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Nullable  bool   `json:"nullable"`
	Frequency int    `json:"frequency"`
}

// GetCollectionSchema returns inferred schema from a sample of documents
func (s *Service) GetCollectionSchema(database, collection string) ([]SchemaField, error) {
	type MongoSchemaReader interface {
		GetCollectionSchemaInDB(ctx context.Context, database, collection string, sampleSize int) ([]map[string]any, error)
	}

	mongoAdapter, ok := s.adapter.(MongoSchemaReader)
	if !ok {
		return nil, fmt.Errorf("adapter does not support schema inference")
	}

	// Sample only 10 documents — enough for schema inference, fast even on huge collections
	adapterFields, err := mongoAdapter.GetCollectionSchemaInDB(s.ctx, database, collection, 10)
	if err != nil {
		return nil, err
	}

	result := make([]SchemaField, len(adapterFields))
	for i, f := range adapterFields {
		result[i] = SchemaField{
			Name:      getString(f, "name"),
			Type:      getString(f, "type"),
			Nullable:  getBool(f, "nullable"),
			Frequency: getInt(f, "frequency"),
		}
	}
	return result, nil
}

// processDocumentTypes ensures proper types for MongoDB operations
func (s *Service) processDocumentTypes(doc map[string]any) {
	for key, value := range doc {
		switch v := value.(type) {
		case string:
			// Try ObjectID first, then RFC3339 date; use else-if to avoid overwriting
			if len(v) == 24 {
				if objectID, err := primitive.ObjectIDFromHex(v); err == nil {
					doc[key] = objectID
					continue
				}
			}
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				doc[key] = primitive.NewDateTimeFromTime(t)
			}
		case map[string]any:
			s.processDocumentTypes(v)
		case []any:
			for i, item := range v {
				if m, ok := item.(map[string]any); ok {
					s.processDocumentTypes(m)
					v[i] = m
				}
			}
		}
	}
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getBool(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func getInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case int32:
		return int(v)
	default:
		return 0
	}
}
