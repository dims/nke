// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	pb "github.com/NVIDIA/nke/agent/pkg/agent/proto"
	"github.com/NVIDIA/nke/cli/internal/inventory"
)

// Status values the agent reports, lower-cased before comparison. These mirror
// the constants in agent/internal/agent/agent.go; they travel as strings on the
// wire, so they are restated here rather than imported from an internal package.
const (
	statusReady   = "ready"
	statusError   = "error"
	phaseInit     = "initialize"
	phaseJoin     = "join"
	cniReadyState = "ready"
	cniErrorState = "error"
)

// Timings for the two waits. kubeadm init is asynchronous on the agent: the RPC
// returns as soon as the work is queued, so progress is observed by polling.
const (
	pollInterval = 10 * time.Second
	initTimeout  = 20 * time.Minute
	joinTimeout  = 15 * time.Minute
)

// Options tunes a bootstrap run.
type Options struct {
	// Logger receives step-by-step progress. Required.
	Logger *slog.Logger
}

// Run bootstraps the cluster described by inv.
//
// The sequence is init the first control-plane node, read the join command from
// it, then join the rest. Each step is verified against the agent before the
// next begins, so a failure names the node and the phase it failed in.
//
// There is no separate preflight step. The agent's ExecuteNodePreflight either
// runs caller-supplied file checks, which the inventory does not describe, or
// runs "kubeadm phase preflight" against a rendered kubeadm config, which is the
// renderer this command deliberately avoids. kubeadm runs its own preflight
// checks at the start of init and join, on each node, before it changes that
// node, so the same guarantee holds.
func Run(ctx context.Context, inv *inventory.Inventory, opts Options) error {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	port := inv.Cluster.NodeAgentPort
	if port == "" {
		port = "50051"
	}

	// Connect to every node before changing anything. A typo in an IP should
	// stop the run while it is still a no-op.
	initConn, joinConns, err := dialAll(ctx, inv, port, log)
	if err != nil {
		return err
	}
	defer func() {
		initConn.Close()
		for _, c := range joinConns {
			c.Close()
		}
	}()

	if err := initControlPlane(ctx, initConn, inv, log); err != nil {
		return err
	}

	if len(joinConns) == 0 {
		log.Info("single-node cluster, nothing to join", "cluster", inv.ClusterName)
		return nil
	}

	joinCmd, err := controlPlaneJoinCommand(ctx, initConn, log)
	if err != nil {
		return err
	}

	for _, c := range joinConns {
		if err := joinControlPlane(ctx, c, joinCmd, log); err != nil {
			return err
		}
	}

	log.Info("bootstrap complete", "cluster", inv.ClusterName, "nodes", len(joinConns)+1)
	return nil
}

// dialAll connects to the init node and every join node.
func dialAll(ctx context.Context, inv *inventory.Inventory, port string, log *slog.Logger) (*agentConn, []*agentConn, error) {
	addr := net.JoinHostPort(inv.Cluster.InitNode.IP, port)
	log.Info("connecting to node agent", "node", inv.Cluster.InitNode.Name, "addr", addr)
	initConn, err := dialAgent(ctx, inv.Cluster.InitNode.Name, addr)
	if err != nil {
		return nil, nil, err
	}

	joins := make([]*agentConn, 0, len(inv.Cluster.JoinNodes))
	for _, n := range inv.Cluster.JoinNodes {
		a := net.JoinHostPort(n.IP, port)
		log.Info("connecting to node agent", "node", n.Name, "addr", a)
		c, err := dialAgent(ctx, n.Name, a)
		if err != nil {
			initConn.Close()
			for _, done := range joins {
				done.Close()
			}
			return nil, nil, err
		}
		joins = append(joins, c)
	}
	return initConn, joins, nil
}

// initControlPlane runs kubeadm init on the first control-plane node and waits
// for both kubeadm and the CNI to report ready.
func initControlPlane(ctx context.Context, c *agentConn, inv *inventory.Inventory, log *slog.Logger) error {
	// The agent builds the kubeadm command line itself when init_config is
	// empty (agent/internal/agent/agent.go, "no init config provided"). That
	// keeps this command free of the controller's kubeadm config renderer,
	// which lives behind an internal package this module cannot import.
	req := &pb.KubeadmInitRequest{
		PodNetworkCidr:       inv.Cluster.PodCidr,
		ServiceCidr:          inv.Cluster.ServiceCidr,
		ControlPlaneEndpoint: inv.Cluster.InitNode.IP,
		KubernetesVersion:    inv.Cluster.K8sVersion,
	}

	switch strings.ToLower(inv.Cluster.CniName) {
	case "calico":
		req.CniPluginName = "calico"               //nolint:staticcheck // in-init CNI still reads this field
		req.TigeraVersion = inv.Cluster.CniVersion //nolint:staticcheck // ditto
	case "ovn-kubernetes":
		req.UseOvnK8SCni = true                 //nolint:staticcheck // ditto
		req.CniPluginName = "ovn"               //nolint:staticcheck // ditto
		req.OvnVersion = inv.Cluster.CniVersion //nolint:staticcheck // ditto
	default:
		return fmt.Errorf("unsupported cniName %q", inv.Cluster.CniName)
	}

	log.Info("running kubeadm init",
		"node", c.name,
		"endpoint", req.ControlPlaneEndpoint,
		"podCidr", req.PodNetworkCidr,
		"serviceCidr", req.ServiceCidr,
		"cni", inv.Cluster.CniName)

	// The agent reports a rejected request in the response body rather than as a
	// gRPC error, so a nil err alone does not mean the init was accepted.
	resp, err := c.client.ExecuteKubeadmInit(ctx, req)
	if err != nil {
		return fmt.Errorf("node %s: kubeadm init: %w", c.name, err)
	}
	if strings.EqualFold(resp.GetStatus(), statusError) {
		return fmt.Errorf("node %s: agent rejected kubeadm init: %s", c.name, resp.GetErrorMessage())
	}

	return waitForKubeadm(ctx, c, phaseInit, true, initTimeout, log)
}

// controlPlaneJoinCommand reads the control-plane join command from the init
// node. The agent returns a worker command as well; every node is a control
// plane node in M0, so only the control-plane one is used.
func controlPlaneJoinCommand(ctx context.Context, c *agentConn, log *slog.Logger) (string, error) {
	resp, err := c.client.GetKubeadmJoinString(ctx, &pb.KubeadmJoinStringRequest{})
	if err != nil {
		return "", fmt.Errorf("node %s: read join command: %w", c.name, err)
	}
	cmd := strings.TrimSpace(resp.GetControlplaneJoinString())
	if cmd == "" {
		return "", fmt.Errorf("node %s: agent returned an empty control-plane join command", c.name)
	}
	log.Info("read control-plane join command", "node", c.name)
	return cmd, nil
}

// joinControlPlane joins one node and waits for it to report ready.
func joinControlPlane(ctx context.Context, c *agentConn, joinCmd string, log *slog.Logger) error {
	log.Info("running kubeadm join", "node", c.name)
	resp, err := c.client.ExecuteKubeadmJoin(ctx, &pb.KubeadmJoinRequest{
		KubeadmJoinString: joinCmd,
	})
	if err != nil {
		return fmt.Errorf("node %s: kubeadm join: %w", c.name, err)
	}
	if strings.EqualFold(resp.GetStatus(), statusError) {
		return fmt.Errorf("node %s: agent rejected kubeadm join: %s", c.name, resp.GetErrorMessage())
	}
	return waitForKubeadm(ctx, c, phaseJoin, false, joinTimeout, log)
}

// waitForKubeadm polls the agent until it reports the wanted phase ready, or
// reports an error, or the timeout expires.
//
// requireCNI is set for the init node only: the CNI is installed once, during
// init, so a joining node never reports a CNI status of its own.
func waitForKubeadm(ctx context.Context, c *agentConn, wantPhase string, requireCNI bool, timeout time.Duration, log *slog.Logger) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		resp, err := c.client.GetKubeadmStatus(ctx, &pb.KubeadmStatusRequest{})
		if err != nil {
			return fmt.Errorf("node %s: read kubeadm status: %w", c.name, err)
		}

		status := strings.ToLower(resp.GetKubeadmStatus())
		phase := strings.ToLower(resp.GetKubeadmPhase())
		cni := strings.ToLower(resp.GetCniInstallStatus())

		if status == statusError {
			return fmt.Errorf("node %s: kubeadm %s failed: %s", c.name, wantPhase, resp.GetKubeadmCurrentMessage())
		}
		if requireCNI && cni == cniErrorState {
			return fmt.Errorf("node %s: CNI install failed: %s", c.name, resp.GetKubeadmCurrentMessage())
		}
		if status == statusReady && phase == wantPhase && (!requireCNI || cni == cniReadyState) {
			log.Info("node ready", "node", c.name, "phase", wantPhase)
			return nil
		}

		log.Info("waiting", "node", c.name, "phase", phase, "status", status,
			"cni", cni, "message", resp.GetKubeadmCurrentMessage())

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("node %s: timed out after %s waiting for kubeadm %s (last status %q, message %q)",
				c.name, timeout, wantPhase, status, resp.GetKubeadmCurrentMessage())
		case <-tick.C:
		}
	}
}
