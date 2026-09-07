# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

##@ Help

.PHONY: help
help: ## Show the available targets.
	@awk 'BEGIN { FS = ":.*## " } \
		/^##@ / { printf "\n%s\n", substr($$0, 5) } \
		/^[a-z][a-z0-9-]*:.*## / { printf "  %-22s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo

GO ?= go

# CRANE_VERSION and NFPM_VERSION come from make/tool-versions.mk.
include ../../../make/tool-versions.mk

# The ovn-kubernetes chart stream the agent installs by default
# (defaultOvnCniChartStream in agent/internal/agent/cni.go). Bump here and run
# `make lock`; see README.md.
OVN_STREAMS ?= 1.4.0

# Where the .deb files are written. Required by `debs`.
OUT_DIR ?=

# Debian packaging revision. Bump when the package content changes but the
# chart stream does not.
DEB_REVISION ?= 1

DIST_DIR ?= $(CURDIR)/dist
BIN_DIR ?= $(CURDIR)/bin
LOCK_DIR ?= $(CURDIR)/lock

##@ Build

.PHONY: debs
debs: check-host check-deb-tools bundle ## Build the charts and images debs into OUT_DIR, and reindex it.
	@# Clear the previous run first: a .deb left from a different
	@# OVN_STREAMS would be indexed here without any package requiring it.
	rm -f "$(OUT_DIR)"/nke-cni-ovn-*.deb
	@set -eu; \
	for v in $(OVN_STREAMS); do \
		DEB_REVISION="$(DEB_REVISION)" NFPM_BIN="$(BIN_DIR)/nfpm" \
			./packaging/build-deb.sh "$$v" "$(DIST_DIR)/$$v" "$(OUT_DIR)"; \
	done
	@# Rebuild the flat index over everything in OUT_DIR: kubeadm-distro and
	@# cni-calico write the same directory, and whichever builds last owns
	@# the index.
	@cd "$(OUT_DIR)" && dpkg-scanpackages . /dev/null >Packages
	@echo "wrote $(OUT_DIR)/Packages"

.PHONY: bundle
bundle: check-host check-ngc-key $(BIN_DIR)/crane ## Download, verify and stage the chart and images into DIST_DIR.
	@set -eu; \
	for v in $(OVN_STREAMS); do \
		./bundle.sh --version "$$v" --dist "$(DIST_DIR)" --lock-dir "$(LOCK_DIR)" --crane "$(BIN_DIR)/crane"; \
	done

.PHONY: lock
lock: check-host check-ngc-key $(BIN_DIR)/crane ## Regenerate lock/<stream>.lock from NGC. Review the diff, then commit it.
	@set -eu; \
	for v in $(OVN_STREAMS); do \
		./bundle.sh --write-lock --version "$$v" --dist "$(DIST_DIR)" --lock-dir "$(LOCK_DIR)" --crane "$(BIN_DIR)/crane"; \
	done
	@echo
	@echo "Review the lock diff before committing it:"
	@echo "    git diff -- $(LOCK_DIR)"

.PHONY: repro-check
repro-check: ## Build `debs` twice into throwaway OUT_DIRs and diff every .deb byte-for-byte.
	@set -eu; \
	a="$$(mktemp -d)"; b="$$(mktemp -d)"; \
	trap 'rm -rf "$$a" "$$b"' EXIT; \
	$(MAKE) debs OUT_DIR="$$a" OVN_STREAMS="$(OVN_STREAMS)" DEB_REVISION="$(DEB_REVISION)" DIST_DIR="$(DIST_DIR)"; \
	$(MAKE) debs OUT_DIR="$$b" OVN_STREAMS="$(OVN_STREAMS)" DEB_REVISION="$(DEB_REVISION)" DIST_DIR="$(DIST_DIR)"; \
	diffs=""; \
	for f in "$$a"/*.deb; do \
		name="$$(basename "$$f")"; \
		if [ ! -f "$$b/$$name" ] || ! cmp -s "$$f" "$$b/$$name"; then diffs="$$diffs $$name"; fi; \
	done; \
	if [ -n "$$diffs" ]; then \
		echo "cni-ovn: not reproducible, these differ between builds:$$diffs" >&2; exit 1; \
	fi; \
	echo "cni-ovn: reproducible: every .deb matched byte-for-byte"

$(BIN_DIR)/crane: ## Build the pinned crane (go-containerregistry) into ./bin.
	@GOBIN=$(BIN_DIR) $(GO) install github.com/google/go-containerregistry/cmd/crane@v$(CRANE_VERSION)

.PHONY: nfpm
nfpm: $(BIN_DIR)/nfpm ## Build the pinned nfpm (goreleaser) into ./bin.

$(BIN_DIR)/nfpm:
	@GOBIN=$(BIN_DIR) $(GO) install github.com/goreleaser/nfpm/v2/cmd/nfpm@v$(NFPM_VERSION)

##@ Checks

.PHONY: check-ngc-key
check-ngc-key: ## Fail early without the NGC key the chart and image pulls need.
	@if [ -z "$${NGC_API_KEY:-}" ]; then \
		echo "cni-ovn: NGC_API_KEY is required (an NGC key with access to the nv-ngn org)." >&2; \
		exit 1; \
	fi

.PHONY: check-host
check-host: ## Fail early when this host cannot run the bundle step.
	@# The repack pins owner, mode and mtime with GNU tar semantics.
	@if [ "$$(uname -s)" != "Linux" ]; then \
		echo "cni-ovn: the bundle step needs a Linux host; this is $$(uname -s)." >&2; \
		exit 1; \
	fi
	@missing=""; \
	for tool in curl gzip python3 sha256sum tar; do \
		command -v "$$tool" >/dev/null 2>&1 || missing="$$missing $$tool"; \
	done; \
	if [ -n "$$missing" ]; then \
		echo "cni-ovn: missing tools needed to bundle the chart and images:$$missing" >&2; \
		exit 1; \
	fi

.PHONY: check-deb-tools
check-deb-tools: $(BIN_DIR)/nfpm ## Fail early when the Debian packaging tools are missing.
	@set -eu; \
	if [ "$$(uname -s)" != "Linux" ]; then \
		echo "cni-ovn: packages are built on a Linux host only; this is $$(uname -s)." >&2; \
		exit 1; \
	fi; \
	if ! command -v dpkg-scanpackages >/dev/null 2>&1; then \
		echo "cni-ovn: dpkg-scanpackages is required to index OUT_DIR." >&2; \
		echo "  install it with: apt install dpkg-dev" >&2; \
		exit 1; \
	fi
	@case "$(OUT_DIR)" in \
	  /*) ;; \
	  "") echo "cni-ovn: OUT_DIR is required, as an absolute path." >&2; exit 1 ;; \
	  *) echo "cni-ovn: OUT_DIR must be an absolute path, got '$(OUT_DIR)'." >&2; exit 1 ;; \
	esac

.PHONY: clean
clean: ## Remove the built tools and the downloaded artifact tree.
	rm -rf $(BIN_DIR) $(DIST_DIR)
