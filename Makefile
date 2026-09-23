.PHONY: dev build test

ifeq ($(OS),Windows_NT)
START := powershell -NoProfile -ExecutionPolicy Bypass -File ./start.ps1
DEV   := $(START) dev
else
START := ./start.sh
DEV   := START_DEV=1 ./start.sh
endif

dev:
	$(DEV)

build:
	$(START)

test:
	go test ./...
