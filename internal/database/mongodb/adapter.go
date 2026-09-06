package mongodb

import (
	"context"
	"fmt"
	"maps"
	"runtime"
	"sort"
	"strings"
	"sync"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Lumos-Labs-HQ/flash/internal/types"
)

type Adapter struct {
	client   *mongo.Client
	database *mongo.Database
	dbName   string
	mu       sync.RWMutex
}

func New() *Adapter {
	return &Adapter{}
}

func (a *Adapter) Connect(ctx context.Context, url string) error {
	clientOpts := options.Client().ApplyURI(url)
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	if err := client.Ping(ctx, nil); err != nil {
		return fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	a.client = client
	dbName := a.extractDBName(url, clientOpts)

	a.database = client.Database(dbName)
	a.dbName = dbName

	return nil
}

func (a *Adapter) extractDBName(url string, opts *options.ClientOptions) string {
	if len(url) > 0 {
		parts := strings.Split(url, "/")
		if len(parts) > 3 {
			dbPart := parts[len(parts)-1]
			if idx := strings.Index(dbPart, "?"); idx > 0 {
				dbPart = dbPart[:idx]
			}
			if dbPart != "" && dbPart != "admin" {
				return dbPart
			}
		}
	}

	if opts != nil && opts.Auth != nil && opts.Auth.AuthSource != "" && opts.Auth.AuthSource != "admin" {
		return opts.Auth.AuthSource
	}

	return "test"
}

func (a *Adapter) Close() error {
	if a.client != nil {
		return a.client.Disconnect(context.Background())
	}
	return nil
}

func (a *Adapter) Ping(ctx context.Context) error {
	return a.client.Ping(ctx, nil)
}

func (a *Adapter) GetKeyspaces(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (a *Adapter) GetAllTableNames(ctx context.Context) ([]string, error) {
	db := a.currentDB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	names, err := db.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to list collections: %w", err)
	}

	var filtered []string
	for _, name := range names {
		if name != "_flash_migrations" {
			filtered = append(filtered, name)
		}
	}

	return filtered, nil
}

func (a *Adapter) GetTableColumns(ctx context.Context, tableName string) ([]types.SchemaColumn, error) {
	coll := a.currentDB().Collection(tableName)

	cursor, err := coll.Find(ctx, bson.M{}, options.Find().SetLimit(100))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	fieldTypes := make(map[string]string)
	fieldNullable := make(map[string]bool)
	fieldCount := make(map[string]int)
	totalDocs := 0

	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			continue
		}
		totalDocs++

		for key, value := range doc {
			fieldCount[key]++
			inferredType := inferBSONType(value)

			if existingType, exists := fieldTypes[key]; exists {
				if existingType != inferredType {
					fieldTypes[key] = "mixed"
				}
			} else {
				fieldTypes[key] = inferredType
			}
		}
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error while reading columns: %w", err)
	}

	for field, count := range fieldCount {
		fieldNullable[field] = count < totalDocs
	}

	var columns []types.SchemaColumn
	columns = append(columns, types.SchemaColumn{
		Name:      "_id",
		Type:      "ObjectId",
		Nullable:  false,
		IsPrimary: true,
	})

	for field, fieldType := range fieldTypes {
		if field == "_id" {
			continue
		}
		columns = append(columns, types.SchemaColumn{
			Name:     field,
			Type:     fieldType,
			Nullable: fieldNullable[field],
		})
	}

	return columns, nil
}

func (a *Adapter) GetTableData(ctx context.Context, tableName string) ([]map[string]any, error) {
	coll := a.currentDB().Collection(tableName)
	cursor, err := coll.Find(ctx, bson.M{}, options.Find().SetLimit(1000).SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var docs []bson.M
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			continue
		}
		docs = append(docs, doc)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error while reading table data: %w", err)
	}

	results := make([]map[string]any, len(docs))
	var wg sync.WaitGroup
	numWorkers := min(runtime.NumCPU(), len(docs))
	if numWorkers < 1 {
		numWorkers = 1
	}
	chunkSize := (len(docs) + numWorkers - 1) / numWorkers
	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= len(docs) {
			break
		}
		if end > len(docs) {
			end = len(docs)
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				doc := docs[i]
				for k, v := range doc {
					doc[k] = convertBSONValue(v)
				}
				results[i] = doc
			}
		}(start, end)
	}
	wg.Wait()
	return results, nil
}

func (a *Adapter) GetTableRowCount(ctx context.Context, tableName string) (int, error) {
	coll := a.currentDB().Collection(tableName)
	count, err := coll.CountDocuments(ctx, bson.M{})
	return int(count), err
}

func (a *Adapter) GetAllTableRowCounts(ctx context.Context, tableNames []string) (map[string]int, error) {
	result := make(map[string]int)
	for _, name := range tableNames {
		count, _ := a.GetTableRowCount(ctx, name)
		result[name] = count
	}
	return result, nil
}

// ExecuteMongoQuery executes a MongoDB query string
func (a *Adapter) ExecuteMongoQuery(ctx context.Context, query string) ([]map[string]any, error) {
	query = strings.TrimSpace(query)
	query = strings.TrimPrefix(query, "db.")

	parts := strings.SplitN(query, ".", 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid query format. Use: collection.find({}) or db.collection.find({})")
	}

	collectionName := parts[0]
	operation := parts[1]

	coll := a.currentDB().Collection(collectionName)

	if strings.HasPrefix(operation, "find(") {
		filterStr := extractBetweenBalanced(operation, "find(", ")")
		if filterStr == "" {
			filterStr = "{}"
		}

		var filter bson.M
		if err := bson.UnmarshalExtJSON([]byte(filterStr), false, &filter); err != nil {
			filter = bson.M{}
		}

		cursor, err := coll.Find(ctx, filter, options.Find().SetLimit(100))
		if err != nil {
			return nil, err
		}
		defer cursor.Close(ctx)

		var docs []bson.M
		for cursor.Next(ctx) {
			var doc bson.M
			if err := cursor.Decode(&doc); err != nil {
				continue
			}
			docs = append(docs, doc)
		}
		if err := cursor.Err(); err != nil {
			return nil, fmt.Errorf("cursor error: %w", err)
		}

		results := make([]map[string]any, len(docs))
		var wg sync.WaitGroup
		for i, doc := range docs {
			wg.Add(1)
			go func(idx int, d bson.M) {
				defer wg.Done()
				for k, v := range d {
					d[k] = convertBSONValue(v)
				}
				results[idx] = d
			}(i, doc)
		}
		wg.Wait()
		return results, nil
	}

	if strings.HasPrefix(operation, "count(") {
		count, err := coll.CountDocuments(ctx, bson.M{})
		if err != nil {
			return nil, err
		}
		return []map[string]any{{"count": count}}, nil
	}

	return nil, fmt.Errorf("unsupported operation. Supported: find({}), count()")
}

// ListCollections returns all collection names
func (a *Adapter) ListCollections(ctx context.Context) ([]string, error) {
	return a.ListCollectionsInDB(ctx, a.currentDBName())
}

func (a *Adapter) ListCollectionsInDB(ctx context.Context, database string) ([]string, error) {
	db := a.client.Database(database)
	names, err := db.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		return nil, err
	}

	var filtered []string
	for _, name := range names {
		if !strings.HasPrefix(name, "system.") {
			filtered = append(filtered, name)
		}
	}
	return filtered, nil
}

// GetCollectionStats returns statistics for a collection
func (a *Adapter) GetCollectionStats(ctx context.Context, collection string) (map[string]any, error) {
	var result bson.M
	err := a.currentDB().RunCommand(ctx, bson.D{{Key: "collStats", Value: collection}}).Decode(&result)
	if err != nil {
		return nil, err
	}

	stats := make(map[string]any)
	maps.Copy(stats, result)
	return stats, nil
}

// FindDocuments finds documents in a collection with pagination
func (a *Adapter) FindDocuments(ctx context.Context, collection string, filter bson.M, skip, limit int64) ([]map[string]any, error) {
	return a.FindDocumentsInDB(ctx, a.currentDBName(), collection, filter, skip, limit)
}

// FindDocumentsInDB finds documents in a specific database and collection with pagination
func (a *Adapter) FindDocumentsInDB(ctx context.Context, database, collection string, filter bson.M, skip, limit int64) ([]map[string]any, error) {
	db := a.client.Database(database)
	coll := db.Collection(collection)
	opts := options.Find().SetSkip(skip).SetLimit(limit).SetSort(bson.D{{Key: "_id", Value: 1}})

	cursor, err := coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	// Decode all documents first, then convert in parallel
	var docs []bson.M
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			continue
		}
		docs = append(docs, doc)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error: %w", err)
	}

	results := make([]map[string]any, len(docs))
	var wg sync.WaitGroup
	numWorkers := min(runtime.NumCPU(), len(docs))
	if numWorkers < 1 {
		numWorkers = 1
	}

	chunkSize := (len(docs) + numWorkers - 1) / numWorkers
	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= len(docs) {
			break
		}
		if end > len(docs) {
			end = len(docs)
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				doc := docs[i]
				for k, v := range doc {
					doc[k] = convertBSONValue(v)
				}
				results[i] = doc
			}
		}(start, end)
	}
	wg.Wait()
	return results, nil
}

