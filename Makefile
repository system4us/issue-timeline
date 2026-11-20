# issue-timeline developer entry points. Run from the repo root.
#   make          list the targets
SHELL := /bin/sh
.DEFAULT_GOAL := help
.PHONY: help deps gen gen-check ui build dev e2e test npm lint-api fmt

help: ## Show this list
	@echo "usage: make <target>"
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

deps: ## Install the web app's dependencies (npm, for the build and its tests only)
	cd web && npm ci

gen: ## Regenerate web/src/api/schema.d.ts from api/openapi.yaml
	cd web && npm run --silent gen:api

gen-check: ## Regenerate, then fail if the generated file differs from the commit
	$(MAKE) gen
	@git ls-files --error-unmatch -- web/src/api/schema.d.ts >/dev/null 2>&1 || { echo "web/src/api/schema.d.ts is not committed yet: commit it first, so gen-check can compare"; exit 1; }
	git diff --exit-code -- web/src/api/schema.d.ts

ui: ## Build the web app (web/dist) and copy it into internal/webui/dist
	cd web && npm run build
	mkdir -p internal/webui/dist
	find internal/webui/dist -mindepth 1 ! -name README -delete
	cp -r web/dist/. internal/webui/dist/

build: ui ## Build the web app into the binary, then bin/issue-timeline
	go build -trimpath -o bin/issue-timeline ./cmd/issue-timeline

dev: ## Run the server from source on :4310 (and 'cd web && npm run dev' for the page on :5173)
	go run ./cmd/issue-timeline

e2e: build ## The end-to-end test over HTTP against bin/issue-timeline (a fake Redmine, sessions and gates)
	node --test test/e2e/*.test.js

test: ## go test -race on every package, the web unit tests (Vitest), then the end-to-end test
	go test -race ./...
	cd web && npm test
	$(MAKE) e2e

npm: ui ## Build the npm packages of VERSION into dist/npm (the CI publishes them on a vX.Y.Z tag)
	@test -n "$(VERSION)" || { echo "usage: make npm VERSION=1.0.0-beta.5"; exit 1; }
	node scripts/npm.mjs $(VERSION)

lint-api: ## Validate api/openapi.yaml with Redocly
	npx --yes @redocly/cli@1 lint api/openapi.yaml

fmt: ## gofmt -w on cmd and internal
	gofmt -w cmd internal
