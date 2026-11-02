.PHONY: build vet lint test race docker-test integration hardened check gates serve

GOLANGCI_LINT ?= golangci-lint

# DOCKER_HOST falls back to the active docker context, so colima and
# Docker Desktop work without exporting it.
DOCKER_ENV = DOCKER_HOST="$${DOCKER_HOST:-$$(docker context inspect --format '{{.Endpoints.docker.Host}}')}" FORJA_DOCKER_TEST=1

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

docker-test:
	$(DOCKER_ENV) go test -race -count=1 ./internal/workspace/ ./cmd/forjad/

# Needs the contract's postgres sidecar reachable by name: it runs as a
# gate inside a Forja run, not in check.
integration:
	go test -tags integration -count=1 -run '^TestPostgresSidecar' ./internal/workspace/

# The whole workspace suite under gVisor. Needs a daemon that lists runsc
# among its runtimes, so it is not part of check.
hardened:
	$(DOCKER_ENV) FORJA_WORKSPACE_RUNTIME=runsc go test -count=1 ./internal/workspace/

check: build vet lint test race docker-test

# Every gate in one call, a table of exit codes, logs on disk.
gates:
	scripts/gates.sh

serve:
	go run ./cmd/forjad serve
