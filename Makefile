SHELL := /usr/bin/env bash

.PHONY: bootstrap generate check-fdg-v2 test-unit test-integration test-e2e build dev check
bootstrap:
	go mod download
	go -C tools/openapi mod download
	npm --prefix web ci
generate:
	./scripts/generate-openapi.sh
check-fdg-v2:
	./scripts/check-fdg-v2.sh
test-unit: generate
	go test ./...
	python3 -m unittest scripts/test_check_template_version.py scripts/test_generate_openapi.py
	npm --prefix web test
test-integration: generate
	./scripts/with-dex.sh go test -tags=integration ./...
test-e2e: generate
	./scripts/with-dex.sh ./scripts/run-e2e.sh
build: generate
	npm --prefix web run build
	go build -o bin/event-booking ./cmd/server
dev: generate
	./scripts/with-dex.sh bash -c 'npm --prefix web run build && go run ./cmd/server'
check: bootstrap generate check-fdg-v2
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './upstream-dex/*'))" || { gofmt -d $$(gofmt -l $$(find . -name '*.go' -not -path './upstream-dex/*')); exit 1; }
	go mod tidy -diff
	go vet ./...
	go test ./...
	python3 -m unittest scripts/test_check_template_version.py scripts/test_generate_openapi.py
	npm --prefix web test
	./scripts/with-dex.sh go test -tags=integration ./...
	./scripts/with-dex.sh ./scripts/run-e2e.sh
	npm --prefix web run build
	go build -o bin/event-booking ./cmd/server
