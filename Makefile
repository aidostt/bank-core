SHELL := bash
COMPOSE := docker compose -f deploy/compose/docker-compose.yml
GOBIN := $(shell go env GOPATH)/bin
export PATH := $(PATH):$(GOBIN)

MODULES := pkg gen/go services/gateway services/identity services/account services/ledger services/transfer services/antifraud services/notification
IT_MODULES := services/identity services/account services/ledger services/transfer

.PHONY: up up-observability down demo test test-integration e2e lint generate \
        verify-ledger load chaos helm-deploy dlq-inspect replay-projections tidy

COMPOSE_OBS := docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/compose.observability.yml

up:
	$(COMPOSE) --profile core up -d --build

up-observability:
	$(COMPOSE_OBS) --profile core --profile obs up -d --build

down:
	$(COMPOSE_OBS) --profile core --profile obs down -v

demo:
	bash scripts/demo.sh

verify-ledger:
	bash scripts/verify-ledger.sh

test:
	@set -e; for m in $(MODULES); do echo "--- go test $$m"; (cd $$m && go test ./...); done

coverage: ## cross-package coverage per module (needs Docker for integration tests)
	bash scripts/coverage.sh

test-integration:
	@set -e; for m in $(IT_MODULES); do echo "--- go test -tags integration $$m"; (cd $$m && go test -tags integration -count=1 -timeout 20m ./...); done

e2e: ## requires a running stack (make up)
	cd tests/e2e && go test -tags e2e -count=1 -timeout 15m ./...

lint:
	buf lint
	@set -e; for m in $(MODULES); do echo "--- golangci-lint $$m"; (cd $$m && golangci-lint run ./...); done

generate:
	buf generate
	sqlc generate
	buf lint

tidy:
	@set -e; for m in $(MODULES); do echo "--- go mod tidy $$m"; (cd $$m && go mod tidy); done

load: ## requires a running stack (make up) and k6
	k6 run load/k6/transfers.js
	k6 run load/k6/reads.js

chaos: ## requires a running stack (make up)
	bash scripts/chaos.sh

helm-deploy: ## requires k3d, kubectl, helm
	bash scripts/helm-deploy.sh

dlq-inspect:
	bash scripts/dlq-inspect.sh

replay-projections:
	bash scripts/replay-projections.sh
