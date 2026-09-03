# CIT — task runner.
#
# On Windows without GNU make, use the equivalent PowerShell wrapper:
#   .\make.ps1 dev | test | build

MODULE  := github.com/clownface471/cit
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X $(MODULE)/cmd.Version=$(VERSION) \
           -X $(MODULE)/cmd.Commit=$(COMMIT) \
           -X $(MODULE)/cmd.BuildDate=$(DATE)

# Non-negotiable: a cgo build breaks cross-compilation in CI.
export CGO_ENABLED := 0
export GOPROXY     := https://proxy.golang.org,direct

.PHONY: help dev test build clean

help:
	@echo "dev    - jalankan aplikasi dengan hot reload"
	@echo "test   - go vet + go test lapis inti (./internal/...)"
	@echo "build  - bangun binari untuk platform saat ini (build/bin/)"
	@echo "clean  - hapus keluaran build"

dev:
	wails dev -ldflags "$(LDFLAGS)"

# Only the pure-Go layers. The desktop shell (root and ./cmd) is left out on
# purpose: Wails forces CGO_ENABLED=1 on macOS and Linux to bind webkit2gtk and
# Cocoa, so it cannot be checked under this rule. `make build` covers it.
test:
	go vet ./internal/...
	go test -count=1 ./internal/...

build:
	wails build -clean -trimpath -ldflags "$(LDFLAGS)"

clean:
	rm -rf build/bin frontend/build/* frontend/.svelte-kit
	@touch frontend/build/.gitkeep
