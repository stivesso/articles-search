package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/redis/go-redis/v9"
	"github.com/stivesso/articles-search/pkg/cache"
	"log/slog"
	"net/http"
	"strconv"
)

// Article represents the structure of an Article
type Article struct {
	Id      uint     `json:"id"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Author  string   `json:"author"`
	Tags    []string `json:"tags"`
}

type CustomOutput struct {
	Error   error  `json:"Error,omitempty"`
	Message string `json:"Message,omitempty"`
}

var redisClient *redis.Client // Global Redis Client Variable
var indentJson = "  "
var ctx = context.Background()

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
	//e.GET("/articles/search", searchArticles)

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
			Error:   err,
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
				Error:   err,
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
			Error:   err,
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
	var articleToReturn []Article
	err = json.Unmarshal([]byte(articleWithThisKey), &articleToReturn)
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to validate the structure of the returned Article",
			Error:   err,
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	return c.JSONPretty(http.StatusOK, articleToReturn[0], indentJson)
}

func createArticle(c echo.Context) error {
	var article Article
	err := c.Bind(&article)
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to validate the structure of article",
			Error:   err,
		}
		return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
	}
	// Check if article ID is valid (higher than zero)
	if article.Id == 0 {
		customOutput := CustomOutput{
			Message: "The article to add must have at least an Id and that ID must be higher than zero",
		}
		return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
	}
	// Check if article ID don't already exist
	articleKey := fmt.Sprintf("articleKey:%d", article.Id)
	articlesWithThisKey, err := redisClient.JSONGet(ctx, articleKey).Result()
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to Check if this article already exists.",
			Error:   err,
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	if articlesWithThisKey != "" {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("An article with key %d already exist", article.Id),
		}
		return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
	}
	status, err := redisClient.JSONSet(ctx, articleKey, "$", article).Result()
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to Set the article in the Database.",
			Error:   err,
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	return c.JSONPretty(http.StatusOK, status, indentJson)
}

func updateArticleByID(c echo.Context) error {
	var article Article
	err := c.Bind(&article)
	id := c.Param("id")
	if err != nil {
		customOutput := CustomOutput{
			Message: "An Error Occurred while trying to validate the structure of article",
			Error:   err,
		}
		return c.JSONPretty(http.StatusBadRequest, customOutput, indentJson)
	}
	// Make sure the ID is set on the updated Article
	intId, err := strconv.Atoi(id)
	if err != nil || intId <= 0 {
		customOutput := CustomOutput{
			Message: "An Error Occurred while checking if the provided id is a number that is greater than zero, please make sure to provide a valid ID",
			Error:   err,
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
			Error:   err,
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	// Update the article
	_, err = redisClient.JSONSet(ctx, articleKey, "$", article).Result()
	if err != nil {
		customOutput := CustomOutput{
			Message: fmt.Sprintf("An Error Occured while trying to Update Article with id %d", article.Id),
			Error:   err,
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
			Error:   err,
		}
		return c.JSONPretty(http.StatusInternalServerError, customOutput, indentJson)
	}
	customOutput := CustomOutput{
		Message: fmt.Sprintf("Article with id %s succesfully deleted", id),
	}
	return c.JSONPretty(http.StatusOK, customOutput, indentJson)
}
