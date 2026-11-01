.PHONY: build vet lint test race check serve

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

check: build vet lint test race

serve:
	go run ./cmd/forjad serve
