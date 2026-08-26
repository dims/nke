//go:build kubeadm_bringup

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package kubeadm drives a minimal two-VM kubeadm cluster bring-up against a
// locally launched nico-vms, to answer one question: does nico-vms work on a
// GitHub Actions runner?
//
// Scope, and what this deliberately does NOT assert. Success here is that
// kubeadm reached phase=initialize on the control plane and phase=join on the
// worker, both driven over nke-agent's gRPC API. It does NOT install a CNI and
// therefore does NOT claim the cluster is usable — without a CNI both nodes
// stay NotReady and CoreDNS stays Pending, which is expected. That is a real
// limit on the signal, and it buys a much shorter run under emulation. The
// GitLab original (4 VMs, HA etcd, OVN or Calico, and a `kubectl get nodes`
// Ready assertion) remains the fuller test.
//
// Topology is one control plane plus one worker, not the original's three
// control planes. With a single control plane there is no etcd quorum to form
// and no leader election across emulated VMs, which removes the failure mode
// most likely to flap when every guest instruction is being interpreted.
//
// Related: NVIDIA/nke#229 reports that CWE never calls the agent's InstallCNI.
// It does not affect this test on three counts: CWE is not in the path here
// (this driver is the orchestrator), the driver would call InstallCNI itself
// anyway, and CNI is out of scope entirely. Note for whoever adds CNI back:
// our cloud-init runs nke-agent without --bundle-server-url, which is exactly
// the configuration #229 describes, so check whether the agent's chart deploy
// needs a bundle server before assuming it works.
package kubeadm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke/controller/test/kubeadm/internal/cloudinit"
	"github.com/NVIDIA/nke/controller/test/kubeadm/internal/cluster"
	"github.com/NVIDIA/nke/controller/test/kubeadm/internal/nicovms"
)

const (
	// vm-1 is the control plane and supplies the controlPlaneEndpoint;
	// vm-2 is the worker.
	nVMs       = 2
	initNode   = 0
	workerNode = 1
)

