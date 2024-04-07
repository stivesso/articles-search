package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-playground/validator/v10"
	"github.com/stivesso/articles-search/pkg/db"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
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
	Error   string `json:"Error,omitempty"`
	Message string `json:"Message,omitempty"`
}

var (
	databaseClient  db.DbClient
	ctx             = context.Background()
	validate        = validator.New()
	searchIndexName = "idx_articles"
	keysPrefix      = "article:"
)

func main() {
	// Initialize Database client.
	err := initializeDatabase()
	if err != nil {
		log.Fatalf("Failed to connect to Database: %v", err)
	}

	// Setup HTTP server and routes.
	setupHTTPServer()
}

func initializeDatabase() error {
	var err error
	dbServer := os.Getenv("AS_DBSERVER")
	dbPort := os.Getenv("AS_DBPORT")
	if dbServer == "" || dbPort == "" {
		return errors.New("The following environment variables need to be set: \n AS_DBSERVER for the Database Server\n AS_DBPORT for the Database Port")
	}
	dbPortInt, err := strconv.Atoi(dbPort)
	if err != nil {
		return fmt.Errorf("unable to convert environment variable AS_DBPORT to a valid integer, the exact error was: %v", err)
	}
	databaseClient, err = db.NewDbClient(dbServer, dbPortInt, "", 0)
	return err
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

// isQueryParamsExpected checks if a list of query parameters are expected
func isQueryParamsExpected(queryParams url.Values, expectedParams []string) error {
	for param := range queryParams {
		if !slices.Contains(expectedParams, param) {
			return fmt.Errorf("%s query provided is not one of the following parameter: %v", param, expectedParams)
		}
	}
	return nil
}

// structFieldsJsonTags returns a list containing fields JSON tags of a struct
// If the provided parameter is not a struct, then the returned Slice will be nil
func structFieldsJsonTags(givenStruct any) []string {
	t := reflect.TypeOf(givenStruct)
	var listOfTags []string
	if t.Kind() == reflect.Struct {
		for i := 0; i < t.NumField(); i++ {
			tag := t.Field(i).Tag.Get("json")
			listOfTags = append(listOfTags, tag)
		}
	}
	return listOfTags
}

// buildSearchParams builds a list of db.SearchParams
// by matching json tags on the given Struct with the parameters provided
func buildSearchParams(providedParams url.Values, givenStruct any) []db.SearchParams {
	var searchParameters []db.SearchParams
	givenStructType := reflect.TypeOf(givenStruct)

	if givenStructType.Kind() == reflect.Struct {
		for param, fieldToSearch := range providedParams {
			// Check if the param is one of the JSON tags in the given struct
			var field reflect.StructField
			var found bool
			for i := 0; i < givenStructType.NumField(); i++ {
				if givenStructType.Field(i).Tag.Get("json") == param {
					field = givenStructType.Field(i)
					found = true
					break
				}
			}

			if !found {
				continue // Skip if the parameter doesn't correspond to a field in the Given struct
			}

			var newSearchParam db.SearchParams
			newSearchParam.Param = strings.ToLower(param)
			newSearchParam.Value = fieldToSearch

			// Determine the type of the field
			switch field.Type.Kind() {
			case reflect.Slice:
				newSearchParam.Type = db.ArrayType
			case reflect.String:
				newSearchParam.Type = db.StringType
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
				reflect.Float32, reflect.Float64:
				newSearchParam.Type = db.NumberType
			case reflect.Bool:
				newSearchParam.Type = db.BooleanType
			case reflect.Map:
				newSearchParam.Type = db.ObjectType
			// Will Add more cases as needed for other types
			// For now, only db.ArrayType really matter as that correlate with tags
			default:
				newSearchParam.Type = db.StringType
			}

			searchParameters = append(searchParameters, newSearchParam)
		}
	}

	return searchParameters
}

// getAutomatedId retrieves the next available ID for a new record.
// If there are existing keys, it returns the maximum ID + 1 as the next ID.
// If there are no existing keys, it returns 1 as the next ID.
func getAutomatedId() (uint, error) {
	existingKeys, err := db.GetAllKeys(ctx, databaseClient, keysPrefix)
	if err != nil {
		return 0, err
	}
	allIds := make([]uint, len(existingKeys))
	for i, key := range existingKeys {
		id, err := strconv.Atoi(strings.TrimPrefix(key, keysPrefix))
		if err != nil {
			return 0, err
		}
		allIds[i] = uint(id)
	}
	if len(allIds) > 0 {
		maxID := slices.Max(allIds)
		nextID := maxID + 1
		return nextID, nil
	}
	return 1, nil
}

/*
Handlers Functions
*/

func getAllArticles(w http.ResponseWriter, r *http.Request) {
	var articles []Article

	// Use Scan to efficiently iterate through keys with the specified keysPrefix.
	keys, err := db.GetAllKeys(ctx, databaseClient, keysPrefix)
	if err != nil {
		handleError(w, "Failed to retrieve article keys from Database", err, http.StatusInternalServerError)
		return
	}

	if len(keys) == 0 {
		// No articles found, return an empty list with HTTP 200 OK.
		responseJSON(w, []Article{}, http.StatusOK)
		return
	}

	// Retrieve article details for each key
	resultMget, err := db.JSONMGet(ctx, databaseClient, keys)
	if err != nil {
		handleError(w, "An Error Occurred while Getting Articles", err, http.StatusInternalServerError)
		return
	}

	if resultMget == nil {
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
	// Build the Database key using the article ID.
	key := fmt.Sprintf("%s%s", keysPrefix, id)

	// Retrieve the article from Database.
	result, err := db.JSONGet(ctx, databaseClient, key)
	if err != nil {
		// Handle unexpected Database errors.
		handleError(w, "Failed to retrieve article from Database", err, http.StatusInternalServerError)
		return
	}

	if result == "" {
		// Article not found, respond with HTTP 404 Not Found.
		handleError(w, fmt.Sprintf("No article found with ID %s", id), nil, http.StatusNotFound)
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

// createArticle processes a HTTP POST request to create a new article or a list of articles.
// The function reads the entire request body and tries to unmarshal it into a slice of articles.
// If unmarshalling fails, it tries again to unmarshal it into a single article.
// If both attempts fail, an error response is sent indicating that the JSON payload is invalid.
// The function then validates each article and generates a new ID if not provided.
// If the article ID already exists in the database, an error response is sent.
// The function sets the result in the database using JSONMSet and sends a successful response.
// If any error occurs during the process, the handleError function is called to handle the error and send an error response.
func createArticle(w http.ResponseWriter, r *http.Request) {
	var articlesSetArgs []db.JSONSetArgs
	var articles []Article

	decoder := json.NewDecoder(r.Body)

	// read the  first token that will help check if it's an array or a single object
	typeChecker, err := decoder.Token()
	if err != nil {
		handleError(w, "Error reading JSON", err, http.StatusBadRequest)
		return
	}

	switch typeChecker {
	case json.Delim('['): // It's an array
		for decoder.More() {
			var article Article
			// decode an array value
			err := decoder.Decode(&article)
			if err != nil {
				handleError(w, "Failed to decode request body", err, http.StatusBadRequest)
				return
			}
			articles = append(articles, article)
		}
	case json.Delim('{'): // It's a single object
		// TODO: This is not working, because rereading r.Body returns an EOF, r.Body can only be read once
		decoder = json.NewDecoder(r.Body) //reinitialize decoder since we consumed the first token
		var article Article
		// decode an array value
		err := decoder.Decode(&article)
		if err != nil {
			handleError(w, "Failed to decode request body", err, http.StatusBadRequest)
			return
		}
		articles = append(articles, article)
	default:
		handleError(w, "Invalid JSON format", errors.New("the Provided JSON is neither a list of articles nor an article"), http.StatusBadRequest)
	}

	// Validate and Database Set arguments needed for Database JSONMSet
	for _, article := range articles {
		if article.Id == 0 {
			newId, err := getAutomatedId()
			if err != nil {
				handleError(w, "Failed to generate automated ID", err, http.StatusInternalServerError)
				return
			}
			article.Id = newId
		}
		if validateErr := validate.Struct(article); validateErr != nil {
			handleError(w, fmt.Sprintf("Validation failed for article %+v", article), validateErr, http.StatusBadRequest)
			return
		}
		key := fmt.Sprintf("%s%d", keysPrefix, article.Id)

		// Check if the article already exists in Database
		exists, err := db.Exists(ctx, databaseClient, key)
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
		articlesSetArgs = append(articlesSetArgs, db.JSONSetArgs{
			Key:   key,
			Path:  "$",
			Value: articleByte,
		})
	}

	// Set the result in Database, using JSONMSet
	result, err := db.JSONMSetArgs(ctx, databaseClient, articlesSetArgs)
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

	// Check if the article exists in Database
	key := fmt.Sprintf("%s%d", keysPrefix, id)
	exists, err := db.Exists(ctx, databaseClient, key)
	if err != nil {
		handleError(w, "Error checking if article exists", err, http.StatusInternalServerError)
		return
	}
	if exists == 0 {
		handleError(w, "Article not found", fmt.Errorf("no article found with ID %d", id), http.StatusNotFound)
		return
	}

	// Update the article in Database
	if _, err = db.JSONSet(ctx, databaseClient, key, "$", article); err != nil {
		handleError(w, "Failed to update article in Database", err, http.StatusInternalServerError)
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

	// Construct the Database key for the article
	key := fmt.Sprintf("%s%d", keysPrefix, id)

	// Check if the article exists before attempting to delete
	exists, err := db.Exists(ctx, databaseClient, key)
	if err != nil {
		handleError(w, "Error checking if article exists", err, http.StatusInternalServerError)
		return
	}
	if exists == 0 {
		handleError(w, "Article not found", fmt.Errorf("no article found with ID %d", id), http.StatusNotFound)
		return
	}

	// Delete the article from Database
	if _, err := db.Del(ctx, databaseClient, key); err != nil {
		handleError(w, "Failed to delete article from Database", err, http.StatusInternalServerError)
		return
	}

	// Respond to indicate successful deletion
	responseJSON(w, CustomOutput{Message: fmt.Sprintf("article with ID %d successfully deleted", id)}, http.StatusOK)
}

func searchArticles(w http.ResponseWriter, r *http.Request) {

	// Getting Expected parameters from Article JSON Tags
	expectedParams := structFieldsJsonTags(Article{})

	providedParams := r.URL.Query()
	invalidSearchError := "invalid search parameter"

	if len(providedParams) == 0 {
		handleError(w,
			invalidSearchError,
			fmt.Errorf("you must provide at least one of the following parameter: %v", expectedParams), http.StatusBadRequest,
		)
		return
	}

	// Check that the provided parameters are in expected Parameters
	if err := isQueryParamsExpected(providedParams, expectedParams); err != nil {
		handleError(w, invalidSearchError, err, http.StatusBadRequest)
		return
	}

	// Database Search Parameter
	searchParameters := buildSearchParams(providedParams, Article{})

	// Run the Search Query
	resArticles, err := db.Search[Article](ctx, databaseClient, searchIndexName, searchParameters)
	if err != nil {
		genericDbErrorMsg := fmt.Sprintf("Database Error while searching with parameter: %s", providedParams.Encode())
		handleError(w, genericDbErrorMsg, err, http.StatusInternalServerError)
		return
	}

	responseJSON(w, resArticles, http.StatusOK)
}
