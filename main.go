package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/redis/go-redis/v9"
	"github.com/stivesso/articles-search/pkg/cache"
	"io"
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

	// Setup Echo Web Framework
	e := echo.New()

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	// Routes
	e.GET("/articles", getAllArticles)
	e.GET("/article/:id", getArticleByID)
	e.POST("/article", createArticle)
	e.PUT("/article/:id", updateArticleByID)
	e.DELETE("/article/:id", deleteArticleByID)
	e.GET("/articles/search", searchArticles)

	// Start the server
	err = e.Start(":8080")
	if err != nil {
		panic(err)
	}
}

func getAllArticles(c echo.Context) error {
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
		return c.JSON(http.StatusNotFound, customOutput)
	}

	// Build the article List, using MGET here to get all Keys, result is an array of JSONGet response
	// JSONGet responses are themselves array containing Response
	retrievedArticleArray, err := redisClient.JSONMGet(ctx, "$", articleKeys...).Result()
	if err != nil && err != redis.Nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to get All Articles",
			Error:   err.Error(),
		}
		return c.JSON(http.StatusInternalServerError, customOutput)
	}
	if err == redis.Nil {
		customOutput := CustomOutput{
			Message: "No Item found",
		}
		return c.JSON(http.StatusNotFound, customOutput)
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
			return c.JSON(http.StatusInternalServerError, customOutput)
		}
		err = json.Unmarshal([]byte(responseArticle), &resultForThisArticle)
		if err != nil {
			customOutput := CustomOutput{
				Message: "An Error Occurred while trying to validate the structure of the returned Article",
				Error:   err.Error(),
			}
			return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
		}
		result = append(result, resultForThisArticle[0])
	}
	return c.JSONPretty(http.StatusOK, result, indentJson)
}

func getArticleByID(c echo.Context) error {
	id := c.Param("id")
	articleWithThisKey, err := redisClient.JSONGet(ctx, fmt.Sprintf("articleKey:%s", id)).Result()
	if err == redis.Nil {
		customOutput := CustomOutput{
			Message: "No Item found",
		}
		return c.JSON(http.StatusNotFound, customOutput)
	}
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("An Error Occurred while trying to get Article with key %s", id),
			Error:   err.Error(),
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	if articleWithThisKey == "" {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("No Article with ID %s found", id),
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	// Let's deserialize this back, to validate the structure (which also rids of the extra backslashes)
	var articleToReturn Article
	err = json.Unmarshal([]byte(articleWithThisKey), &articleToReturn)
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to validate the structure of the returned Article",
			Error:   err.Error(),
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	return c.JSONPretty(http.StatusOK, articleToReturn, indentJson)
}

func createArticle(c echo.Context) error {
	/*
		Using Same function to process either  a single article or a list of articles
		hence not using echo.Context Bind() function, as the The c.Bind() method in Echo
		reads the request body only once, subsequent attempts to read it will result in an EOF error
	*/

	// Read the request body into bytes
	bodyBytes, err := io.ReadAll(c.Request().Body)
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("Failed to read request body"),
			Error:   err.Error(),
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
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
			return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
		}
		articles = []Article{article}
	}

	// Run Validation Checks on the Article
	validate := validator.New()
	var articlesSetArgs []redis.JSONSetArgs
	for _, article := range articles {
		// Validate the Article Structure (must have at least ID and Title)
		err = validate.Struct(article)
		if err != nil {
			customOutput := CustomOutput{
				Message: fmt.Sprintf("The item %+v does not seem to have all the required fields, it must have at least an ID greater than zero and a title", article),
				Error:   err.Error(),
			}
			return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
		}
		// Check if article ID don't already exist
		articleKey := fmt.Sprintf("articleKey:%d", article.Id)
		articlesWithThisKey, err := redisClient.JSONGet(ctx, articleKey).Result()
		if err != nil {
			customOutput := CustomOutput{
				Message: "An Error Occurred while trying to Check if this article already exists.",
				Error:   err.Error(),
			}
			return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
		}
		if articlesWithThisKey != "" {
			customOutput := CustomOutput{
				Message: fmt.Sprintf("An article with key %d already exist", article.Id),
			}
			return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
		}
		// For now JSONSetArgs does not seem to marshaled back JSON
		// Hence, we marshall this before setting as Argument
		articleByte, err := json.Marshal(article)
		if err != nil {
			customOutput := CustomOutput{
				Message: fmt.Sprintf("An Error Occurred while trying to Set article with ID %d in the Database. No Article Added", article.Id),
				Error:   err.Error(),
			}
			return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
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
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	return c.JSONPretty(http.StatusOK, result, indentJson)
}

func updateArticleByID(c echo.Context) error {
	var article Article
	err := c.Bind(&article)
	id := c.Param("id")
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to validate the structure of article",
			Error:   err.Error(),
		}
		return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
	}
	// Make sure the ID is set on the updated Article
	intId, err := strconv.Atoi(id)
	if err != nil || intId <= 0 {
		customOutput := CustomOutput{
			Message: "An Error Occurred while checking if the provided id is a number that is greater than zero, please make sure to provide a valid ID",
			Error:   err.Error(),
		}
		return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
	}
	article.Id = uint(intId)

	// Check if article ID exist
	articleKey := fmt.Sprintf("articleKey:%s", id)
	_, err = redisClient.JSONGet(ctx, articleKey).Result()
	if err == redis.Nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("No article with key %d exists", article.Id),
		}
		return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
	}
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to Check if this article already exists.",
			Error:   err.Error(),
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}

	// Validate the provided article
	validate := validator.New()
	err = validate.Struct(article)
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("The item %+v does not seem to have all the required fields, it must have at least an ID greater than zero and a title", article),
			Error:   err.Error(),
		}
		return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
	}

	// Update the article
	_, err = redisClient.JSONSet(ctx, articleKey, "$", article).Result()
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("An Error Occured while trying to Update Article with id %d", article.Id),
			Error:   err.Error(),
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	customOutput := CustomOutput{
		Message: fmt.Sprintf("Article with id %d succesfully updated", article.Id),
	}
	return c.JSONPretty(http.StatusOK, customOutput, indentJson)
}

func deleteArticleByID(c echo.Context) error {
	id := c.Param("id")
	_, err := redisClient.Del(ctx, fmt.Sprintf("articleKey:%s", id)).Result()
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("An Error Occured while trying to Delete Articles with id %s", id),
			Error:   err.Error(),
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	customOutput := CustomOutput{
		Message: fmt.Sprintf("Article with id %s succesfully deleted", id),
	}
	return c.JSONPretty(http.StatusOK, customOutput, indentJson)
}

func searchArticles(c echo.Context) error {

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
	providedParams := c.QueryParams()
	if len(providedParams) == 0 {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("You must provide at least one of the following parameter: %v", expectedParams),
		}
		return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
	}
	for param, _ := range providedParams {
		if !slices.Contains(expectedParams, param) {
			customOutput := CustomOutput{
				Message: fmt.Sprintf("%s query provided is not one of the following parameter: %v. Please provide a valid query", param, expectedParams),
			}
			return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
		}
	}

	// Build the Search Query
	var queries []interface{}
	for param, fieldToSearrch := range providedParams {
		queries = append(queries, []string{searchIndexName, param, fieldToSearrch[0]})
	}
	result, err := redisClient.Do(ctx, queries...).Result()
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occured while trying to Get Result for this search",
			Error:   err.Error(),
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	return c.JSONPretty(http.StatusOK, result, indentJson)
}
