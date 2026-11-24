.PHONY: web build build-linux package test dev

# Node comes from nvm on the owner's machine, so load it first.
web:
	@bash -c 'export NVM_DIR="$$HOME/.nvm"; [ -s "$$NVM_DIR/nvm.sh" ] && . "$$NVM_DIR/nvm.sh"; cd web && { [ -d node_modules ] || npm ci; } && npm run build'

build: web
	go build -tags webui -o bin/ownpaneld ./cmd/ownpaneld

build-linux: web
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags webui -o bin/linux-amd64/ownpaneld ./cmd/ownpaneld

# Release archives for linux/amd64 and linux/arm64 in dist/, exactly as the
# release workflow builds them: make package VERSION=v1.2.3
package:
	scripts/package.sh $(or $(VERSION),dev)

test:
	go vet ./... && go test ./...

dev:
	scripts/dev.sh
