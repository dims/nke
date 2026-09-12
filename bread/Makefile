# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

GO ?= go
GOBIN ?= $(shell $(GO) env GOBIN)
ifeq (,$(GOBIN))
GOBIN = $(shell $(GO) env GOPATH)/bin
endif

# Lint this module on its own, not through the workspace; see make/golangci.mk.
GOLANGCI_LINT_GOWORK :=
include ../make/golangci.mk

GO_TEST_FLAGS ?=

.PHONY: help lint test

help: ## Show the available targets.
	@awk 'BEGIN { FS = ":.*## " } \
		/^##@ / { printf "\n%s\n", substr($$0, 5) } \
		/^[a-z][a-z0-9-]*:.*## / { printf "  %-22s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo

lint: ## Run golangci-lint over this module. (run by the merge gate)
	@$(GOLANGCI_LINT_RUN) run --timeout 10m

test: ## Run this module's tests.
	$(GO) test $(GO_TEST_FLAGS) ./...
