SHELL := /usr/bin/env bash

.PHONY: bootstrap generate check-generated check-fdg-v2 test-unit test-integration test-e2e test-mock-e2e build dev mock check
bootstrap:
	go mod download
	go -C tools/openapi mod download
	npm --prefix web ci
generate:
	go -C tools/openapi tool ogen --target ../../internal/api/generated --package generated ../../openapi/openapi.yaml
	npm --prefix web run generate
check-generated:
	./scripts/check-generated.sh
check-fdg-v2:
	./scripts/check-fdg-v2.sh
test-unit:
	go test ./...
	python3 -m unittest scripts/test_check_template_version.py
	npm --prefix web test
test-integration:
	./scripts/with-dex.sh go test -tags=integration ./...
test-e2e:
	./scripts/with-dex.sh ./scripts/run-e2e.sh
test-mock-e2e:
	./scripts/run-mock-e2e.sh
build:
	npm --prefix web run build
	go build -o bin/event-booking ./cmd/server
dev:
	./scripts/with-dex.sh bash -c 'npm --prefix web run build && go run ./cmd/server'
mock:
	./scripts/with-mock.sh
check: bootstrap check-generated check-fdg-v2
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './upstream-dex/*'))" || { gofmt -d $$(gofmt -l $$(find . -name '*.go' -not -path './upstream-dex/*')); exit 1; }
	go mod tidy -diff
	go vet ./...
	$(MAKE) test-unit
	$(MAKE) test-integration
	$(MAKE) test-e2e
	$(MAKE) test-mock-e2e
	$(MAKE) build
