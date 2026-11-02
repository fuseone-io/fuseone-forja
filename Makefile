.PHONY: build vet lint test race integration hardened check serve

build:
	go build -o bin/forjad ./cmd/forjad

vet:
	go vet ./...

lint:
	golangci-lint run ./...

test:
	go test ./...

race:
	go test -race ./...

# Needs the contract's postgres sidecar reachable by name: it runs as a
# gate inside a Forja run, not in check.
integration:
	go test -tags integration -count=1 -run '^TestPostgresSidecar' ./internal/workspace/

# The whole workspace suite under gVisor. Needs a daemon that lists runsc
# among its runtimes, so it is not part of check.
hardened:
	FORJA_WORKSPACE_RUNTIME=runsc go test -count=1 ./internal/workspace/

check: build vet lint test race

serve:
	go run ./cmd/forjad serve
