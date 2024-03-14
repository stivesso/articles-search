package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-playground/validator/v10"
	"github.com/redis/go-redis/v9"
	"io"
	"log"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strconv"
)

// Article represents the structure of an Article.
type Article struct {
	Id      uint     `json:"id" validate:"required"`
	Title   string   `json:"title" validate:"required"`
	Content string   `json:"content" validate:"omitempty"`
	Author  string   `json:"author" validate:"omitempty"`
	Tags    []string `json:"tags" validate:"omitempty"`
}

// CustomOutput for standardized error and message responses.
type CustomOutput struct {
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

var (
	redisClient     *redis.Client
	ctx                                 = context.Background()
	validate        *validator.Validate = validator.New()
	searchIndexName                     = "idx_articles"
	keysPrefix                          = "article:"
)

func main() {
	// Initialize Redis client.
	err := initializeRedis()
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// Setup HTTP server and routes.
	setupHTTPServer()
}

func initializeRedis() error {
	var err error
	redisClient, err = NewRedisClient("192.168.64.7", 30183, "", 0)
	return err
}

func NewRedisClient(dbHost string, dbPort int, dbPassword string, dbRedis int) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", dbHost, dbPort),
		Password: dbPassword, //For this test, not setting Authentication
		DB:       dbRedis,    // For this Te
	})
	// Ping the redis server to check connection
	_, err := client.Ping(context.Background()).Result()
	return client, err
}

func setupHTTPServer() {
	mux := http.NewServeMux()

	// Define routes using pattern matching for IDs.
	mux.HandleFunc("GET /articles", getAllArticles)
	mux.HandleFunc("GET /article/{id}", getArticleByID)
	mux.HandleFunc("POST /article", createArticle)
	mux.HandleFunc("PUT /article/{id}", updateArticleByID)
	mux.HandleFunc("DELETE /article/{id}", deleteArticleByID)
	mux.HandleFunc("GET /articles/search", searchArticles)

	serverAddress := ":8080"
	slog.Info(fmt.Sprintf("Starting HTTP Server on address %s\n", serverAddress))
	if err := http.ListenAndServe(serverAddress, mux); err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}
}

// responseJSON simplifies JSON response writing.
func responseJSON(w http.ResponseWriter, v interface{}, statusCode int) {
	jsonResp, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(statusCode)
	w.Header().Set("Content-Type", "application/json")
	nbrBytesWritten, err := w.Write(jsonResp)
	if err != nil {
		slog.Error("Unable to write the following response", "response", jsonResp, "lenght_response", nbrBytesWritten)
	}
}

// handleError simplifies error handling and response.
func handleError(w http.ResponseWriter, errMsg string, err error, statusCode int) {
	//Logging any 5xx error
	if statusCode >= http.StatusInternalServerError {
		slog.Error(errMsg, "Error:", err)
	}
	responseJSON(w, CustomOutput{Error: err.Error(), Message: errMsg}, statusCode)
}

/*
Handlers Functions
*/

func getAllArticles(w http.ResponseWriter, r *http.Request) {
	var keys []string
	var articles []Article

	// Use Scan to efficiently iterate through keys with the specified keysPrefix.
	iter := redisClient.Scan(ctx, 0, keysPrefix+"*", 0).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		handleError(w, "Failed to retrieve article keys from Redis", err, http.StatusInternalServerError)
		return
	}

	if len(keys) == 0 {
		// No articles found, return an empty list with HTTP 200 OK.
		responseJSON(w, articles, http.StatusOK)
		return
	}

	// Retrieve article details for each key
	resultMget, err := redisClient.JSONMGet(ctx, "$", keys...).Result()
	if err != nil && err != redis.Nil {
		handleError(w, "An Error Occurred while Getting Articles", err, http.StatusInternalServerError)
		return
	}
	if err == redis.Nil {
		// No articles found, return an empty list with HTTP 200 OK.
		responseJSON(w, articles, http.StatusOK)
		return
	}

	// Loop on each element in the array and append its first element to the result after validation
	var result []Article
	for _, responseRetrievedArticle := range resultMget {
		var resultForThisArticle []Article
		responseArticle, isString := responseRetrievedArticle.(string)
		if !isString {
			handleError(w, "An Error Occurred while Getting Articles", fmt.Errorf("article returned in incorrect format"), http.StatusInternalServerError)
			return
		}
		err = json.Unmarshal([]byte(responseArticle), &resultForThisArticle)
		if err != nil {
			handleError(w, "Unable to validate the structure of returned Article", err, http.StatusInternalServerError)
			return
		}
		result = append(result, resultForThisArticle[0])
	}

	responseJSON(w, result, http.StatusOK)
}

func getArticleByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Build the Redis key using the article ID.
	key := fmt.Sprintf("%s%s", keysPrefix, id)

	// Retrieve the article from Redis.
	result, err := redisClient.JSONGet(ctx, key).Result()
	if err == redis.Nil {
		// Article not found, respond with HTTP 404 Not Found.
		handleError(w, fmt.Sprintf("No article found with ID %s", id), nil, http.StatusNotFound)
		return
	} else if err != nil {
		// Handle unexpected Redis errors.
		handleError(w, "Failed to retrieve article from Redis", err, http.StatusInternalServerError)
		return
	}

	// Unmarshal the article JSON into the Article struct.
	var article Article
	if err := json.Unmarshal([]byte(result), &article); err != nil {
		handleError(w, "Failed to parse article data", err, http.StatusInternalServerError)
		return
	}

	// Return the article as JSON.
	responseJSON(w, article, http.StatusOK)
}

func createArticle(w http.ResponseWriter, r *http.Request) {
	// Using Same function to process either  a single article or a list of articles

	// Read the entire request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		handleError(w, "Failed to read request body", err, http.StatusInternalServerError)
		return
	}

	// First, try unmarshalling into a slice of articles
	var articles []Article
	errSlice := json.Unmarshal(bodyBytes, &articles)

	// If unmarshalling into a slice fails, try unmarshalling into a single article
	if errSlice != nil {
		var singleArticle Article
		errSingle := json.Unmarshal(bodyBytes, &singleArticle)
		if errSingle != nil {
			// If both attempts fail, the JSON is neither a valid single article nor a valid slice of articles
			handleError(w, "Invalid JSON payload", errSingle, http.StatusBadRequest)
			return
		}
		// If the single unmarshal succeeds, append it to the articles slice for consistent processing
		articles = append(articles, singleArticle)
	}

	// Validate and Redis Set arguments needed for Redis JSONMSet
	var articlesSetArgs []redis.JSONSetArgs
	for _, article := range articles {
		if validateErr := validate.Struct(article); validateErr != nil {
			handleError(w, fmt.Sprintf("Validation failed for article %+v", article), validateErr, http.StatusBadRequest)
			return
		}
		key := fmt.Sprintf("%s%d", keysPrefix, article.Id)

		// Check if the article already exists in Redis
		exists, err := redisClient.Exists(ctx, key).Result()
		if err != nil {
			handleError(w, "Error checking if article exists", err, http.StatusInternalServerError)
			return
		}
		if exists != 0 {
			handleError(w, fmt.Sprintf("article with ID %d found in Database", article.Id), fmt.Errorf("duplicate Article Id"), http.StatusNotFound)
			return
		}

		// Note: For now JSONSetArgs does not seem to marshaled back JSON
		// Hence, we marshall this before setting as Argument
		articleByte, errMarshall := json.Marshal(article)
		if errMarshall != nil {
			handleError(w, fmt.Sprintf("Creating article with ID %d in the Database failed. No Article Added", article.Id), errMarshall, http.StatusInternalServerError)
			return
		}
		articlesSetArgs = append(articlesSetArgs, redis.JSONSetArgs{
			Key:   key,
			Path:  "$",
			Value: articleByte,
		})
	}

	// Seth the result in Database, using JSONMSet
	result, err := redisClient.JSONMSetArgs(ctx, articlesSetArgs).Result()
	if err != nil {
		handleError(w, "creating articles in the Database failed", err, http.StatusInternalServerError)
		return
	}
	responseJSON(w, result, http.StatusOK)
}

func updateArticleByID(w http.ResponseWriter, r *http.Request) {

	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		handleError(w, "Invalid article ID", err, http.StatusBadRequest)
		return
	}

	// Decode the JSON payload directly from the request body
	var article Article
	if err := json.NewDecoder(r.Body).Decode(&article); err != nil {
		handleError(w, "Invalid JSON payload", err, http.StatusBadRequest)
		return
	}

	// Ensure the ID in the path matches the ID in the payload if present
	if article.Id != 0 && uint(id) != article.Id {
		handleError(w, "Mismatch between URL ID and payload ID", fmt.Errorf("URL ID: %d, Payload ID: %d", id, article.Id), http.StatusBadRequest)
		return
	}
	article.Id = uint(id) // Set the ID from the URL to ensure consistency

	// Validate the article struct
	if err := validate.Struct(article); err != nil {
		handleError(w, "Validation failed for article", err, http.StatusBadRequest)
		return
	}

	// Check if the article exists in Redis
	key := fmt.Sprintf("%s%d", keysPrefix, id)
	exists, err := redisClient.Exists(ctx, key).Result()
	if err != nil {
		handleError(w, "Error checking if article exists", err, http.StatusInternalServerError)
		return
	}
	if exists == 0 {
		handleError(w, "Article not found", fmt.Errorf("no article found with ID %d", id), http.StatusNotFound)
		return
	}

	// Update the article in Redis
	if _, err = redisClient.JSONSet(ctx, key, "$", article).Result(); err != nil {
		handleError(w, "Failed to update article in Redis", err, http.StatusInternalServerError)
		return
	}

	// Respond with the updated article
	responseJSON(w, article, http.StatusOK)
}

