#!/bin/bash

# Execute the original entrypoint logic as background process
/entrypoint.sh &

# Timeout for waiting (in seconds)
timeout=60
start_time=$(date +%s)

# Wait for Redis server to be ready
until redis-cli ping &>/dev/null; do
    current_time=$(date +%s)
    elapsed_time=$((current_time - start_time))

    if [ $elapsed_time -ge $timeout ]; then
        echo "Timeout: Unable to connect to Redis server within $timeout seconds."
        exit 1
    fi

    echo "Waiting for Redis server..."
    sleep 1
done

# Add logic for creating Redisearch indexes
redis-cli FT.CREATE idx_articles ON JSON PREFIX 1 "article" SCHEMA $.id AS id TEXT $.title AS title TEXT $.content AS content TEXT $.author AS author TEXT $.tags AS tags TAG

# Wait for the background process to finish ,and returns its exit code
wait