package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-playground/validator/v10"
	"github.com/redis/go-redis/v9"
	"github.com/stivesso/articles-search/pkg/cache"
	"io"
	"log"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strconv"
)

// Article represents the structure of an Article
type Article struct {
	Id      uint     `json:"id" validate:"required"`
	Title   string   `json:"title" validate:"required"`
	Content string   `json:"content" validate:"omitempty"`
	Author  string   `json:"author" validate:"omitempty"`
	Tags    []string `json:"tags" validate:"omitempty"`
}

type CustomOutput struct {
	Error   string `json:"Error,omitempty"`
	Message string `json:"Message,omitempty"`
}

var redisClient *redis.Client // Global Redis Client Variable
var indentJson = "  "
var ctx = context.Background()
var searchIndexName = "idx_articles"
var validate = validator.New()

func main() {

	var err error
	// Connect to Redis
	slog.Info("Connecting to Redis")
	redisClient, err = cache.NewRedisClient("192.168.64.7", 30183, "", 0)
	if err != nil {
		panic(err)
	}

	// Defer Closing Redis Client
	defer func() {
		slog.Info("Closing redisClient")
		err := redisClient.Close()
		if err != nil {
			slog.Error("Unable to Close Redis", "Error", err)
		}
	}()

	// Setup http serveMux
	mux := http.NewServeMux()

	// Routes
	mux.HandleFunc("GET /articles", getAllArticles)
	mux.HandleFunc("GET /articles/{id}", getArticleByID)
	mux.HandleFunc("POST /article", createArticle)
	mux.HandleFunc("PUT /article/{id}", updateArticleByID)
	mux.HandleFunc("DELETE /article/{id}", deleteArticleByID)
	mux.HandleFunc("GET /articles/search", searchArticles)

	// Start the server
	serverAddress := ":8080"
	slog.Info(fmt.Sprintf("Starting HTTP Server on Address %s", serverAddress))
	log.Fatal(http.ListenAndServe(serverAddress, mux))
}

// Give me an interface 'v' that can be render as JSON and a statusCode
// and I will writes it as a response into the http Response Writer w
func responseJSON(w http.ResponseWriter, v interface{}, statusCode int) {
	js, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(statusCode)
	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
}

func getAllArticles(w http.ResponseWriter, r *http.Request) {
	// First retrieve all Article keys using SCAN
	var articleKeys []string
	iter := redisClient.Scan(ctx, 0, "articleKey:*", 0).Iterator()
	for iter.Next(ctx) {
		articleKeys = append(articleKeys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		panic(err)
	}
	if len(articleKeys) == 0 {
		customOutput := CustomOutput{
			Message: "No Item found",
		}
		responseJSON(w, customOutput, http.StatusNotFound)
		return
	}

	// Build the article List, using MGET here to get all Keys, result is an array of JSONGet response
	// JSONGet responses are themselves array containing Response
	retrievedArticleArray, err := redisClient.JSONMGet(ctx, "$", articleKeys...).Result()
	if err != nil && err != redis.Nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to get All Articles",
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
		return
	}
	if err == redis.Nil {
		customOutput := CustomOutput{
			Message: "No Item found",
		}
		responseJSON(w, customOutput, http.StatusNotFound)
		return
	}

	// Loop on each element in the array and append its first element to the result after validation
	var result []Article
	for _, responseRetrievedArticle := range retrievedArticleArray {
		var resultForThisArticle []Article
		responseArticle, isString := responseRetrievedArticle.(string)
		if !isString {
			customOutput := CustomOutput{
				Message: "An Error Occurred while trying to get All Articles, Article returned were not in the correct format",
			}
			responseJSON(w, customOutput, http.StatusInternalServerError)
			return
		}
		err = json.Unmarshal([]byte(responseArticle), &resultForThisArticle)
		if err != nil {
			customOutput := CustomOutput{
				Message: "An Error Occurred while trying to validate the structure of the returned Article",
				Error:   err.Error(),
			}
			responseJSON(w, customOutput, http.StatusInternalServerError)
			return
		}
		result = append(result, resultForThisArticle[0])
	}

	responseJSON(w, result, http.StatusOK)
}

func getArticleByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	articleWithThisKey, err := redisClient.JSONGet(ctx, fmt.Sprintf("articleKey:%s", id)).Result()
	if err == redis.Nil {
		customOutput := CustomOutput{
			Message: "No Item found",
		}
		responseJSON(w, customOutput, http.StatusNotFound)
		return
	}
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("An Error Occurred while trying to get Article with key %s", id),
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
	}
	if articleWithThisKey == "" {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("No Article with ID %s found", id),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
		return
	}

	// Let's deserialize this back, that also validate the structure and rids of the extra backslashes
	var articleToReturn Article
	err = json.Unmarshal([]byte(articleWithThisKey), &articleToReturn)
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to validate the structure of the returned Article",
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
		return
	}
	responseJSON(w, articleToReturn, http.StatusOK)
}

func createArticle(w http.ResponseWriter, r *http.Request) {
	/*
		Using Same function to process either  a single article or a list of articles
		hence not using echo.Context Bind() function, as the The c.Bind() method in Echo
		reads the request body only once, subsequent attempts to read it will result in an EOF error
	*/

	// Read the request body into bytes
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("Failed to read request body"),
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
		return
	}

	// Try to decode the request body into a slice of Article
	var articles []Article
	err = json.Unmarshal(bodyBytes, &articles)
	if err != nil {
		// Let us check if this is a single Item
		var article Article
		err := json.Unmarshal(bodyBytes, &article)
		if err != nil {
			customOutput := CustomOutput{
				Message: "The input provided is neither a list of articles nor a valid article. Please input either a list of articles of a single article",
				Error:   err.Error(),
			}
			responseJSON(w, customOutput, http.StatusBadRequest)
			return
		}
		articles = []Article{article}
	}

	// Run Validation Checks on the Article
	var articlesSetArgs []redis.JSONSetArgs
	for _, article := range articles {
		// Validate the Article Structure (must have at least ID and Title)
		err = validate.Struct(article)
		if err != nil {
			customOutput := CustomOutput{
				Message: fmt.Sprintf("The item %+v does not seem to have all the required fields, it must have at least an ID greater than zero and a title", article),
				Error:   err.Error(),
			}
			responseJSON(w, customOutput, http.StatusBadRequest)
			return
		}
		// Check if article ID don't already exist
		articleKey := fmt.Sprintf("articleKey:%d", article.Id)
		articlesWithThisKey, err := redisClient.JSONGet(ctx, articleKey).Result()
		if err != nil {
			customOutput := CustomOutput{
				Message: "An Error Occurred while trying to Check if this article already exists.",
				Error:   err.Error(),
			}
			responseJSON(w, customOutput, http.StatusInternalServerError)
			return
		}
		if articlesWithThisKey != "" {
			customOutput := CustomOutput{
				Message: fmt.Sprintf("An article with key %d already exist", article.Id),
			}
			responseJSON(w, customOutput, http.StatusBadRequest)
			return
		}
		// For now JSONSetArgs does not seem to marshaled back JSON
		// Hence, we marshall this before setting as Argument
		articleByte, err := json.Marshal(article)
		if err != nil {
			customOutput := CustomOutput{
				Message: fmt.Sprintf("An Error Occurred while trying to Set article with ID %d in the Database. No Article Added", article.Id),
				Error:   err.Error(),
			}
			responseJSON(w, customOutput, http.StatusInternalServerError)
			return
		}
		articlesSetArgs = append(articlesSetArgs, redis.JSONSetArgs{
			Key:   articleKey,
			Path:  "$",
			Value: articleByte,
		})
	}

	// Seth the result in Database, using JSONMSet
	result, err := redisClient.JSONMSetArgs(ctx, articlesSetArgs).Result()
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to Set articles in the Database.",
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
		return
	}
	responseJSON(w, result, http.StatusOK)
}

