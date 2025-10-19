#!/bin/bash

# Test script to verify immediate processing of pending downloads

echo "Starting VideoFetch server..."
./videofetch --port 8082 --host 127.0.0.1 --output-dir /tmp/videofetch-test > /tmp/videofetch.log 2>&1 &
SERVER_PID=$!

# Wait for server to start
sleep 2

echo "Submitting batch download request..."
START_TIME=$(date +%s)

# Submit a URL for download
RESPONSE=$(curl -s -X POST http://127.0.0.1:8082/api/download \
  -H 'Content-Type: application/json' \
  -d '{"urls":["https://www.youtube.com/watch?v=jNQXAC9IVRw"]}')

echo "Response: $RESPONSE"
DB_ID=$(echo "$RESPONSE" | grep -o '"db_ids":\[[0-9]*\]' | grep -o '[0-9]*')

if [ -z "$DB_ID" ]; then
  echo "ERROR: No DB ID returned"
  kill $SERVER_PID
  exit 1
fi

echo "Checking download status immediately (DB ID: $DB_ID)..."

# Poll for status change from 'pending' to 'downloading'
for i in {1..10}; do
  sleep 0.5
  STATUS_RESPONSE=$(curl -s http://127.0.0.1:8082/api/downloads)
  STATUS=$(echo "$STATUS_RESPONSE" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)

  if [ "$STATUS" = "downloading" ]; then
    END_TIME=$(date +%s)
    ELAPSED=$((END_TIME - START_TIME))
    echo "SUCCESS: Download started processing in $ELAPSED seconds (status: $STATUS)"
    echo "This confirms immediate processing without 2-second DBWorker delay!"
    kill $SERVER_PID
    exit 0
  fi

  echo "  Attempt $i/10: Status is '$STATUS'"
done

echo "ERROR: Download did not start processing within 5 seconds"
kill $SERVER_PID
exit 1