package db

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"strings"
)

// JSONSetArgs simply mirrors go-redis/v9 JSONSetArgs
type JSONSetArgs struct {
	Key   string
	Path  string
	Value interface{}
}

// SearchParams encapsulates the parameters used during a search
type SearchParams struct {
	Param string
	Type  string
	Value []string
}

// GetAllKeys returns all keys matching a certain prefix
func GetAllKeys(ctx context.Context, redisClient *redis.Client, keysPrefix string) ([]string, error) {
	var keys []string

	// Use Scan to efficiently iterate through keys with the specified keysPrefix.
	iter := redisClient.Scan(ctx, 0, keysPrefix+"*", 0).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

// JSONGet returns results from go-redis/v9 JSONGet
func JSONGet(ctx context.Context, redisClient *redis.Client, key string) (string, error) {
	result, err := redisClient.JSONGet(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return result, err
}

// JSONMGet returns results from go-redis/v9 JSONMGet
func JSONMGet(ctx context.Context, redisClient *redis.Client, keys []string) ([]any, error) {
	result, err := redisClient.JSONMGet(ctx, "$", keys...).Result()
	if err == redis.Nil {
		return nil, nil
	}
	return result, err
}

// JSONSet returns results from go-redis/v9 JSONSet
func JSONSet(ctx context.Context, redisClient *redis.Client, key string, path string, value any) (string, error) {
	return redisClient.JSONSet(ctx, key, path, value).Result()
}

// JSONMSetArgs returns  results from go-redis/v9 JSONMSetArgs
func JSONMSetArgs(ctx context.Context, redisClient *redis.Client, setArgs []JSONSetArgs) (string, error) {
	var redisSetArgs []redis.JSONSetArgs
	for _, setArg := range setArgs {
		redisSetArgs = append(redisSetArgs, redis.JSONSetArgs(setArg))
	}
	return redisClient.JSONMSetArgs(ctx, redisSetArgs).Result()
}

// Exists return results from go-redis/v9 Exists
func Exists(ctx context.Context, redisClient *redis.Client, key string) (int64, error) {
	return redisClient.Exists(ctx, key).Result()
}

// Del return results from go-redis/v9 Del
func Del(ctx context.Context, redisClient *redis.Client, key string) (int64, error) {
	return redisClient.Del(ctx, key).Result()
}

// Search perform a FT.SEARCH on the given index
func Search[T any](ctx context.Context, redisClient *redis.Client, indexName string, filters []SearchParams) ([]T, error) {

	var queries []any
	var result []T

	// Build the Search Query
	queries = append(queries, "FT.SEARCH", indexName)
	for _, searchParam := range filters {
		var args []any
		if searchParam.Type == "Slice" {
			args = []any{fmt.Sprintf("@%s:{%s}", searchParam.Param, strings.Join(searchParam.Value, " "))}
		} else {
			args = []any{fmt.Sprintf("@%s:%s", searchParam.Param, strings.Join(searchParam.Value, " "))}
		}

		queries = append(queries, args...)
	}
	queries = append(queries, "DIALECT", "3")

	/*
		Run query FT.SEARCH https://redis.io/commands/ft.search/
		Results on FT.SEARCH returns map[interface{}]interface{}
		that looks like:
		map[attributes:[] format:STRING results:[map[extra_attributes:map[$:{"id":1,"title"...}] id:articleKey:1 values:[]]] total_results:1 warning:[]]
	*/

	redisFtResult, err := redisClient.Do(ctx, queries...).Result()
	if err != nil {
		return result, err
	}

	// Gather Top level map
	topLevel, ok := redisFtResult.(map[interface{}]interface{})
	if !ok {
		return result, fmt.Errorf("response returned when running this search is not a valid map structure")
	}

	// Check TotalResult
	totalResults, ok := topLevel["total_results"].(int64)
	if !ok {
		return result, fmt.Errorf("total Results is not a valid digit")
	}

	if totalResults <= 0 {
		return result, nil
	}

	resultsArray, ok := topLevel["results"].([]any)
	if !ok {
		return result, fmt.Errorf("result from the query is not a valid List of Interfaces")
	}

	// Each item in ResultsArray should be (map[interface{}]interface{}) that has keys id and extra_attributes
	// With the id being Redis Key and extra_attributes being another (map[interface{}]interface{})
	// that contains key->path(e.g. $) and value->Article , we should be able to marshall/unmarshall
	// That object back to type T

	for _, eachResult := range resultsArray {
		res, ok := eachResult.(map[interface{}]interface{})
		if !ok {
			return result, fmt.Errorf("database Search results at first level is in invalid format")
		}
		resAttributes, ok := res["extra_attributes"].(map[interface{}]interface{})
		if !ok {
			return result, fmt.Errorf("database Search result at second level is in invalid format")
		}

		for _, resultArticle := range resAttributes {
			if jsonString, ok := resultArticle.(string); ok {
				var newArticles []T // Use a slice to handle multiple Items
				err = json.Unmarshal([]byte(jsonString), &newArticles)
				if err != nil {
					return result, fmt.Errorf("database result not on expected format, error %v", err)
				}
				result = append(result, newArticles...)
			}
		}
	}
	return result, nil
}
