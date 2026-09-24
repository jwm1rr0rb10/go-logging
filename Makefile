NAME=go-logging

.PHONY: tidy fmt vet lint test bench cover tags

tidy:
	go mod tidy

fmt:
	gofmt -s -w .

vet:
	go vet ./...

lint:
	golangci-lint run ./...

test:
	go test -race -count=1 ./...

bench:
	go test -run=^$$ -bench=. -benchmem ./...

cover:
	go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

tags: test
	@bash -c ' \
		version=$$(cat "$(CURDIR)/version" 2>/dev/null || echo "0.0.0") && \
		tag=v$$version && \
		echo "→ tag: $$tag" && \
		if [[ ! $$(git tag -l "$$tag") ]]; then \
			git tag -a "$$tag" -m "Release $$version" && \
			git push origin "$$tag" -o ci.skip && \
			echo "✅ Tagged and pushed $$tag"; \
		else \
			echo "⚠️  Tag $$tag already exists"; \
		fi \
	'
