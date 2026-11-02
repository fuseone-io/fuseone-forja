.PHONY: build vet lint test race docker-test integration check serve

GOLANGCI_LINT ?= golangci-lint

build:
	go build -o bin/forjad ./cmd/forjad

vet:
	go vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

# test and race skip the tests that drive the Docker Engine; docker-test
# runs them once, under the race detector, and fails if no daemon answers.
test:
	go test ./...

race:
	go test -race ./...

# DOCKER_HOST falls back to the active docker context, so colima and
# Docker Desktop work without exporting it.
docker-test:
	DOCKER_HOST="$${DOCKER_HOST:-$$(docker context inspect --format '{{.Endpoints.docker.Host}}')}" \
	FORJA_DOCKER_TEST=1 go test -race -count=1 ./internal/workspace/ ./cmd/forjad/

# Needs the contract's postgres sidecar reachable by name: it runs as a
# gate inside a Forja run, not in check.
integration:
	go test -tags integration -count=1 -run '^TestPostgresSidecar' ./internal/workspace/

check: build vet lint test race docker-test

serve:
	go run ./cmd/forjad serve
