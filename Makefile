.PHONY: test go-test go-build go-vet legacy-test provisioning-test fake-vps-smoke build build-linux clean

GO ?= go
BUN ?= bun
PYTHON ?= python3
DIST_DIR ?= dist

test: go-test go-build go-vet legacy-test provisioning-test

go-test:
	$(GO) test ./...

go-build:
	$(GO) build ./...

go-vet:
	$(GO) vet ./...

legacy-test:
	cd packages/cli && $(BUN) test

provisioning-test:
	bash -n install.sh
	bash -n provisioning/install.sh
	$(PYTHON) -m unittest provisioning/tests/test_simple_vps_cli.py
	provisioning/tests/install_plan_test.sh
	provisioning/tests/bootstrap_tarball_smoke.sh
	if command -v ansible-playbook >/dev/null 2>&1; then ANSIBLE_CONFIG=provisioning/ansible.cfg ansible-playbook --syntax-check -i provisioning/inventory/hosts.ini provisioning/playbooks/vps-bootstrap.yml; else echo "ansible-playbook not found; skipping bootstrap syntax check"; fi
	if command -v ansible-playbook >/dev/null 2>&1; then ANSIBLE_CONFIG=provisioning/ansible.cfg ansible-playbook --syntax-check -i provisioning/inventory/hosts.ini provisioning/playbooks/vps-apply.yml; else echo "ansible-playbook not found; skipping apply syntax check"; fi

fake-vps-smoke:
	packages/cli/tests/fake_vps_smoke.sh

build:
	mkdir -p $(DIST_DIR)
	$(GO) build -trimpath -o $(DIST_DIR)/simple-vps .

build-linux:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/simple-vps-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/simple-vps-linux-arm64 .

clean:
	rm -rf $(DIST_DIR)
