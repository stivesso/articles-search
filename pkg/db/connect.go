// Package db provides primitives for interacting with Redis Database
package db

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
)

// DbClient represents a client for connecting to a Redis database. It is a pointer to a redis.Client.
type DbClient *redis.Client

// NewDbClient creates a new DbClient instance for connecting to a Redis database.
func NewDbClient(dbHost string, dbPort int, dbPassword string, dbRedis int) (DbClient, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", dbHost, dbPort),
		Password: dbPassword,
		DB:       dbRedis,
	})
	// Ping the redis server to check connection
	_, err := client.Ping(context.Background()).Result()
	return client, err
}