// CountDocuments counts documents in a collection
func (a *Adapter) CountDocuments(ctx context.Context, collection string, filter bson.M) (int64, error) {
	return a.CountDocumentsInDB(ctx, a.currentDBName(), collection, filter)
}

// CountDocumentsInDB counts documents in a specific database and collection
func (a *Adapter) CountDocumentsInDB(ctx context.Context, database, collection string, filter bson.M) (int64, error) {
	db := a.client.Database(database)
	coll := db.Collection(collection)
	count, err := coll.CountDocuments(ctx, filter)
	return count, err
}

// EstimatedDocumentCountInDB returns an estimated count using collection metadata (fast)
func (a *Adapter) EstimatedDocumentCountInDB(ctx context.Context, database, collection string) (int64, error) {
	db := a.client.Database(database)
	coll := db.Collection(collection)
	count, err := coll.EstimatedDocumentCount(ctx)
	return count, err
}

// InsertDocument inserts a document into a collection
func (a *Adapter) InsertDocument(ctx context.Context, collection string, document any) (string, error) {
	coll := a.currentDB().Collection(collection)
	result, err := coll.InsertOne(ctx, document)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", result.InsertedID), nil
}

// UpdateDocument updates a document in a collection
func (a *Adapter) UpdateDocument(ctx context.Context, collection string, id string, update any) error {
	coll := a.currentDB().Collection(collection)
	objectID, err := parseObjectID(id)
	if err != nil {
		return err
	}
	_, err = coll.UpdateOne(ctx, bson.M{"_id": objectID}, update)
	return err
}

// DeleteDocument deletes a document from a collection
func (a *Adapter) DeleteDocument(ctx context.Context, collection string, id string) error {
	coll := a.currentDB().Collection(collection)
	objectID, err := parseObjectID(id)
	if err != nil {
		return err
	}
	_, err = coll.DeleteOne(ctx, bson.M{"_id": objectID})
	return err
}

// BulkDeleteDocuments deletes multiple documents by IDs using $in operator
func (a *Adapter) BulkDeleteDocuments(ctx context.Context, collection string, ids []string) (int64, error) {
	coll := a.currentDB().Collection(collection)

	objectIDs := make([]any, 0, len(ids))
	for _, id := range ids {
		objectID, err := parseObjectID(id)
		if err != nil {
			return 0, fmt.Errorf("invalid ID %s: %w", id, err)
		}
		objectIDs = append(objectIDs, objectID)
	}

	result, err := coll.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": objectIDs}})
	if err != nil {
		return 0, err
	}
	return result.DeletedCount, nil
}

// CreateCollection creates a new collection
func (a *Adapter) CreateCollection(ctx context.Context, name string, opts any) error {
	var createOpts []*options.CreateCollectionOptions
	if o, ok := opts.(*options.CreateCollectionOptions); ok && o != nil {
		createOpts = append(createOpts, o)
	}
	return a.currentDB().CreateCollection(ctx, name, createOpts...)
}

// DropCollection drops a collection
func (a *Adapter) DropCollection(ctx context.Context, name string) error {
	return a.currentDB().Collection(name).Drop(ctx)
}

// Aggregate runs an aggregation pipeline
func (a *Adapter) Aggregate(ctx context.Context, collection string, pipeline any) ([]map[string]any, error) {
	coll := a.currentDB().Collection(collection)
	cursor, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var docs []bson.M
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			continue
		}
		docs = append(docs, doc)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error: %w", err)
	}

	results := make([]map[string]any, len(docs))
	var wg sync.WaitGroup
	for i, doc := range docs {
		wg.Add(1)
		go func(idx int, d bson.M) {
			defer wg.Done()
			for k, v := range d {
				d[k] = convertBSONValue(v)
			}
			results[idx] = d
		}(i, doc)
	}
	wg.Wait()
	return results, nil
}

// ListIndexes lists all indexes for a collection
func (a *Adapter) ListIndexes(ctx context.Context, collection string) ([]map[string]any, error) {
	coll := a.currentDB().Collection(collection)
	cursor, err := coll.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var items []bson.M
	for cursor.Next(ctx) {
		var index bson.M
		if err := cursor.Decode(&index); err != nil {
			continue
		}
		items = append(items, index)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error: %w", err)
	}

	results := make([]map[string]any, len(items))
	var wg sync.WaitGroup
	for i, index := range items {
		wg.Add(1)
		go func(idx int, d bson.M) {
			defer wg.Done()
			for k, v := range d {
				d[k] = convertBSONValue(v)
			}
			results[idx] = d
		}(i, index)
	}
	wg.Wait()
	return results, nil
}

