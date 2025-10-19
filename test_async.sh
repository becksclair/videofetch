#!/bin/bash

# Test script for async metadata fetching behavior

set -e

SERVER_URL="http://localhost:8080"
TEST_URL="https://www.youtube.com/watch?v=dQw4w9WgXcQ"

echo "Testing VideoFetch Async Pattern"
echo "================================="
echo

echo "1. Testing /api/download_single endpoint..."
response=$(curl -s -X POST "$SERVER_URL/api/download_single" \
  -H "Content-Type: application/json" \
  -d "{\"url\":\"$TEST_URL\"}")
echo "Response: $response"
echo

# Extract db_id if present
db_id=$(echo "$response" | grep -o '"db_id":[0-9]*' | cut -d: -f2)
if [ -n "$db_id" ]; then
    echo "DB ID created: $db_id"
    echo

    echo "2. Waiting for async metadata fetch (5 seconds)..."
    sleep 5

    echo "3. Checking download history..."
    history=$(curl -s "$SERVER_URL/api/downloads")
    echo "History response (first 500 chars): ${history:0:500}"
    echo

    # Check if metadata was fetched (title should not be the URL anymore)
    if echo "$history" | grep -q "Rick Astley"; then
        echo "✓ Metadata successfully fetched asynchronously!"
    else
        echo "⚠ Metadata not yet available or fetch failed"
    fi
else
    echo "No DB ID in response (might be running without store)"
fi

echo
echo "4. Testing /api/status endpoint..."
status=$(curl -s "$SERVER_URL/api/status")
echo "Status response: $status"

echo
echo "Test complete!"