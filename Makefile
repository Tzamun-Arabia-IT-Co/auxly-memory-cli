.PHONY: build run test clean install tui

BINARY=auxly
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

build:
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) .

run: build
	./$(BINARY)

tui: build
	./$(BINARY) ui

test:
	go test ./...

clean:
	rm -f $(BINARY)

install: build
	cp $(BINARY) /usr/local/bin/$(BINARY)

init: build
	./$(BINARY) init

release:
	goreleaser release --clean
