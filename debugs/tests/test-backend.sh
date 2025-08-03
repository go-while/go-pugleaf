#!/bin/bash

# Quick test to verify the go-pugleaf backend is accessible
echo "Testing go-pugleaf backend connectivity..."

BACKEND_URL="https://reader-nyc.newsdeef.eu:11980/api/v1/groups"

echo "Testing URL: $BACKEND_URL"

# Test 1: Basic connectivity
echo ""
echo "=== Test 1: Basic HTTP connectivity ==="
curl -v -k --connect-timeout 10 --max-time 30 "$BACKEND_URL" 2>&1 | head -20

echo ""
echo "=== Test 2: JSON response test ==="
response=$(curl -s -k "$BACKEND_URL" 2>/dev/null)
if [ $? -eq 0 ]; then
    echo "Response received (first 200 chars):"
    echo "$response" | head -c 200
    echo ""

    # Try to validate JSON
    if echo "$response" | jq . >/dev/null 2>&1; then
        echo "✓ Valid JSON response"
        echo "Response structure:"
        echo "$response" | jq 'keys' 2>/dev/null || echo "Could not parse JSON keys"
    else
        echo "⚠ Response is not valid JSON"
    fi
else
    echo "✗ Failed to get response"
fi

echo ""
echo "=== Test 3: Local backend test (if running locally) ==="
LOCAL_URL="https://localhost:11980/api/v1/groups"
echo "Testing local URL: $LOCAL_URL"
curl -s -k --connect-timeout 5 "$LOCAL_URL" >/dev/null 2>&1
if [ $? -eq 0 ]; then
    echo "✓ Local backend is accessible"
else
    echo "⚠ Local backend not accessible (expected if running remotely)"
fi

echo ""
echo "=== Environment Check ==="
echo "PUGLEAF_WEB_PORT environment variable: ${PUGLEAF_WEB_PORT:-'not set'}"

echo ""
echo "Test completed."