func TestKubeadmBringup(t *testing.T) {
	cfg := loadConfig(t)
	requireKubeadmConfig(t, cfg)
	workDir := resolveWorkDir(t, cfg)
	t.Logf("work-dir: %s", workDir)

	// No t.Deadline-derived context: the per-step timeouts below are the real
	// budget, and `go test -timeout` remains the outer backstop.
	//
	// t.Cleanup, not defer: deferred calls run when the test function returns,
	// BEFORE any t.Cleanup callback. Cancelling here with defer would kill the
	// nico-vms subprocess (started with exec.CommandContext) while the cleanup
	// that deletes the instances through its API is still to come, and those
	// deletes would fail with "connection reset by peer". Registering first
	// means it runs last, because cleanups run in reverse order.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	specs := renderCloudInit(t, cfg, workDir)
	proc := launchNicoVMS(ctx, t, cfg, workDir)
	nodes := createNodes(ctx, t, cfg, proc, workDir, specs)
	waitForAgents(ctx, t, cfg, nodes)

	cp, worker := nodes[initNode], nodes[workerNode]
	initAddr := agentAddr(cp.IP, cluster.DefaultAgentPort)
	workerAddr := agentAddr(worker.IP, cluster.DefaultAgentPort)
	cpe := fmt.Sprintf("%s:6443", cp.IP)

	// ── kubeadm init on the control plane ───────────────────────────────────
	t.Logf("ExecuteKubeadmInit on %s (controlPlaneEndpoint=%s)", cp.Name, cpe)
	if err := cluster.ExecuteInit(ctx, initAddr, cluster.InitRequest{
		PodNetworkCIDR:       cfg.podCIDR,
		ServiceCIDR:          cfg.serviceCIDR,
		ControlPlaneEndpoint: cpe,
	}); err != nil {
		initNodeDiag(ctx, t, cp.Name, cp.IP, worker.IP)
		t.Fatalf("ExecuteKubeadmInit on %s: %v", cp.Name, err)
	}
	t.Logf("polling GetKubeadmStatus for phase=initialize (timeout %s)", cfg.initTimeout)
	if err := cluster.WaitForPhase(ctx, initAddr, "initialize", cfg.initTimeout, 5*time.Second); err != nil {
		initNodeDiag(ctx, t, cp.Name, cp.IP, worker.IP)
		t.Fatalf("kubeadm init never reached phase=initialize on %s: %v", cp.Name, err)
	}
	t.Logf("kubeadm init complete on %s", cp.Name)

	// ── join the worker ─────────────────────────────────────────────────────
	// This is the step that actually exercises cross-VM reachability over the
	// nico-vms bridge, and so the one most likely to expose a runner-specific
	// networking problem. With a single control plane there is no CP join and
	// no etcd learner promotion to serialise.
	js, err := cluster.GetJoinStrings(ctx, initAddr)
	if err != nil {
		t.Fatalf("GetJoinStrings from %s: %v", cp.Name, err)
	}
	t.Logf("fetched join strings (cp len=%d, worker len=%d)", len(js.ControlPlane), len(js.Worker))

	t.Logf("ExecuteKubeadmJoin on %s as worker", worker.Name)
	if err := cluster.ExecuteJoin(ctx, workerAddr, js.Worker); err != nil {
		joinFailureDiag(ctx, t, worker.Name, worker.IP, cp.IP)
		initNodeDiag(ctx, t, cp.Name, cp.IP, worker.IP)
		t.Fatalf("ExecuteKubeadmJoin on %s: %v", worker.Name, err)
	}
	t.Logf("polling GetKubeadmStatus on %s for phase=join (timeout %s)", worker.Name, cfg.joinTimeout)
	if err := cluster.WaitForPhase(ctx, workerAddr, "join", cfg.joinTimeout, 5*time.Second); err != nil {
		joinFailureDiag(ctx, t, worker.Name, worker.IP, cp.IP)
		initNodeDiag(ctx, t, cp.Name, cp.IP, worker.IP)
		t.Fatalf("join never reached phase=join on %s: %v", worker.Name, err)
	}
	t.Logf("join complete on %s", worker.Name)

	// ── keep the kubeconfig for post-mortem ─────────────────────────────────
	// Fetched but not used to assert readiness: no CNI is installed, so nodes
	// will report NotReady. It is written out purely so a failed or surprising
	// run can be inspected by hand from the uploaded work dir.
	kc, err := cluster.FetchKubeconfig(ctx, initAddr)
	if err != nil {
		t.Logf("FetchKubeconfig failed (non-fatal, diagnostics only): %v", err)
	} else if werr := os.WriteFile(filepath.Join(workDir, "admin.conf"), []byte(kc), 0o600); werr != nil {
		t.Logf("writing admin.conf failed (non-fatal): %v", werr)
	}

	t.Logf("SUCCESS: kubeadm reached phase=initialize on %s and phase=join on %s", cp.Name, worker.Name)
}

