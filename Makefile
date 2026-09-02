.PHONY: build test run docker-build

build:
	go build -trimpath -o /tmp/codereview ./cmd/codereview
	go build -trimpath -o /tmp/codereview-webhook ./cmd/codereview-webhook

test:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go test -race -count=1 ./...
	go build -trimpath -o /tmp/codereview ./cmd/codereview
	go build -trimpath -o /tmp/codereview-webhook ./cmd/codereview-webhook

run:
	go run ./cmd/codereview-webhook

docker-build:
	docker build -t codereview:local .
