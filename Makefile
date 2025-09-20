.PHONY: build run docker-build docker-run clean test-listener

# Build the main monitor application
build:
	go build -o bin/bloop-monitor ./cmd/monitor

# Build the test listener
build-listener:
	go build -o bin/test-listener ./cmd/test-listener

# Build both applications
build-all: build build-listener

# Run the monitor (requires Redis and RabbitMQ to be running)
run: build
	./bin/bloop-monitor

# Run the test listener
test-listener: build-listener
	./bin/test-listener

# Start infrastructure services
infra-up:
	docker-compose up -d redis rabbitmq

# Stop infrastructure services
infra-down:
	docker-compose down

# Start all services including the monitor
up: infra-up
	sleep 5  # Wait for services to be ready
	$(MAKE) run

# Build Docker image
docker-build:
	docker build -t bloop-monitor .

# Run with Docker Compose
docker-run:
	docker-compose up --build

# Clean build artifacts
clean:
	rm -rf bin/
	docker-compose down -v

# Run tests
test:
	go test ./...

# Development workflow: start infra, run monitor, and test listener in separate terminals
dev:
	@echo "🚀 Development setup:"
	@echo "1. Start infrastructure: make infra-up"
	@echo "2. Run monitor: make run"
	@echo "3. Run test listener: make test-listener"
	@echo "4. Add a wallet to watch and trigger some transactions!"

# Quick demo setup
demo: infra-up
	@echo "⏳ Waiting for services to start..."
	@sleep 10
	@echo "🎯 Starting monitor in background..."
	@./bin/bloop-monitor &
	@echo "🎧 Starting test listener..."
	@./bin/test-listener