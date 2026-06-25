APP := pax-manager
PKG := ./...
BIN_DIR := bin
BIN := $(BIN_DIR)/$(APP)
GOCACHE ?= /tmp/pax-manager-go-cache
GOLANGCI_LINT_CACHE ?= /tmp/pax-manager-golangci-lint-cache
DATABASE_URL ?= postgres://pax:pax@localhost:5432/paxdb?sslmode=disable
PORT ?= 9879
INTEGRATION_PORT ?= 19879
HZ_IDL := api/pax_manager.thrift
HZ_MODULE := github.com/pax-beehive/pax-manager
HZ_HANDLER_DIR := internal/transport/http/handler
HZ_MODEL_DIR := internal/transport/http/model
HZ_UPDATE_FLAGS := --idl $(HZ_IDL) --module $(HZ_MODULE) --out_dir . --handler_dir $(HZ_HANDLER_DIR) --model_dir $(HZ_MODEL_DIR) --sort_router --handler_by_method -t go:nil_safe
MOCKERY := go run github.com/vektra/mockery/v2@v2.53.5
GOLANGCI_LINT := go tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint
GOIMPORTS := go tool golang.org/x/tools/cmd/goimports
GOLINES := go tool github.com/segmentio/golines
GO_MODULE := github.com/pax-beehive/pax-manager
GOFILES_NO_GENERATED := $$(find cmd internal integration -name '*.go' -type f -exec sh -c 'for f do if ! head -n 3 "$$f" | grep -q "Code generated"; then printf "%s\n" "$$f"; fi; done' sh {} +)
COVER_PKGS := $$(go list ./cmd/... ./internal/... | grep -vE '/mocks$$|/internal/transport/http/(model|router)')

.PHONY: help
help:
	@printf "Targets:\n"
	@printf "  make build          Build local manager binary\n"
	@printf "  make run            Run manager locally with DATABASE_URL\n"
	@printf "  make run-memory     Run manager locally with in-memory storage\n"
	@printf "  make test           Run Go tests\n"
	@printf "  make test-coverage  Run Go tests with coverage\n"
	@printf "  make test-race      Run Go tests with race detector\n"
	@printf "  make integration-test Start Docker Compose and run integration tests\n"
	@printf "  make integration-down Stop the integration Docker Compose stack\n"
	@printf "  make paxd-integration-up Start manager + postgres + paxd integration stack\n"
	@printf "  make paxd-integration-down Stop the paxd integration Docker Compose stack\n"
	@printf "  make paxd-integration-logs Tail the paxd integration stack logs\n"
	@printf "  make lint           Run golangci-lint\n"
	@printf "  make fmt-check      Check gofmt, goimports, and golines formatting\n"
	@printf "  make generate       Generate derived source files\n"
	@printf "  make gorm-gen       Regenerate GORM models and query helpers from DATABASE_URL\n"
	@printf "  make hz-update      Regenerate Hertz router and model from Thrift IDL\n"
	@printf "  make mocks          Regenerate interface mocks with mockery\n"
	@printf "  make fmt            Format Go files\n"
	@printf "  make tidy           Run go mod tidy\n"
	@printf "  make docker-build   Build manager Docker image\n"
	@printf "  make cloud-build    Submit Cloud Build using cloudbuild.yaml\n"
	@printf "  make up             Start Postgres and manager with Docker Compose\n"
	@printf "  make db-up          Start only Postgres with Docker Compose\n"
	@printf "  make db-ensure      Create local paxdb database if missing\n"
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
	PORT=$(PORT) DATABASE_URL="$(DATABASE_URL)" CLOUDFLARE_ACCESS_DISABLED=true ALLOW_LOCAL_USER_HEADER=true go run ./cmd/manager

.PHONY: run-memory
run-memory:
	PORT=$(PORT) DATABASE_URL= CLOUDFLARE_ACCESS_DISABLED=true ALLOW_LOCAL_USER_HEADER=true go run ./cmd/manager

.PHONY: test
test:
	GOCACHE=$(GOCACHE) go test -count=1 $(PKG)

.PHONY: test-coverage
test-coverage:
	GOCACHE=$(GOCACHE) go test -count=1 -covermode=atomic -coverprofile=coverage.out $(COVER_PKGS)
	go tool cover -func=coverage.out

.PHONY: test-race
test-race:
	GOCACHE=$(GOCACHE) go test -race -count=1 $(PKG)

