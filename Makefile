.PHONY: build vet lint test race integration check serve

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

check: build vet lint test race

serve:
	go run ./cmd/forjad serve
