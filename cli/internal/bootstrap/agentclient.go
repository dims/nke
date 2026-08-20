// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package bootstrap drives a site bootstrap by talking to the nke-agent on each
// pre-provisioned node.
//
// The agent already exposes every step a bootstrap needs, so nkectl calls it
// directly rather than standing up an intermediate engine. That keeps the
// bootstrap dependency-free: no cluster, no chart, no registry credential, and
// nothing to tear down afterwards.
package bootstrap

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/NVIDIA/nke/agent/pkg/agent/proto"
)

// dialTimeout bounds the initial connection to a node agent. A node that is
// powered off or firewalled should be reported quickly and by name, rather than
// hanging the whole run.
const dialTimeout = 30 * time.Second

// agentConn is one node's agent connection.
type agentConn struct {
	name   string
	addr   string
	conn   *grpc.ClientConn
	client pb.NodeAgentClient
}

// dialAgent opens a connection to one node agent.
//
// The transport is insecure. The agent serves SPIFFE mTLS when it has an
// identity, but a node being bootstrapped has no identity yet: SPIRE runs on the
// cluster this command is about to create. Until the agent gains a bootstrap
// credential, plaintext on a provisioning network is the only option, which is
// why the caller is required to say so explicitly.
func dialAgent(ctx context.Context, name, addr string) (*agentConn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("node %s: connect to agent at %s: %w", name, addr, err)
	}

	// grpc.NewClient is lazy, so make one real call to fail here rather than
	// midway through the bootstrap with a less obvious error.
	if _, err := pb.NewNodeAgentClient(conn).GetKubeadmStatus(dialCtx, &pb.KubeadmStatusRequest{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("node %s: agent at %s did not answer: %w", name, addr, err)
	}

	return &agentConn{
		name:   name,
		addr:   addr,
		conn:   conn,
		client: pb.NewNodeAgentClient(conn),
	}, nil
}

func (a *agentConn) Close() error {
	if a == nil || a.conn == nil {
		return nil
	}
	return a.conn.Close()
}