func updateArticleByID(w http.ResponseWriter, r *http.Request) {

	// Read the request body into bytes
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("Failed to read request body"),
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
		return
	}

	id := r.PathValue("id")

	// Try to decode the request body into an Article
	var article Article
	err = json.Unmarshal(bodyBytes, &article)
	if err != nil {
		customOutput := CustomOutput{
			Message: "The input provided is not a valid article. Please input either a valid article",
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusBadRequest)
		return
	}

	// Make sure the ID is set on the updated Article
	intId, err := strconv.Atoi(id)
	if err != nil || intId <= 0 {
		customOutput := CustomOutput{
			Message: "An Error Occurred while checking if the provided id is a number that is greater than zero, please make sure to provide a valid ID",
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusBadRequest)
		return
	}
	article.Id = uint(intId)

	// Check if article ID exist
	articleKey := fmt.Sprintf("articleKey:%s", id)
	_, err = redisClient.JSONGet(ctx, articleKey).Result()
	if err == redis.Nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("No article with key %d exists", article.Id),
		}
		responseJSON(w, customOutput, http.StatusBadRequest)
		return
	}
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to Check if this article already exists.",
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
		return
	}

	// Validate the provided article
	validate := validator.New()
	err = validate.Struct(article)
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("The item %+v does not seem to have all the required fields, it must have at least an ID greater than zero and a title", article),
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusBadRequest)
		return
	}

	// Update the article
	_, err = redisClient.JSONSet(ctx, articleKey, "$", article).Result()
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("An Error Occured while trying to Update Article with id %d", article.Id),
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
		return
	}
	customOutput := CustomOutput{
		Message: fmt.Sprintf("Article with id %d succesfully updated", article.Id),
	}
	responseJSON(w, customOutput, http.StatusOK)
}

func deleteArticleByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := redisClient.Del(ctx, fmt.Sprintf("articleKey:%s", id)).Result()
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("An Error Occured while trying to Delete Articles with id %s", id),
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
		return
	}
	customOutput := CustomOutput{
		Message: fmt.Sprintf("Article with id %s succesfully deleted", id),
	}
	responseJSON(w, customOutput, http.StatusOK)
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
		customOutput := CustomOutput{
			Message: fmt.Sprintf("You must provide at least one of the following parameter: %v", expectedParams),
		}
		responseJSON(w, customOutput, http.StatusBadRequest)
		return
	}
	for param, _ := range providedParams {
		if !slices.Contains(expectedParams, param) {
			customOutput := CustomOutput{
				Message: fmt.Sprintf("%s query provided is not one of the following parameter: %v. Please provide a valid query", param, expectedParams),
			}
			responseJSON(w, customOutput, http.StatusBadRequest)
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

	/*
		Run query FT.SEARCH
		https://redis.io/commands/ft.search/
		FT.SEARCH returns an array reply, where the first element is an integer reply
		of the total number of results, and then array reply pairs of document ids,
		and array replies of attribute/value pairs
			In other words, it returns [totalItems, keys, [path, Articles], keys, [path, Articles], keys, [path, Articles]...]
	*/
	result, err := redisClient.Do(ctx, queries...).Result()
	fmt.Printf("%v\n\n", result)
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occured while trying to Get Result for this search",
			Error:   err.Error(),
		}
		responseJSON(w, customOutput, http.StatusInternalServerError)
		return
	}

	// Generic Error
	customOutputUnknownFormat := CustomOutput{
		Message: fmt.Sprintf("Database Returned an unexpected format type when Searching with parameter %v", providedParams),
	}

	// Process Results
	replies, ok := result.(map[interface{}]interface{})
	if !ok || len(replies) < 1 {
		responseJSON(w, customOutputUnknownFormat, http.StatusInternalServerError)
		return
	}

	totalResults, ok := replies["total_results"].(int64)
	fmt.Printf("%v", replies)
	if !ok {
		responseJSON(w, customOutputUnknownFormat, http.StatusInternalServerError)
		return
	}

	if totalResults <= 0 {
		customOutput := CustomOutput{
			Message: "Search completed, but no article found with the search criteria",
		}
		responseJSON(w, customOutput, http.StatusOK)
		return
	}

	// Number of elements in the replies must be 3 times the totalResults
	if len(replies) != int(totalResults*3) {
		responseJSON(w, customOutputUnknownFormat, http.StatusInternalServerError)
		return
	}

	// Let's put all keys and articles tuple in a slice
	/*
		allKeysAndArticles := replies[1:]
		var arrayTupleKeysArticles [][]any
		for i := 0; i < len(allKeysAndArticles); i += 2 {
			arrayTupleKeysArticles = append(arrayTupleKeysArticles, allKeysAndArticles[i:i+2])
		}*/

	responseJSON(w, nil, http.StatusOK)
}
