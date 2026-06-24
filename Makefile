BINARY  := twist
CMD     := ./cmd/twist
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

test:
	go test -tags lang_all ./...

vet:
	go vet -tags lang_all ./...

clean:
	rm -f $(BINARY)

.PHONY: build build-release build-minimal build-no-heavy build-custom test vet clean
