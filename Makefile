.PHONY: test go-test go-build go-vet shell-test fake-vps-smoke fake-vps-install-smoke init-template-builds build build-linux build-darwin checksum build-release release-smoke example-matrix-smoke clean

GO ?= go
DIST_DIR ?= dist
VERSION ?= $(shell git describe --tags --always --dirty)
VERSION_LDFLAGS := -X github.com/fprl/simple-vps/internal/version.Version=$(VERSION)
SHELL_SCRIPTS := \
	install.sh \
	scripts/install-smoke.sh \
	scripts/example-matrix-smoke.sh \
	scripts/release-smoke.sh
FAKE_VPS_SHELL_SCRIPTS := \
	tests/fake-vps/fake-caddy \
	tests/fake-vps/fake-install-apt-get \
	tests/fake-vps/fake-install-curl \
	tests/fake-vps/fake-install-dpkg-query \
	tests/fake-vps/fake-install-gpg \
	tests/fake-vps/fake-install-localectl \
	tests/fake-vps/fake-install-systemctl \
	tests/fake-vps/fake-install-timedatectl \
	tests/fake-vps/fake-install-ufw \
	tests/fake-vps/fake-podman \
	tests/fake-vps/fake-systemctl

test: go-test go-build go-vet shell-test

go-test:
	$(GO) test ./...

go-build:
	$(GO) build ./...

go-vet:
	$(GO) vet ./...

shell-test:
	for script in $(SHELL_SCRIPTS); do bash -n $$script; done
	for script in $(FAKE_VPS_SHELL_SCRIPTS); do bash -n $$script; done
	bash scripts/install-smoke.sh

fake-vps-smoke:
	SHIP_RUN_FAKE_VPS_SMOKE=1 $(GO) test ./tests/fake-vps -run TestContainerSmoke -count=1 -timeout 20m

fake-vps-install-smoke:
	rm -rf $(DIST_DIR) # ensure host install smoke builds fresh helper binaries
	SHIP_RUN_FAKE_VPS_SMOKE=1 $(GO) test ./tests/fake-vps -run TestFreshHostInstall -count=1 -timeout 20m

init-template-builds:
	SHIP_TEST_INIT_BUILDS=1 $(GO) test ./cmd/client -run TestRunInitGeneratedContainerTemplatesBuildWhenRequested -count=1 -timeout 20m

build:
	mkdir -p $(DIST_DIR)
	$(GO) build -trimpath -ldflags="$(VERSION_LDFLAGS)" -o $(DIST_DIR)/ship .

build-linux:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w $(VERSION_LDFLAGS)" -o $(DIST_DIR)/ship-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w $(VERSION_LDFLAGS)" -o $(DIST_DIR)/ship-linux-arm64 .

build-darwin:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w $(VERSION_LDFLAGS)" -o $(DIST_DIR)/ship-darwin-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w $(VERSION_LDFLAGS)" -o $(DIST_DIR)/ship-darwin-arm64 .

checksum:
	cd $(DIST_DIR) && if command -v sha256sum >/dev/null 2>&1; then sha256sum ship-* > SHA256SUMS; else shasum -a 256 ship-* > SHA256SUMS; fi

build-release: build-linux build-darwin checksum

release-smoke:
	scripts/release-smoke.sh --version $(VERSION) --host $(HOST)

example-matrix-smoke:
	scripts/example-matrix-smoke.sh --host $(HOST)

clean:
	rm -rf $(DIST_DIR)
