//go:build kubeadm_bringup

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package kubeadm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The GitLab original was a CLI (`cluster-bringup full …`) and took these as
// flags. A `go test` binary has no flag surface of its own, so every former
// flag becomes an NKE_BRINGUP_* environment variable with the same default.
//
// The prefix is deliberately NOT NICO_VMS_*: that namespace belongs to the
// nico-vms binary itself, which parses its own environment. Mixing test-driver
// settings into it would make it impossible to tell which process owns a
// given variable.
const (
	// Published production NKE apt repo. Reachable anonymously over plain
	// HTTPS, which matters because CI runners have no route to NVIDIA's
	// internal network.
	defaultAptRepoURL = "https://nke-prod-assets.s3.us-west-2.amazonaws.com/nke-apt-noble"
	defaultAptGpgURL  = "https://nke-prod-assets.s3.us-west-2.amazonaws.com/nke-apt-public.key"
	defaultAptSuite   = "nke-noble" // NOT plain "noble"
	defaultAptComp    = "main"

	// Non-overlapping with nico-vms's own bridge subnet (172.30.0.0/24).
	defaultPodCIDR     = "10.244.0.0/16"
	defaultServiceCIDR = "10.96.0.0/12"

	defaultHTTPAddr = ":18080"

	// Generous relative to the GitLab defaults (15m init, 10m join). CI has
	// no /dev/kvm, so guests run under QEMU TCG emulation at roughly an order
	// of magnitude slower than native; the stock timeouts are calibrated for
	// KVM and are the most likely cause of a spurious red run.
	defaultInitTimeout = 45 * time.Minute
	defaultJoinTimeout = 30 * time.Minute
)

// config is the fully resolved test configuration.
type config struct {
	nicoVMSBin   string
	seedPath     string
	templatePath string

	kubeadmDeb        string
	kubeadmDebVersion string
	sshPasswordHash   string

	aptRepoURL, aptGpgURL, aptSuite, aptComp string

	bridgeName, bridgeCIDR string
	podCIDR, serviceCIDR   string

	nodeImageURL string
	workDir      string

	initTimeout time.Duration
	joinTimeout time.Duration

	keepVMs bool
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("%s=%q is not a valid duration: %v", key, raw, err)
	}
	return d
}

