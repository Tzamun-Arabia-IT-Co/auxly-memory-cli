.PHONY: build run test clean install tui check dev

BINARY=auxly
# Strip a leading "v" from the tag so it matches internal/update.Current ("1.0.0").
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo "dev")
LDFLAGS=-s -w -X github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update.Current=$(VERSION)

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BINARY) .

run: build
	./$(BINARY)

tui: build
	./$(BINARY) ui

test:
	go test ./...

check:
	gofmt -l .
	go vet ./...
	go test -race ./...

clean:
	rm -f $(BINARY)

# Build a dev binary into ~/.local/bin. The git tag isn't always present locally,
# so derive the version from the VERSION file with a -dev marker. Run after edits
# so `auxly-dev` always reflects current source.
dev:
	CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/update.Current=$(shell cat VERSION)-dev" -o $(HOME)/.local/bin/auxly-dev .
	@$(HOME)/.local/bin/auxly-dev --version | head -1

install: build
	cp $(BINARY) /usr/local/bin/$(BINARY)

init: build
	./$(BINARY) init

release:
	goreleaser release --clean