func deleteArticleByID(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		handleError(w, "Invalid article ID", err, http.StatusBadRequest)
		return
	}

	// Construct the Redis key for the article
	key := fmt.Sprintf("%s%d", keysPrefix, id)

	// Check if the article exists before attempting to delete
	exists, err := redisClient.Exists(ctx, key).Result()
	if err != nil {
		handleError(w, "Error checking if article exists", err, http.StatusInternalServerError)
		return
	}
	if exists == 0 {
		handleError(w, "Article not found", fmt.Errorf("no article found with ID %d", id), http.StatusNotFound)
		return
	}

	// Delete the article from Redis
	if _, err := redisClient.Del(ctx, key).Result(); err != nil {
		handleError(w, "Failed to delete article from Redis", err, http.StatusInternalServerError)
		return
	}

	// Respond to indicate successful deletion
	responseJSON(w, CustomOutput{Message: fmt.Sprintf("Article with ID %d successfully deleted", id)}, http.StatusOK)
}

func searchArticles(w http.ResponseWriter, r *http.Request) {

	// Using reflect here to Get the list of expected parameters from Article Type
	// Using the JSON Tag
	articleParams := Article{}
	t := reflect.TypeOf(articleParams)
	var expectedParams []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		expectedParams = append(expectedParams, tag)
	}

	// Check that the provided parameters are in expected Parameters
	providedParams := r.URL.Query()
	if len(providedParams) == 0 {
		handleError(w,
			fmt.Sprintf("You must provide at least one of the following parameter: %v", expectedParams),
			fmt.Errorf("invalid search parameter"), http.StatusBadRequest,
		)
		return
	}
	for param, _ := range providedParams {
		if !slices.Contains(expectedParams, param) {
			handleError(w,
				fmt.Sprintf("%s query provided is not one of the following parameter: %v", param, expectedParams),
				fmt.Errorf("invalid search parameter"), http.StatusBadRequest,
			)
			return
		}
	}

	// Build the Search Query
	var queries []any
	queries = append(queries, "FT.SEARCH", searchIndexName)
	for param, fieldToSearch := range providedParams {
		args := []any{fmt.Sprintf("@%s:%s", param, fieldToSearch[0])}
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
	fmt.Printf("%v\n\n", redisFtResult)
	if err != nil {
		handleError(w, "An Error Occurred while trying to Get Result for this search", err, http.StatusBadRequest)
		return
	}

	// Generic DB Error
	genericDbErrorMsg := fmt.Sprintf("Database Returned an unexpected format type when Searching with parameter: %s", providedParams.Encode())

	// Gather Top level map
	topLevel, ok := redisFtResult.(map[interface{}]interface{})
	if !ok {
		handleError(w, genericDbErrorMsg, fmt.Errorf("response is not a valid map"), http.StatusInternalServerError)
		return
	}

	// Check TotalResult
	totalResults, ok := topLevel["total_results"].(int64)
	if !ok {
		handleError(w, genericDbErrorMsg, fmt.Errorf("total result not a valid digit"), http.StatusInternalServerError)
		return
	}

	if totalResults <= 0 {
		responseJSON(w, CustomOutput{Message: "no article found with the search criteria"}, http.StatusNotFound)
		return
	}

	// Retrieve Result Array
	resultsArray, ok := topLevel["results"].([]interface{})
	if !ok {
		handleError(w, genericDbErrorMsg, fmt.Errorf("results not a valid List of Articles"), http.StatusInternalServerError)
		return
	}

	// Each item in ResultsArray should be (map[interface{}]interface{}) that has keys id and extra_attributes
	// With the id being Redis Key and extra_attributes being another (map[interface{}]interface{})
	// that contains key->path(e.g. $) and value->Article , so we should be able to marshall/unmarshall
	// That object back to an Article
	var resArticles []Article
	for _, eachResult := range resultsArray {
		res, ok := eachResult.(map[interface{}]interface{})
		if !ok {
			handleError(w, genericDbErrorMsg, fmt.Errorf("database Search result at first level is in invalid format"), http.StatusInternalServerError)
			return
		}
		resAttributes, ok := res["extra_attributes"].(map[interface{}]interface{})
		if !ok {
			handleError(w, genericDbErrorMsg, fmt.Errorf("database Search result at second level is in invalid format"), http.StatusInternalServerError)
			return
		}

		for _, resultArticle := range resAttributes {
			if jsonString, ok := resultArticle.(string); ok {
				var newArticles []Article // Use a slice to handle multiple articles
				err = json.Unmarshal([]byte(jsonString), &newArticles)
				if err != nil {
					handleError(w, "Failed to unmarshal articles", err, http.StatusInternalServerError)
					return
				}
				resArticles = append(resArticles, newArticles...)
			}
		}

	}

	responseJSON(w, resArticles, http.StatusOK)
}
