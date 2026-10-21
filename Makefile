# tfmetrics developer tasks. `make check` runs everything CI runs.

BINARY  := tfmetrics
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X github.com/rossedman/tfmetrics/cmd.version=$(VERSION) -X github.com/rossedman/tfmetrics/cmd.commit=$(COMMIT) -X github.com/rossedman/tfmetrics/cmd.date=$(DATE)
COVER_MIN ?= 90
FUZZTIME  ?= 30s
MKDOCS    := uv run --no-project --with-requirements requirements-docs.txt mkdocs

.PHONY: build test cover cover-gate bench fuzz fuzz-smoke bench-smoke lint vuln check-cmds doccheck docs docs-serve docs-build release-check check clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

test:
	go test -race ./...

cover:
	go test -race -coverprofile=coverage.out ./...
	@$(MAKE) --no-print-directory cover-gate

# Gate coverage on hand-written code only: generated files and examples are excluded.
cover-gate:
	@go run ./tools/generated ./... > .generated.txt
	@{ head -1 coverage.out; grep -v '^mode:' coverage.out | { if [ -s .generated.txt ]; then grep -vFf .generated.txt; else cat; fi; } | grep -v '/examples/'; } > coverage.hand.out
	@rm -f .generated.txt
	@go tool cover -func=coverage.hand.out | awk -v min=$(COVER_MIN) '/^total:/ {sub("%","",$$3); print "coverage: " $$3 "%"; if ($$3+0 < min) {print "coverage " $$3 "% < " min "%"; exit 1}}'

bench:
	go test -run='^$$' -bench=. -benchmem -count=6 ./... | tee bench.txt
	@echo "compare runs with: benchstat old.txt bench.txt"

bench-smoke:
	go test -run='^$$' -bench=. -benchtime=1x ./...

fuzz:
	@go list -f '{{.ImportPath}} {{.Dir}}' ./... | while read -r pkg dir; do \
		for t in $$(cd $$dir && go test -list '^Fuzz' . 2>/dev/null | grep '^Fuzz'); do \
			echo "fuzz $$pkg $$t ($(FUZZTIME))"; \
			go test -run='^$$' -fuzz="^$$t\$$" -fuzztime=$(FUZZTIME) $$pkg || exit 1; \
		done; \
	done

fuzz-smoke:
	@$(MAKE) --no-print-directory fuzz FUZZTIME=10s

lint:
	golangci-lint run ./...

vuln:
	govulncheck ./...

check-cmds:
	sh tools/check-cmds.sh

doccheck:
	go run ./tools/doccheck ./...

docs:
	go run ./tools/gendocs

docs-serve: docs
	$(MKDOCS) serve

docs-build: docs
	$(MKDOCS) build --strict

release-check:
	goreleaser check
	goreleaser release --snapshot --clean --skip=sign

check: build lint cover check-cmds doccheck bench-smoke fuzz-smoke vuln docs-build release-check

clean:
	rm -rf bin dist site coverage.out coverage.hand.out bench.txt
