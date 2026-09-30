BINARY  := buildfleetd
PKG     := github.com/funcx27/kite/modules/buildfleet
PROTO_DIR := api/proto
PROTO    := buildfleet/v1/service.proto
REGISTRY_HOST ?= MUST-SET
BUILDFLEET_IMAGE ?= moby/buildkit:master

.PHONY: all build proto test lint fmt vet clean e2e install-tools

all: fmt vet lint test build

## build: compile binaries and write a ready-to-run bin/buildfleet.yaml
build:
	go build -o bin/$(BINARY) ./cmd/$(BINARY)
	go build -o bin/apiclient ./examples/apiclient
	go build -o bin/basic ./examples/basic
	go build -o bin/gitclient ./examples/gitclient
	go build -o bin/httpclient ./examples/httpclient

## proto: regenerate Go + Connect code from .proto
proto:
	protoc --proto_path=$(PROTO_DIR) \
		--go_out=. --go_opt=module=$(PKG) \
		--connect-go_out=. --connect-go_opt=module=$(PKG) \
		$(PROTO)

## install-tools: protoc plugins needed by `make proto`
install-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest

## test: unit tests
test:
	go test ./...

## lint: staticcheck (expects a go1.27-compatible build)
lint:
	staticcheck ./...

## vet / fmt
vet:
	go vet ./...

fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './.*')

## e2e: full build via gitclient against the k8s verification stack
## (gitea repo + authenticated registry; see deploy/gitea/)
e2e: build
	./bin/$(BINARY) -config bin/buildfleet.yaml &
	sleep 1
	./bin/gitclient -config bin/gitclient.yaml
	curl -s -u bfadmin:bftest123 http://$(REGISTRY_HOST)/v2/_catalog
	pkill -f bin/$(BINARY) || true

## clean: remove build artifacts
clean:
	rm -rf bin/
