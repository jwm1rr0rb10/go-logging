NAME=go-logging

# Modules of this repository: the library, the gRPC integration and the
# comparison benchmarks (zap, zerolog). Only the first two are released.
MODULES := . grpc benchmarks

# -race needs cgo and a C compiler. Without them (e.g. WSL or a container
# without gcc) tests run without -race and a warning instead of failing.
RACE := $(shell [ "$$(go env CGO_ENABLED)" = "1" ] && command -v "$$(go env CC)" >/dev/null 2>&1 && echo -race)

define race_warning
	@if [ -z "$(RACE)" ]; then \
		echo "⚠️  race detector unavailable (needs cgo + gcc): running without -race"; \
		echo "   fix: sudo apt install build-essential && go env -u CGO_ENABLED"; \
	fi
endef

.PHONY: tidy fmt vet lint test cover bench compare fuzz tags

tidy:
	@for m in $(MODULES); do (cd $$m && go mod tidy) || exit 1; done

fmt:
	gofmt -s -w .

vet:
	@for m in $(MODULES); do (cd $$m && go vet ./...) || exit 1; done

lint:
	@for m in . grpc; do (cd $$m && golangci-lint run ./...) || exit 1; done

test:
	$(race_warning)
	@for m in . grpc; do (cd $$m && go test $(RACE) -count=1 ./...) || exit 1; done

cover:
	$(race_warning)
	go test $(RACE) -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

bench:
	go test -run=^$$ -bench=. -benchmem ./...

# Comparison with zap and zerolog, single core and 8 cores.
compare:
	cd benchmarks && go test -run=^$$ -bench=. -benchmem -cpu=1,8 ./...

# FUZZTIME=5m make fuzz
FUZZTIME ?= 60s
fuzz:
	go test -run=^$$ -fuzz=^FuzzRequestID$$ -fuzztime=$(FUZZTIME) .
	go test -run=^$$ -fuzz=^FuzzParseLevel$$ -fuzztime=$(FUZZTIME) .
	go test -run=^$$ -fuzz=^FuzzAsyncWriter$$ -fuzztime=$(FUZZTIME) .

# Tags the library (vX.Y.Z) and the gRPC module (grpc/vX.Y.Z) with the
# version from ./version. grpc/go.mod must require that library version.
tags: test
	@bash -c ' \
		version=$$(tr -d "[:space:]" < "$(CURDIR)/version" 2>/dev/null || echo "0.0.0") && \
		if ! grep -Eq "go-logging/v2 v$$version( |$$)" grpc/go.mod; then \
			echo "❌ grpc/go.mod must require github.com/jwm1rr0rb10/go-logging/v2 v$$version"; exit 1; \
		fi && \
		for tag in v$$version grpc/v$$version; do \
			echo "→ tag: $$tag"; \
			if [[ ! $$(git tag -l "$$tag") ]]; then \
				git tag -a "$$tag" -m "Release $$tag" && \
				git push origin "$$tag" -o ci.skip && \
				echo "✅ Tagged and pushed $$tag"; \
			else \
				echo "⚠️  Tag $$tag already exists"; \
			fi; \
		done \
	'
