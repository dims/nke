# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: LicenseRef-NvidiaProprietary

# Module-level targets, matching the other workspace members. The root Makefile
# builds nkectl into the repository's bin/ as part of `make build`; this file is
# for working inside cli/ alone, and it is what the Lint workflow invokes.
#
# Every dependency here is public, so these targets need neither the workspace
# nor the committed vendor/ tree, and CI runs them with GOWORK=off.

GO ?= go
BINARY := bin/nkectl

# Go tool install location (GOPATH/bin, unless GOBIN is set); matches
# agent/Makefile and controller/forge-vms/Makefile.
GOBIN ?= $(shell $(GO) env GOBIN)
ifeq (,$(GOBIN))
GOBIN = $(shell $(GO) env GOPATH)/bin
endif

GOLANGCI_LINT_REPO    ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.12.2

.PHONY: build test fmt lint license file-license-check

build:
	$(GO) build -o $(BINARY) ./cmd/nkectl

test:
	$(GO) test ./...

# golangci-lint applies the formatters enabled in .golangci.yml (gci, gofumpt),
# so there is no separate gofmt step here.
fmt:
	$(GO) install $(GOLANGCI_LINT_REPO)@$(GOLANGCI_LINT_VERSION)
	$(GOBIN)/golangci-lint fmt ./...

# One recipe for CI and local, pinned by version, as in the sibling modules.
lint:
	$(GO) install $(GOLANGCI_LINT_REPO)@$(GOLANGCI_LINT_VERSION)
	$(GOBIN)/golangci-lint run --timeout 10m --max-same-issues 0 --max-issues-per-linter 0 ./...

license: ## Ensure license headers at code.
	$(GO) run github.com/google/addlicense@v1.1.1 -f ./hack/boilerplate.addlicense.txt .

# The name matters beyond this file. license.yml builds its header-check matrix
# by selecting the modules whose Makefile defines a `file-license-check:`
# target, so a module without one is silently absent from that matrix rather
# than reported as unchecked. Expects a clean working directory.
file-license-check: license ## Verify all files have license headers.
	@if ! git diff --exit-code -- . > /dev/null 2>&1; then \
		echo "License headers are missing or outdated. Please run 'make license' locally and commit the changes."; \
		echo "Files with missing/outdated license headers:"; \
		git diff --name-only -- .; \
		exit 1; \
	fi