// loadConfig resolves configuration from the environment, failing loudly on
// anything missing. It deliberately does not t.Skip: the kubeadm_bringup build
// tag is already the opt-in, so reaching this function means the caller asked
// for a real two-VM bring-up and a silent skip would be a lie.
func loadConfig(t *testing.T) config {
	t.Helper()

	c := config{
		nicoVMSBin:        os.Getenv("NKE_BRINGUP_NICO_VMS_BIN"),
		kubeadmDeb:        os.Getenv("NKE_BRINGUP_KUBEADM_DEB"),
		kubeadmDebVersion: os.Getenv("NKE_BRINGUP_KUBEADM_VERSION"),
		nodeImageURL:      os.Getenv("NKE_BRINGUP_NODE_IMAGE_URL"),
		workDir:           os.Getenv("NKE_BRINGUP_WORK_DIR"),
		bridgeName:        os.Getenv("NKE_BRINGUP_BRIDGE_NAME"),
		bridgeCIDR:        os.Getenv("NKE_BRINGUP_BRIDGE_CIDR"),
		aptRepoURL:        envOr("NKE_BRINGUP_APT_REPO_URL", defaultAptRepoURL),
		aptGpgURL:         envOr("NKE_BRINGUP_APT_GPG_URL", defaultAptGpgURL),
		aptSuite:          envOr("NKE_BRINGUP_APT_SUITE", defaultAptSuite),
		aptComp:           envOr("NKE_BRINGUP_APT_COMPONENT", defaultAptComp),
		podCIDR:           envOr("NKE_BRINGUP_POD_CIDR", defaultPodCIDR),
		serviceCIDR:       envOr("NKE_BRINGUP_SERVICE_CIDR", defaultServiceCIDR),
		sshPasswordHash: envOr("NKE_BRINGUP_SSH_PASSWORD_HASH",
			"$6$nke-cluster-bringup$KrSrwhFvUJrjMqlVELHsBSx5G1QO8RtNyG8GP6Jrh3.dRgCWQ8GgY79hCsLwlcMz1F0VfeyzL6lJEhwYO5Ay00"),
		initTimeout: envDuration(t, "NKE_BRINGUP_INIT_TIMEOUT", defaultInitTimeout),
		joinTimeout: envDuration(t, "NKE_BRINGUP_JOIN_TIMEOUT", defaultJoinTimeout),
		keepVMs:     os.Getenv("NKE_BRINGUP_KEEP_VMS") != "",
	}

	var missing []string
	if c.nicoVMSBin == "" {
		missing = append(missing, "NKE_BRINGUP_NICO_VMS_BIN (path to the nico-vms binary; build it with `CGO_ENABLED=1 make -C controller/nico-vms build-privileged`)")
	}
	if c.kubeadmDeb == "" {
		missing = append(missing, "NKE_BRINGUP_KUBEADM_DEB (e.g. nke-kubeadm-distro-1.36.3)")
	}
	if c.kubeadmDebVersion == "" {
		missing = append(missing, "NKE_BRINGUP_KUBEADM_VERSION (pinned apt version, e.g. 6)")
	}
	if len(missing) > 0 {
		t.Fatalf("missing required configuration:\n  - %s", strings.Join(missing, "\n  - "))
	}

	abs, err := filepath.Abs(c.nicoVMSBin)
	if err != nil {
		t.Fatalf("resolve NKE_BRINGUP_NICO_VMS_BIN %q: %v", c.nicoVMSBin, err)
	}
	c.nicoVMSBin = abs
	if _, err := os.Stat(c.nicoVMSBin); err != nil {
		t.Fatalf("nico-vms binary not usable at %s: %v", c.nicoVMSBin, err)
	}

	// nico-vms resolves its seed relative to its own working directory, so it
	// must be handed an absolute path or it exits 1. The binary lands at
	// <module-root>/bin/nico-vms, which makes the seed derivable; the override
	// exists for anyone running a binary from somewhere else.
	c.seedPath = os.Getenv("NKE_BRINGUP_SEED_PATH")
	if c.seedPath == "" {
		c.seedPath = filepath.Join(
			filepath.Dir(filepath.Dir(c.nicoVMSBin)),
			"internal", "seed", "default-seed.yaml")
	}
	if c.seedPath, err = filepath.Abs(c.seedPath); err != nil {
		t.Fatalf("resolve seed path: %v", err)
	}
	if _, err := os.Stat(c.seedPath); err != nil {
		t.Fatalf("nico-vms seed not readable at %s: %v\n"+
			"set NKE_BRINGUP_SEED_PATH if the binary is not at <module-root>/bin/nico-vms", c.seedPath, err)
	}

	// A `go test` runs with the package directory as its working directory,
	// so the template is simply alongside us under testdata/.
	c.templatePath = envOr("NKE_BRINGUP_CLOUD_INIT_TEMPLATE",
		filepath.Join("testdata", "user-data.yml.tmpl"))
	if c.templatePath, err = filepath.Abs(c.templatePath); err != nil {
		t.Fatalf("resolve cloud-init template path: %v", err)
	}
	if _, err := os.Stat(c.templatePath); err != nil {
		t.Fatalf("cloud-init template not readable at %s: %v", c.templatePath, err)
	}

	return c
}

// resolveWorkDir returns the directory for the nico-vms db, its log, the
// rendered cloud-init bodies and the fetched kubeconfig. A caller-supplied
// directory is kept on exit so CI can upload it; a temporary one is removed
// unless the run asked for the VMs to be kept.
func resolveWorkDir(t *testing.T, c config) string {
	t.Helper()
	if c.workDir != "" {
		if err := os.MkdirAll(c.workDir, 0o755); err != nil {
			t.Fatalf("create work dir %s: %v", c.workDir, err)
		}
		abs, err := filepath.Abs(c.workDir)
		if err != nil {
			t.Fatalf("resolve work dir: %v", err)
		}
		return abs
	}
	dir, err := os.MkdirTemp("", "nke-kubeadm-bringup-")
	if err != nil {
		t.Fatalf("create temp work dir: %v", err)
	}
	t.Cleanup(func() {
		if c.keepVMs {
			t.Logf("NKE_BRINGUP_KEEP_VMS set — keeping work dir %s", dir)
			return
		}
		_ = os.RemoveAll(dir)
	})
	return dir
}

// agentAddr is the plaintext gRPC endpoint the cloud-init drop-in configures
// nke-agent to serve on.
func agentAddr(ip string, port int) string { return fmt.Sprintf("%s:%d", ip, port) }
