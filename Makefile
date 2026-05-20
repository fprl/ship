.PHONY: test go-test go-build go-vet legacy-test build build-linux clean

GO ?= go
BUN ?= bun
PYTHON ?= python3
DIST_DIR ?= dist

test: go-test go-build go-vet legacy-test

go-test:
	$(GO) test ./...

go-build:
	$(GO) build ./...

go-vet:
	$(GO) vet ./...

legacy-test:
	cd packages/cli && $(BUN) test
	$(PYTHON) -m unittest packages/simple-vps/tests/test_simple_vps_cli.py
	packages/simple-vps/tests/install_plan_test.sh

build:
	mkdir -p $(DIST_DIR)
	$(GO) build -trimpath -o $(DIST_DIR)/simple-vps .

build-linux:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/simple-vps-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/simple-vps-linux-arm64 .

clean:
	rm -rf $(DIST_DIR)
