BINARY := miya-channels
GOOS ?= linux
GOARCH ?= amd64
GOFLAGS ?= -ldflags="-s -w"
OUTPUT := bin/$(BINARY)-$(GOOS)-$(GOARCH)

.PHONY: all build clean

all: build

build:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(GOFLAGS) -o $(OUTPUT) .

clean:
	rm -rf bin