.PHONY: integration-test
integration-test:
	INTEGRATION_PORT=$(INTEGRATION_PORT) docker compose -f docker-compose.integration.yml -p pax-manager-integration down -v --remove-orphans
	INTEGRATION_PORT=$(INTEGRATION_PORT) docker compose -f docker-compose.integration.yml -p pax-manager-integration up --build -d postgres manager
	INTEGRATION_BASE_URL=http://localhost:$(INTEGRATION_PORT) GOCACHE=$(GOCACHE) go test -tags=integration -count=1 ./integration

.PHONY: integration-down
integration-down:
	INTEGRATION_PORT=$(INTEGRATION_PORT) docker compose -f docker-compose.integration.yml -p pax-manager-integration down -v --remove-orphans

.PHONY: paxd-integration-up
paxd-integration-up:
	INTEGRATION_PORT=$(INTEGRATION_PORT) docker compose -f docker-compose.paxd-integration.yml -p pax-manager-paxd-integration up --build -d postgres manager paxd

.PHONY: paxd-integration-down
paxd-integration-down:
	INTEGRATION_PORT=$(INTEGRATION_PORT) docker compose -f docker-compose.paxd-integration.yml -p pax-manager-paxd-integration down -v --remove-orphans

.PHONY: paxd-integration-logs
paxd-integration-logs:
	INTEGRATION_PORT=$(INTEGRATION_PORT) docker compose -f docker-compose.paxd-integration.yml -p pax-manager-paxd-integration logs -f

.PHONY: lint
lint:
	GOCACHE=$(GOCACHE) GOLANGCI_LINT_CACHE=$(GOLANGCI_LINT_CACHE) $(GOLANGCI_LINT) run
	GOCACHE=$(GOCACHE) GOLANGCI_LINT_CACHE=$(GOLANGCI_LINT_CACHE) $(GOLANGCI_LINT) run --build-tags integration ./integration

.PHONY: fmt-check
fmt-check:
	@files="$$(gofmt -l $(GOFILES_NO_GENERATED))"; if [ -n "$$files" ]; then printf "gofmt needed:\n%s\n" "$$files"; exit 1; fi
	@files="$$(GOCACHE=$(GOCACHE) $(GOIMPORTS) -local $(GO_MODULE) -l $(GOFILES_NO_GENERATED))" || exit $$?; if [ -n "$$files" ]; then printf "goimports needed:\n%s\n" "$$files"; exit 1; fi
	@files="$$(GOCACHE=$(GOCACHE) $(GOLINES) --ignore-generated --base-formatter=gofmt --max-len=100 -l $(GOFILES_NO_GENERATED))" || exit $$?; if [ -n "$$files" ]; then printf "golines needed:\n%s\n" "$$files"; exit 1; fi

.PHONY: generate
generate: hz-update mocks
	GOCACHE=$(GOCACHE) go generate ./internal/manager

.PHONY: gorm-gen
gorm-gen:
	GOCACHE=$(GOCACHE) DATABASE_URL="$(DATABASE_URL)" go run ./cmd/gormgen

.PHONY: hz-update
hz-update:
	hz update $(HZ_UPDATE_FLAGS)

.PHONY: mocks
mocks:
	GOCACHE=$(GOCACHE) $(MOCKERY) --config .mockery.yaml

.PHONY: fmt
fmt:
	gofmt -w $(GOFILES_NO_GENERATED)
	GOCACHE=$(GOCACHE) $(GOIMPORTS) -local $(GO_MODULE) -w $(GOFILES_NO_GENERATED)
	GOCACHE=$(GOCACHE) $(GOLINES) --ignore-generated --base-formatter=gofmt --max-len=100 -w $(GOFILES_NO_GENERATED)

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
up: db-ensure
	docker compose up --build -d manager

.PHONY: db-up
db-up: db-ensure

.PHONY: db-ensure
db-ensure:
	docker compose up --build -d postgres
	docker compose exec -T postgres sh -c 'until pg_isready -U "$$POSTGRES_USER" -d postgres >/dev/null 2>&1; do sleep 1; done'
	docker compose exec -T postgres sh -c 'if ! psql -U "$$POSTGRES_USER" -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '\''$$POSTGRES_DB'\''" | grep -q 1; then createdb -U "$$POSTGRES_USER" "$$POSTGRES_DB"; fi'

.PHONY: db-reset
db-reset:
	docker compose down -v
	$(MAKE) db-ensure

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
