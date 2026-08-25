//go:build kubeadm_bringup

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package kubeadm

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke/controller/test/kubeadm/internal/nicovms"
)

// TestNicoVMSSmoke answers one question on its own: does nico-vms work on this
// runner? It boots two VMs from the stock Ubuntu cloud image, waits for both to
// reach Ready with an address, and proves they can reach each other across the
// bridge.
//
// It installs NOTHING. No kubeadm, no nke-agent, no NKE apt repo — and that is
// the point. The NKE package repository sits behind a source-IP allowlist that
// these runners may not be on, so a test that needed it could not distinguish
// "nico-vms is broken here" from "we cannot download packages here".
// TestKubeadmBringup covers the kubeadm layer and does depend on that repo.
//
// Passing this establishes: nico-vms starts (seed path, cgo sqlite and config
// are all right), libvirt and QEMU work, the bridge exists and can carry
// traffic, VMs boot a qcow2, and IP assignment works. It does NOT establish
// that any of it is fast — no runner here has /dev/kvm, so this is emulated —
// nor does it exercise the iPXE/UEFI path the NKE FDE images need.
func TestNicoVMSSmoke(t *testing.T) {
	cfg := loadConfig(t)
	workDir := resolveWorkDir(t, cfg)
	t.Logf("work-dir: %s", workDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pubKey := generateSSHKey(t, workDir)
	specs := make([]nicovms.NodeSpec, 2)
	for i := range specs {
		name := fmt.Sprintf("smoke-vm-%d", i+1)
		userData := smokeCloudConfig(name, pubKey)
		if err := os.WriteFile(filepath.Join(workDir, name+"-user-data.yml"), []byte(userData), 0o644); err != nil {
			t.Fatalf("write cloud-init for %s: %v", name, err)
		}
		specs[i] = nicovms.NodeSpec{Name: name, UserData: userData, PhoneHomeEnabled: false}
	}

	proc := launchNicoVMS(ctx, t, cfg, workDir)
	nodes := createNodes(ctx, t, cfg, proc, specs)

	// createNodes already waits for Ready, which nico-vms decides with an SSH
	// probe. Re-assert the properties we actually care about rather than
	// trusting that: two distinct, parseable addresses.
	seen := map[string]string{}
	for _, n := range nodes {
		if n.IP == "" {
			t.Fatalf("%s reached Ready with no address", n.Name)
		}
		if net.ParseIP(n.IP) == nil {
			t.Fatalf("%s has an unparseable address %q", n.Name, n.IP)
		}
		if other, dup := seen[n.IP]; dup {
			t.Fatalf("%s and %s were both given %s", other, n.Name, n.IP)
		}
		seen[n.IP] = n.Name
		t.Logf("%s is up at %s", n.Name, n.IP)
	}

	// Runner to VM. The runner owns the bridge gateway, so this failing points
	// at the bridge or its firewall rules rather than at the guest.
	for _, n := range nodes {
		if err := dialWithRetry(ctx, net.JoinHostPort(n.IP, "22"), 2*time.Minute); err != nil {
			t.Fatalf("runner cannot reach %s on :22: %v", n.Name, err)
		}
		t.Logf("runner -> %s:22 OK", n.Name)
	}

	// VM to VM, which is what a kubeadm join depends on and the part most
	// likely to be broken by a runner's own NAT rules. Driven over ssh(1) from
	// inside the first VM so the traffic genuinely crosses the bridge.
	a, b := nodes[0], nodes[1]
	keyPath := filepath.Join(workDir, "smoke_id_ed25519")
	out, err := sshRun(ctx, keyPath, a.IP, fmt.Sprintf(
		"ping -c3 -W5 %s && timeout 10 bash -c 'echo > /dev/tcp/%s/22' && echo VM_TO_VM_OK", b.IP, b.IP))
	t.Logf("--- %s -> %s ---\n%s", a.Name, b.Name, out)
	if err != nil {
		t.Fatalf("%s could not reach %s across the bridge: %v", a.Name, b.Name, err)
	}
	if !strings.Contains(out, "VM_TO_VM_OK") {
		t.Fatalf("%s -> %s connectivity check did not report success", a.Name, b.Name)
	}

	t.Logf("SUCCESS: nico-vms booted 2 VMs on this runner and they can reach each other")
}

// smokeCloudConfig is the smallest cloud-config that gets a stock Ubuntu image
// to a state nico-vms will call Ready: a user with our key, and sshd. No
// package_update and no packages, so it needs no apt mirror at all — neither
// NKE's nor Ubuntu's — and cannot be slowed or failed by one.
//
// Note there is no phone_home stanza. The full bring-up template posts to
// 169.254.169.254:7777, and setup-ci-host.sh deliberately does not put that
// address on the bridge because it is the runner's own EC2 IMDS. Readiness here
// is nico-vms' SSH probe, which needs no metadata service.
func smokeCloudConfig(hostname, pubKey string) string {
	return fmt.Sprintf(`#cloud-config
hostname: %s
preserve_hostname: false
manage_etc_hosts: true
users:
  - name: nke
    groups: sudo
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - %s
ssh_pwauth: false
`, hostname, pubKey)
}

// generateSSHKey makes a throwaway keypair for this run and returns the public
// half. It shells out to ssh-keygen rather than importing
// golang.org/x/crypto/ssh, which is an indirect dependency of the controller
// module — importing it directly would promote it in go.mod, and keeping this
// suite free of dependency changes is deliberate.
func generateSSHKey(t *testing.T, workDir string) string {
	t.Helper()
	keyPath := filepath.Join(workDir, "smoke_id_ed25519")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-q",
		"-C", "nke-nico-vms-smoke", "-f", keyPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("read generated public key: %v", err)
	}
	return strings.TrimSpace(string(pub))
}

// sshRun executes a command on a VM as the seeded 'nke' user. Host key checking
// is off and no known_hosts is kept: these are ephemeral VMs on a private
// bridge, created seconds earlier by this test, with a key that exists only for
// this run.
func sshRun(ctx context.Context, keyPath, host, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh",
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=15",
		"-o", "BatchMode=yes",
		"nke@"+host, script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// dialWithRetry waits for a TCP endpoint to accept a connection. cloud-init may
// still be finishing when nico-vms reports Ready, so a single dial is racy.
func dialWithRetry(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("timed out after %s: %w", timeout, lastErr)
}
