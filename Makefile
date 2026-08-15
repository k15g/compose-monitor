# Build automation. CI runs these same targets, so what passes here passes
# there.

BINARY := compose-monitor

# The version stamped into an image built here. A tagged commit is its own
# version; anything else gets a timestamp and the commit, matching what CI does
# for a build that is not a release.
# Piping git through sed would hide its exit status — sed succeeds on empty
# input — so the tag is captured first and tested.
VERSION ?= $(shell tag=$$(git describe --tags --exact-match 2>/dev/null); \
	if [ -n "$$tag" ]; then echo "$${tag#v}"; \
	else echo "$$(date +%s)-$$(git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)"; fi)

.PHONY: all deps generate fmt vet test cover lint build run image version clean check

all: check build

## deps: download the module's dependencies
deps:
	go mod download

## generate: regenerate the templ templates
#
# The CLI version is read from go.mod rather than pinned here, so the generator
# can never disagree with the runtime the generated code compiles against.
generate:
	go run github.com/a-h/templ/cmd/templ@$(shell go list -m -f '{{.Version}}' github.com/a-h/templ) generate

## fmt: format the Go sources
fmt:
	gofmt -w .

## vet: run the standard vet checks
vet:
	go vet ./...

## test: run the tests with the race detector
test:
	go test -race ./...

## cover: report per-package test coverage
cover:
	go test -race -cover ./...

## lint: run golangci-lint
lint:
	golangci-lint run

## check: everything CI checks, short of building
check: vet test lint

## build: build the server binary
build:
	CGO_ENABLED=0 go build -o bin/$(BINARY) ./cmd/server

## run: run the server from source
run:
	go run ./cmd/server

## image: build the container image
image:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):dev .

## version: print the version an image built here would carry
version:
	@echo $(VERSION)

## clean: remove build output
clean:
	rm -rf bin
