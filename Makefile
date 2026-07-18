SHELL := bash
COMPOSE := docker compose -f deploy/compose/docker-compose.yml
GOBIN := $(shell go env GOPATH)/bin
export PATH := $(PATH):$(GOBIN)

MODULES := pkg gen/go services/gateway services/identity services/account services/ledger services/transfer services/antifraud services/notification
IT_MODULES := services/identity services/account services/ledger services/transfer

.PHONY: up up-observability down demo test test-integration e2e lint generate \
        verify-ledger load chaos helm-deploy dlq-inspect tidy

up:
	$(COMPOSE) --profile core up -d --build

up-observability: ## obs profile (prometheus, grafana, jaeger) ships in M2
	@echo "observability profile ships in M2 (see prompts/M2.md)"; exit 1

down:
	$(COMPOSE) --profile core down -v

demo:
	bash scripts/demo.sh

verify-ledger:
	bash scripts/verify-ledger.sh

test:
	@set -e; for m in $(MODULES); do echo "--- go test $$m"; (cd $$m && go test ./...); done

test-integration:
	@set -e; for m in $(IT_MODULES); do echo "--- go test -tags integration $$m"; (cd $$m && go test -tags integration -count=1 -timeout 20m ./...); done

e2e: ## full cross-service e2e test ships in M2 (tests/e2e)
	@echo "e2e suite ships in M2 (see prompts/M2.md)"; exit 1

lint:
	buf lint
	@set -e; for m in $(MODULES); do echo "--- golangci-lint $$m"; (cd $$m && golangci-lint run ./...); done

generate:
	buf generate
	sqlc generate
	buf lint

tidy:
	@set -e; for m in $(MODULES); do echo "--- go mod tidy $$m"; (cd $$m && go mod tidy); done

load: ## k6 load scripts ship in M3
	@echo "load scripts ship in M3 (see prompts/M3.md)"; exit 1

chaos: ## toxiproxy chaos demo ships in M3
	@echo "chaos demo ships in M3 (see prompts/M3.md)"; exit 1

helm-deploy: ## helm + k3d ships in M3
	@echo "helm deploy ships in M3 (see prompts/M3.md)"; exit 1

dlq-inspect: ## DLQ wiring ships in M2
	@echo "DLQ inspection ships in M2 (see prompts/M2.md)"; exit 1
