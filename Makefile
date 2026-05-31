.PHONY: build run test clean install tui check

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

install: build
	cp $(BINARY) /usr/local/bin/$(BINARY)

init: build
	./$(BINARY) init

release:
	goreleaser release --clean
