UNAME_S := $(shell uname -s)
VERSION ?= 0.1.0
BIN_DIR := $(CURDIR)/bin

ifeq ($(OS),Windows_NT)
PLUGIN_EXT := dll
else ifeq ($(UNAME_S),Darwin)
PLUGIN_EXT := dylib
else
PLUGIN_EXT := so
endif

LINGMA_ARTIFACT := $(BIN_DIR)/lingma-plugin-v$(VERSION).$(PLUGIN_EXT)
TRAE_ARTIFACT := $(BIN_DIR)/trae-plugin-v$(VERSION).$(PLUGIN_EXT)

.PHONY: build test fmt clean force

build: $(LINGMA_ARTIFACT) $(TRAE_ARTIFACT)

test:
	go test ./...

fmt:
	gofmt -w .

clean:
	rm -rf $(BIN_DIR)

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

$(LINGMA_ARTIFACT): force | $(BIN_DIR)
	CGO_ENABLED=1 go build -buildmode=c-shared -o $@ ./cmd/lingma
	rm -f $(basename $@).h

$(TRAE_ARTIFACT): force | $(BIN_DIR)
	CGO_ENABLED=1 go build -buildmode=c-shared -o $@ ./cmd/trae
	rm -f $(basename $@).h

force:
