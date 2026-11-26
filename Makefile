GO      ?= go
VERSION ?= 0.0.1
TAILWIND ?= tailwindcss

.PHONY: help build run test test-race vet fmt css css-watch docker docker-proxy clean

help:
	@echo "build        build ./docktimer"
	@echo "run          run locally (needs .env in the environment)"
	@echo "test         go test ./..."
	@echo "test-race    go test -race ./..."
	@echo "vet          go vet ./..."
	@echo "fmt          gofmt -w ."
	@echo "css          rebuild web/static/css/app.css with the Tailwind CLI"
	@echo "docker       build the container image"
	@echo "docker-proxy start with docker-socket-proxy override"

build:
	CGO_ENABLED=0 $(GO) build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" -o docktimer ./cmd/server

run:
	$(GO) run ./cmd/server

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

css:
	$(TAILWIND) --input web/tailwind/input.css --output web/static/css/app.css --minify

css-watch:
	$(TAILWIND) --input web/tailwind/input.css --output web/static/css/app.css --watch

docker:
	docker build --build-arg VERSION=$(VERSION) -t docktimer:local .

docker-proxy:
	docker compose -f docker-compose.yml -f docker-compose.socket-proxy.yml up -d --build

clean:
	rm -f docktimer
