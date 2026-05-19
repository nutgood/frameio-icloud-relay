.PHONY: build install lint vet fmt test clean

BIN := bin/frameio-icloud
VERSION ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/frameio-icloud

# Convenience: build then install the LaunchAgent on this Mac.
install: build
	./$(BIN) install

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

lint: vet
	@unformatted=$$(gofmt -l cmd internal); \
	if [ -n "$$unformatted" ]; then \
		echo "ERROR: files not formatted with gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

test:
	go test ./...

clean:
	rm -rf bin
