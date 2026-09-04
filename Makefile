.PHONY: dev build test

dev:
	START_DEV=1 ./start.sh

build:
	./start.sh

test:
	go test ./...
