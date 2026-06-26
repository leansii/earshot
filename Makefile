# SPDX-License-Identifier: Apache-2.0
.DEFAULT_GOAL := check
VERSION ?= dev

.PHONY: build
build:
	go build -ldflags "-X main.version=$(VERSION)" ./...

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || \
		echo "golangci-lint not installed; ran go vet only"

.PHONY: fmt
fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

.PHONY: check
check: build vet lint test

.PHONY: run
run:
	go run ./cmd/earshot serve

.PHONY: docker
docker:
	docker build -t earshot:$(VERSION) .

.PHONY: migrate
migrate:
	go run ./cmd/earshot migrate
