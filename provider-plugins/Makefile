UNAME_S := $(shell uname -s)
VERSION ?= 0.2.0
BIN_DIR := $(CURDIR)/bin
DIST_DIR := $(CURDIR)/dist
GOOS_NAME := $(shell go env GOOS)
GOARCH_NAME := $(shell go env GOARCH)

ifeq ($(OS),Windows_NT)
PLUGIN_EXT := dll
else ifeq ($(UNAME_S),Darwin)
PLUGIN_EXT := dylib
else
PLUGIN_EXT := so
endif

LINGMA_ARTIFACT := $(BIN_DIR)/lingma-plugin-v$(VERSION).$(PLUGIN_EXT)

.PHONY: build test fmt package checksums package-check release-check clean force

build: $(LINGMA_ARTIFACT)

test:
	go test ./...

fmt:
	gofmt -w .

package: build
	mkdir -p $(DIST_DIR)
	rm -f $(DIST_DIR)/lingma-plugin_$(VERSION)_$(GOOS_NAME)_$(GOARCH_NAME).zip $(DIST_DIR)/checksums.txt
	cp $(LINGMA_ARTIFACT) $(DIST_DIR)/lingma-plugin.$(PLUGIN_EXT)
	cd $(DIST_DIR) && zip -q lingma-plugin_$(VERSION)_$(GOOS_NAME)_$(GOARCH_NAME).zip lingma-plugin.$(PLUGIN_EXT)
	rm -f $(DIST_DIR)/lingma-plugin.$(PLUGIN_EXT)
	$(MAKE) checksums
	$(MAKE) package-check

checksums:
	cd $(DIST_DIR) && if command -v sha256sum >/dev/null 2>&1; then sha256sum *.zip > checksums.txt; else shasum -a 256 *.zip > checksums.txt; fi

package-check:
	test "$$(unzip -Z1 $(DIST_DIR)/lingma-plugin_$(VERSION)_$(GOOS_NAME)_$(GOARCH_NAME).zip)" = "lingma-plugin.$(PLUGIN_EXT)"
	cd $(DIST_DIR) && if command -v sha256sum >/dev/null 2>&1; then sha256sum -c checksums.txt; else shasum -a 256 -c checksums.txt; fi

release-check: fmt test package

clean:
	rm -f $(LINGMA_ARTIFACT)
	rm -f $(DIST_DIR)/lingma-plugin_$(VERSION)_$(GOOS_NAME)_$(GOARCH_NAME).zip $(DIST_DIR)/checksums.txt

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

$(LINGMA_ARTIFACT): force | $(BIN_DIR)
	CGO_ENABLED=1 go build -buildmode=c-shared -o $@ ./cmd/lingma
	rm -f $(basename $@).h

force:
