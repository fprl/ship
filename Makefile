.PHONY: test go-test go-build go-vet shell-test fake-vps-smoke fake-vps-install-smoke build build-linux build-darwin build-release clean

GO ?= go
DIST_DIR ?= dist
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
	tests/fake-vps/fake-journalctl \
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
	bash -n install.sh
	for script in $(FAKE_VPS_SHELL_SCRIPTS); do bash -n $$script; done

fake-vps-smoke:
	SIMPLE_VPS_RUN_FAKE_VPS_SMOKE=1 $(GO) test ./tests/fake-vps -run TestContainerSmoke -count=1 -timeout 20m

fake-vps-smoke-legacy:
	SIMPLE_VPS_RUN_FAKE_VPS_SMOKE=1 $(GO) test ./tests/fake-vps -run TestSmoke$$ -count=1 -timeout 20m

fake-vps-install-smoke:
	SIMPLE_VPS_RUN_FAKE_VPS_SMOKE=1 $(GO) test ./tests/fake-vps -run TestFreshHostInstall -count=1 -timeout 20m

build:
	mkdir -p $(DIST_DIR)
	$(GO) build -trimpath -o $(DIST_DIR)/simple-vps .

build-linux:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/simple-vps-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/simple-vps-linux-arm64 .

build-darwin:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/simple-vps-darwin-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/simple-vps-darwin-arm64 .

build-release: build-linux build-darwin

clean:
	rm -rf $(DIST_DIR)
