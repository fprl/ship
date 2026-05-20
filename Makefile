.PHONY: test go-test go-build go-vet provisioning-test fake-vps-smoke build build-linux build-darwin build-release clean

GO ?= go
DIST_DIR ?= dist

test: go-test go-build go-vet provisioning-test

go-test:
	$(GO) test ./...

go-build:
	$(GO) build ./...

go-vet:
	$(GO) vet ./...

provisioning-test:
	bash -n install.sh
	bash -n provisioning/install.sh
	provisioning/tests/install_plan_test.sh
	provisioning/tests/bootstrap_tarball_smoke.sh
	if command -v ansible-playbook >/dev/null 2>&1; then ANSIBLE_CONFIG=provisioning/ansible.cfg ansible-playbook --syntax-check -i provisioning/inventory/hosts.ini provisioning/playbooks/vps-bootstrap.yml; else echo "ansible-playbook not found; skipping bootstrap syntax check"; fi
	if command -v ansible-playbook >/dev/null 2>&1; then ANSIBLE_CONFIG=provisioning/ansible.cfg ansible-playbook --syntax-check -i provisioning/inventory/hosts.ini provisioning/playbooks/vps-apply.yml; else echo "ansible-playbook not found; skipping apply syntax check"; fi

fake-vps-smoke:
	tests/fake-vps/smoke.sh

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
