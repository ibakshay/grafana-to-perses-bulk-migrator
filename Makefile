# Grafana to Perses Migration Tool Makefile
# Go version: 1.24.2

BINARY_NAME := perses-migration
BINARY_PATH := bin/$(BINARY_NAME)
GO_FILES := $(shell find . -name '*.go' -type f)
MAIN_FILE := main.go

# Default Go flags
GOCMD := go
GOBUILD := $(GOCMD) build
GOCLEAN := $(GOCMD) clean
GOTEST := $(GOCMD) test
GOGET := $(GOCMD) get
GOMOD := $(GOCMD) mod
GOFMT := gofmt
GOVET := $(GOCMD) vet

# Build flags
BUILD_FLAGS := -ldflags="-s -w"
BUILD_DIR := bin

# Default target
.PHONY: all
all: build

# Build the binary
.PHONY: build
build: $(BINARY_PATH)

$(BINARY_PATH): $(GO_FILES) go.mod
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(BUILD_FLAGS) -o $(BINARY_PATH) $(MAIN_FILE)
	@echo "✓ Binary built: $(BINARY_PATH)"

# Install binary to system PATH
.PHONY: install
install: build
	@echo "Installing $(BINARY_NAME) to GOPATH/bin..."
	@mkdir -p $(shell go env GOPATH)/bin
	cp $(BINARY_PATH) $(shell go env GOPATH)/bin/$(BINARY_NAME)
	@echo "✓ $(BINARY_NAME) installed to $(shell go env GOPATH)/bin/$(BINARY_NAME)"



# Migrate with common flags
.PHONY: migrate
migrate: build
	@echo "Running migration with cleanup flags..."
	@if [ -z "$(INPUT_DIR)" ]; then \
		echo "Error: INPUT_DIR is required. Usage: make migrate INPUT_DIR=/path/to/dashboards"; \
		exit 1; \
	fi
	./$(BINARY_PATH) --input-dir=$(INPUT_DIR) --cleanup $(if $(OUTPUT_DIR),--output-dir=$(OUTPUT_DIR)) $(EXTRA_FLAGS)

# Migrate with recursive flag (processes subdirectories)
.PHONY: migrate-recursive
migrate-recursive: build
	@echo "Running migration with cleanup and recursive flags..."
	@if [ -z "$(INPUT_DIR)" ]; then \
		echo "Error: INPUT_DIR is required. Usage: make migrate-recursive INPUT_DIR=/path/to/dashboards"; \
		exit 1; \
	fi
	./$(BINARY_PATH) --input-dir=$(INPUT_DIR) --cleanup --recursive $(if $(OUTPUT_DIR),--output-dir=$(OUTPUT_DIR)) $(EXTRA_FLAGS)

# Cleanup targets
.PHONY: clean
clean:
	@echo "Cleaning built binaries..."
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)
	rm -rf bin/percli
	@echo "✓ Binaries cleaned"

.PHONY: clean-containers
clean-containers:
	@echo "Stopping and removing migration-related containers..."
	@docker ps -q --filter "name=grafana" | xargs -r docker rm -f || true
	@docker ps -q --filter "name=perses" | xargs -r docker rm -f || true
	@echo "✓ Containers cleaned"

.PHONY: clean-all
clean-all: clean clean-containers
	@echo "✓ Complete cleanup finished"

# Help target
.PHONY: help
help:
	@echo "Grafana to Perses Migration Tool - Available targets:"
	@echo ""
	@echo "Build targets:"
	@echo "  build           Build the migration binary"
	@echo "  install         Build and install binary to GOPATH/bin"
	@echo ""
	@echo "Migrate targets:"
	@echo "  migrate         Run with cleanup flags (requires INPUT_DIR=/path)"
	@echo "  migrate-recursive Run with cleanup and recursive flags (requires INPUT_DIR=/path)"
	@echo ""
	@echo "Cleanup:"
	@echo "  clean           Remove built binaries"
	@echo "  clean-containers Stop and remove Docker containers"
	@echo ""
	@echo "Examples:"
	@echo "  make migrate INPUT_DIR=/Users/user/dashboards OUTPUT_DIR=/Users/user/output"
	@echo "  make migrate-recursive INPUT_DIR=/path/to/dashboards"
	@echo "  make migrate INPUT_DIR=/path/to/dashboards EXTRA_FLAGS='--wait=30s --perses-version=0.52.0'"
	@echo "  make migrate-recursive INPUT_DIR=/path/to/dashboards EXTRA_FLAGS='--wait=5s --cleanup=false'"