// CreateIndex creates a new index on a collection
func (a *Adapter) CreateIndex(ctx context.Context, collection string, keys map[string]any, unique bool) error {
	coll := a.currentDB().Collection(collection)
	indexModel := mongo.IndexModel{
		Keys:    keys,
		Options: options.Index().SetUnique(unique),
	}
	_, err := coll.Indexes().CreateOne(ctx, indexModel)
	return err
}

// DropIndex drops an index from a collection
func (a *Adapter) DropIndex(ctx context.Context, collection string, indexName string) error {
	coll := a.currentDB().Collection(collection)
	_, err := coll.Indexes().DropOne(ctx, indexName)
	return err
}

// GetDatabaseStats returns database statistics
func (a *Adapter) GetDatabaseStats(ctx context.Context) (map[string]any, error) {
	var result bson.M
	err := a.currentDB().RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}}).Decode(&result)
	if err != nil {
		return nil, err
	}

	stats := make(map[string]any)
	maps.Copy(stats, result)
	return stats, nil
}

// ListDatabases lists all databases
func (a *Adapter) ListDatabases(ctx context.Context) ([]map[string]any, error) {
	databases, err := a.client.ListDatabases(ctx, bson.M{})
	if err != nil {
		return nil, err
	}

	result := make([]map[string]any, 0)
	for _, db := range databases.Databases {
		result = append(result, map[string]any{
			"name":       db.Name,
			"sizeOnDisk": db.SizeOnDisk,
			"empty":      db.Empty,
		})
	}
	return result, nil
}

// SwitchDatabase switches to a different database
func (a *Adapter) SwitchDatabase(dbName string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.database = a.client.Database(dbName)
	a.dbName = dbName
	return nil
}

func (a *Adapter) currentDB() *mongo.Database {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.database
}

func (a *Adapter) currentDBName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.dbName
}

// DropDatabase drops a database
func (a *Adapter) DropDatabase(ctx context.Context, dbName string) error {
	return a.client.Database(dbName).Drop(ctx)
}

// CreateDatabase creates a new database by creating an initial collection
func (a *Adapter) CreateDatabase(ctx context.Context, dbName string) error {
	db := a.client.Database(dbName)
	err := db.CreateCollection(ctx, "_init")
	if err != nil {
		return fmt.Errorf("failed to create database: %w", err)
	}
	return nil
}

// GetCollectionSchemaInDB samples documents and returns inferred schema as maps
func (a *Adapter) GetCollectionSchemaInDB(ctx context.Context, database, collection string, sampleSize int) ([]map[string]any, error) {
	db := a.client.Database(database)
	coll := db.Collection(collection)

	// Use aggregation with $sample for efficient random sampling
	pipeline := mongo.Pipeline{
		{{Key: "$sample", Value: bson.D{{Key: "size", Value: sampleSize}}}},
	}

	cursor, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	fields := make(map[string]map[string]any)
	totalDocs := 0

	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			continue
		}
		totalDocs++

		for key, value := range doc {
			if _, ok := fields[key]; !ok {
				fields[key] = map[string]any{
					"name":     key,
					"type":     "",
					"nullable": false,
					"count":    0,
				}
			}
			field := fields[key]
			field["count"] = field["count"].(int) + 1
			inferredType := inferBSONType(value)
			currentType := field["type"].(string)
			if currentType == "" {
				field["type"] = inferredType
			} else if currentType != inferredType && !strings.Contains(currentType, inferredType) {
				field["type"] = currentType + " | " + inferredType
			}
			if value == nil {
				field["nullable"] = true
			}
		}
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error: %w", err)
	}

	result := make([]map[string]any, 0, len(fields))
	for _, field := range fields {
		count := field["count"].(int)
		if count < totalDocs {
			field["nullable"] = true
		}
		if totalDocs > 0 {
			field["frequency"] = (count * 100) / totalDocs
		} else {
			field["frequency"] = 0
		}
		delete(field, "count")
		result = append(result, field)
	}

	// Sort by name for consistent output
	sort.Slice(result, func(i, j int) bool {
		return result[i]["name"].(string) < result[j]["name"].(string)
	})

	return result, nil
}
