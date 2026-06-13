APP := pax-manager
PKG := ./...
BIN_DIR := bin
BIN := $(BIN_DIR)/$(APP)
GOCACHE ?= /tmp/pax-manager-go-cache
DATABASE_URL ?= postgres://pax:pax@localhost:5432/paxdb?sslmode=disable
PORT ?= 9879

.PHONY: help
help:
	@printf "Targets:\n"
	@printf "  make build          Build local manager binary\n"
	@printf "  make run            Run manager locally with DATABASE_URL\n"
	@printf "  make run-memory     Run manager locally with in-memory storage\n"
	@printf "  make test           Run Go tests\n"
	@printf "  make test-race      Run Go tests with race detector\n"
	@printf "  make fmt            Format Go files\n"
	@printf "  make tidy           Run go mod tidy\n"
	@printf "  make docker-build   Build manager Docker image\n"
	@printf "  make cloud-build    Submit Cloud Build using cloudbuild.yaml\n"
	@printf "  make up             Start Postgres and manager with Docker Compose\n"
	@printf "  make db-up          Start only Postgres with Docker Compose\n"
	@printf "  make db-reset       Recreate local Postgres volume and start Postgres\n"
	@printf "  make down           Stop Docker Compose services\n"
	@printf "  make logs           Tail Docker Compose logs\n"
	@printf "  make psql           Open psql in the Postgres container\n"
	@printf "  make clean          Remove local build output\n"

.PHONY: build
build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN) ./cmd/manager

.PHONY: run
run:
	PORT=$(PORT) DATABASE_URL="$(DATABASE_URL)" go run ./cmd/manager

.PHONY: run-memory
run-memory:
	PORT=$(PORT) DATABASE_URL= go run ./cmd/manager

.PHONY: test
test:
	GOCACHE=$(GOCACHE) go test -count=1 $(PKG)

.PHONY: test-race
test-race:
	GOCACHE=$(GOCACHE) go test -race -count=1 $(PKG)

.PHONY: fmt
fmt:
	gofmt -w $$(find cmd -name '*.go' -type f)

.PHONY: tidy
tidy:
	GOCACHE=$(GOCACHE) go mod tidy

.PHONY: docker-build
docker-build:
	docker build -t $(APP):local .

.PHONY: cloud-build
cloud-build:
	gcloud builds submit --config cloudbuild.yaml

.PHONY: up
up:
	docker compose up --build

.PHONY: db-up
db-up:
	docker compose up --build -d postgres

.PHONY: db-reset
db-reset:
	docker compose down -v
	docker compose up --build -d postgres

.PHONY: down
down:
	docker compose down

.PHONY: logs
logs:
	docker compose logs -f

.PHONY: psql
psql:
	docker compose exec postgres psql -U pax -d paxdb

.PHONY: clean
clean:
	rm -rf $(BIN_DIR)
