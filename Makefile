BINARY  := indigo
CMD     := ./cmd/indigo
OUT     := $(BINARY)

# Default: all languages included.
build:
	go build -tags lang_all -o $(OUT) $(CMD)

# Strip debug info for a smaller binary.
build-release:
	go build -tags lang_all -ldflags="-s -w" -o $(OUT) $(CMD)

# No language grammars — smallest possible binary (no syntax highlighting).
build-minimal:
	go build -o $(OUT) $(CMD)

# Exclude the two largest grammars (Nim ~68 MB, Swift ~18 MB of C source).
build-no-heavy:
	go build -tags "lang_all lang_not_nim lang_not_swift" -ldflags="-s -w" -o $(OUT) $(CMD)

# Build with a specific set of languages. Override via: make build-custom LANGS="lang_go lang_rust"
LANGS ?= lang_go lang_python lang_typescript lang_rust
build-custom:
	go build -tags "$(LANGS)" -o $(OUT) $(CMD)

install: build-release
	mv $(OUT) $(shell go env GOPATH)/bin/$(BINARY)

test:
	go test -tags lang_all ./...

vet:
	go vet -tags lang_all ./...

lint:
	golangci-lint run --build-tags lang_all ./...

clean:
	rm -f $(BINARY)

# --- Plugin targets ---

PLUGINS_DIR := plugins
GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

JUMPY_DIR   := $(PLUGINS_DIR)/jumpy
JUMPY_OUT   := $(JUMPY_DIR)/jumpy-$(GOOS)-$(GOARCH)
JUMPY_INSTALL := $(HOME)/.config/indigo/plugins/jumpy

build-jumpy:
	go build -o $(JUMPY_OUT) ./$(JUMPY_DIR)

install-jumpy: build-jumpy
	mkdir -p $(JUMPY_INSTALL)
	mv $(JUMPY_OUT) $(JUMPY_INSTALL)/
	cp $(JUMPY_DIR)/plugin.toml $(JUMPY_INSTALL)/

uninstall-jumpy:
	rm -rf $(JUMPY_INSTALL)

SPELL_DIR   := $(PLUGINS_DIR)/indigo-spell
SPELL_OUT   := $(SPELL_DIR)/indigo-spell-$(GOOS)-$(GOARCH)
SPELL_INSTALL := $(HOME)/.config/indigo/plugins/indigo-spell

build-spell:
	go build -o $(SPELL_OUT) ./$(SPELL_DIR)

install-spell: build-spell
	mkdir -p $(SPELL_INSTALL)
	mv $(SPELL_OUT) $(SPELL_INSTALL)/
	cp $(SPELL_DIR)/plugin.toml $(SPELL_INSTALL)/

uninstall-spell:
	rm -rf $(SPELL_INSTALL)

GIT_DIR     := $(PLUGINS_DIR)/indigo-git
GIT_OUT     := $(GIT_DIR)/indigo-git-$(GOOS)-$(GOARCH)
GIT_INSTALL := $(HOME)/.config/indigo/plugins/indigo-git

build-git:
	go build -o $(GIT_OUT) ./$(GIT_DIR)

install-git: build-git
	mkdir -p $(GIT_INSTALL)
	mv $(GIT_OUT) $(GIT_INSTALL)/
	cp $(GIT_DIR)/plugin.toml $(GIT_INSTALL)/

uninstall-git:
	rm -rf $(GIT_INSTALL)

BOOKMARKS_DIR     := $(PLUGINS_DIR)/bookmarks
BOOKMARKS_OUT     := $(BOOKMARKS_DIR)/bookmarks-$(GOOS)-$(GOARCH)
BOOKMARKS_INSTALL := $(HOME)/.config/indigo/plugins/bookmarks

build-bookmarks:
	go build -o $(BOOKMARKS_OUT) ./$(BOOKMARKS_DIR)

install-bookmarks: build-bookmarks
	mkdir -p $(BOOKMARKS_INSTALL)
	mv $(BOOKMARKS_OUT) $(BOOKMARKS_INSTALL)/
	cp $(BOOKMARKS_DIR)/plugin.toml $(BOOKMARKS_INSTALL)/

uninstall-bookmarks:
	rm -rf $(BOOKMARKS_INSTALL)

build-plugins: build-jumpy build-spell build-git build-bookmarks

.PHONY: build build-release build-minimal build-no-heavy build-custom install test vet lint clean \
        build-jumpy install-jumpy uninstall-jumpy \
        build-spell install-spell uninstall-spell \
        build-git install-git uninstall-git \
        build-bookmarks install-bookmarks uninstall-bookmarks \
        build-plugins