// renderCloudInit builds the per-VM cloud-init bodies and writes a copy of each
// into the work dir for post-mortem.
func renderCloudInit(t *testing.T, cfg config, workDir string) []nicovms.NodeSpec {
	t.Helper()
	specs := make([]nicovms.NodeSpec, nVMs)
	for i := range specs {
		name := fmt.Sprintf("vm-%d", i+1)
		userData, err := cloudinit.Render(cfg.templatePath, cloudinit.Vars{
			Hostname:             name,
			PasswordHash:         cfg.sshPasswordHash,
			NkeAptRepoURL:        cfg.aptRepoURL,
			NkeAptGpgURL:         cfg.aptGpgURL,
			NkeAptSuite:          cfg.aptSuite,
			NkeAptComponent:      cfg.aptComp,
			NkeKubeadmDeb:        cfg.kubeadmDeb,
			NkeKubeadmDebVersion: cfg.kubeadmDebVersion,
		})
		if err != nil {
			t.Fatalf("render cloud-init for %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(workDir, name+"-user-data.yml"), []byte(userData), 0o644); err != nil {
			t.Fatalf("write rendered cloud-init for %s: %v", name, err)
		}
		// PhoneHomeEnabled=false: nico-vms marks a VM ready on an SSH probe
		// rather than a cloud-init phone_home callback. We wait on the agents
		// ourselves below, which is what restores the "all agents up"
		// invariant the rest of the flow assumes.
		specs[i] = nicovms.NodeSpec{Name: name, UserData: userData, PhoneHomeEnabled: false}
	}
	return specs
}

func launchNicoVMS(ctx context.Context, t *testing.T, cfg config, workDir string) *nicovms.Process {
	t.Helper()
	logPath := filepath.Join(workDir, "nico-vms.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("open nico-vms log %s: %v", logPath, err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	t.Logf("launching nico-vms (bin=%s, seed=%s, log=%s)", cfg.nicoVMSBin, cfg.seedPath, logPath)
	proc, err := nicovms.Launch(ctx, nicovms.LaunchOpts{
		BinaryPath:   cfg.nicoVMSBin,
		HTTPAddr:     defaultHTTPAddr,
		DBPath:       filepath.Join(workDir, "nico-vms.db"),
		SeedPath:     cfg.seedPath,
		BridgeName:   cfg.bridgeName,
		BridgeCIDR:   cfg.bridgeCIDR,
		IpxeImageURL: cfg.nodeImageURL,
		Stdout:       logFile,
		Stderr:       logFile,
	})
	if err != nil {
		// The server log is the only place the real reason appears — a bad
		// seed path, a missing bridge or a cgo-less sqlite build all surface
		// there and nowhere else.
		t.Logf("--- tail of nico-vms.log ---\n%s", tailFile(logPath, 100))
		t.Fatalf("launch nico-vms: %v", err)
	}
	t.Cleanup(func() {
		if cfg.keepVMs {
			t.Logf("NKE_BRINGUP_KEEP_VMS set — leaving nico-vms running for post-mortem")
			return
		}
		t.Logf("stopping nico-vms")
		_ = proc.Stop()
	})
	return proc
}

func createNodes(ctx context.Context, t *testing.T, cfg config, proc *nicovms.Process, workDir string, specs []nicovms.NodeSpec) []nicovms.Node {
	t.Helper()
	t.Logf("creating %d VMs concurrently", len(specs))
	nodes, err := proc.CreateNodes(ctx, specs)
	if err != nil {
		t.Fatalf("create nodes: %v", err)
	}
	t.Cleanup(func() {
		if cfg.keepVMs {
			for _, n := range nodes {
				t.Logf("NKE_BRINGUP_KEEP_VMS set — kept VM %s id=%s ip=%s", n.Name, n.InstanceID, n.IP)
			}
			return
		}
		// Deleting an instance removes its directory, and the serial-console
		// logs live in there beside the disk overlay. They are the only record
		// of what cloud-init and kubeadm did inside the VM, so save them before
		// the delete or teardown finds nothing left to collect.
		if t.Failed() {
			saveConsoleLogs(t, workDir, nodes)
		}
		// context.Background deliberately: the test context is cancelled by
		// the time cleanup runs, and the instances still have to be deleted.
		for _, n := range nodes {
			t.Logf("deleting instance %s (%s)", n.Name, n.InstanceID)
			if derr := proc.DeleteNode(context.Background(), n.InstanceID); derr != nil {
				t.Logf("delete %s: %v", n.InstanceID, derr)
			}
		}
	})
	for _, n := range nodes {
		t.Logf("VM reachable (SSH): name=%s id=%s ip=%s", n.Name, n.InstanceID, n.IP)
	}
	if len(nodes) != nVMs {
		t.Fatalf("expected %d nodes, got %d", nVMs, len(nodes))
	}
	return nodes
}

// waitForAgents blocks until every nke-agent answers. An SSH-ready VM has only
// booted; cloud-init may still be installing the kubeadm distro and the agent.
func waitForAgents(ctx context.Context, t *testing.T, cfg config, nodes []nicovms.Node) {
	t.Helper()
	for _, n := range nodes {
		addr := agentAddr(n.IP, cluster.DefaultAgentPort)
		t.Logf("waiting for nke-agent on %s (%s, timeout %s)", n.Name, addr, cfg.initTimeout)
		if err := cluster.WaitForAgent(ctx, addr, cfg.initTimeout, 5*time.Second); err != nil {
			t.Fatalf("nke-agent on %s never came up: %v", n.Name, err)
		}
	}
	t.Logf("all %d nke-agents are up", len(nodes))
}

// tailFile returns the last n lines of a file, best-effort — it only ever
// enriches a failure message.
func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(unreadable: " + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
