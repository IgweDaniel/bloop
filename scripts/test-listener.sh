#!/bin/bash

# Test listener script for RabbitMQ deposit events
# This script builds and runs the test listener

set -e

echo "🔨 Building test listener..."
cd "$(dirname "$0")/.."

# Build the test listener
go build -o bin/test-listener ./cmd/test-listener

echo "🎧 Starting test listener..."
echo "📋 This will listen for deposit events from the wallet tracker"
echo "⏹️  Press Ctrl+C to stop"
echo ""

# Set RabbitMQ URL if not already set
export RABBITMQ_URL=${RABBITMQ_URL:-"amqp://bloop:bloop123@localhost:5673/"}

# Run the test listener
./bin/test-listener
