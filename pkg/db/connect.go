package db

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
)

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
