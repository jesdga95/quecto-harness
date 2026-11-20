BINARY  := quecto-harness
VERSION ?= 0.1.0
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

.PHONY: all web build test fmt vet check clean run desktop

all: build

# The UI is embedded at compile time, so it is rebuilt before the binary. The
# build empties dist, and the tracked .gitkeep is put back after it.
web:
	cd web && npm ci && npm run build
	touch internal/web/dist/.gitkeep

build: web
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/quecto-harness

# Backend-only work needs no Node; the binary then serves the fallback page.
backend:
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/quecto-harness

test:
	go test -race ./...

fmt:
	gofmt -l .

vet:
	go vet ./...

check: fmt vet test

run: backend
	./$(BINARY)

# The desktop app bundles the binary built for this machine's architecture.
desktop: build
	mkdir -p desktop/bin
	cp $(BINARY) desktop/bin/$(BINARY)
	cd desktop && npm ci && npm run dist

clean:
	rm -f $(BINARY)
	rm -rf desktop/dist desktop/bin
