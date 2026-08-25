//go:build kubeadm_bringup

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cluster

import (
	"context"
	"fmt"
	"time"

	pb "github.com/NVIDIA/nke/agent/pkg/agent/proto"
)

// RunScript executes an inline diagnostic script on the node-agent at addr and
// returns its combined output. Used for on-failure post-mortem over the agent's
// existing gRPC — no SSH, and it runs while the VM is still alive (before the
// driver deletes it), so nothing leaks on the shared runner.
func RunScript(ctx context.Context, addr, name, contents string) (string, error) {
	conn, client, err := Dial(ctx, addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	rpcCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resp, err := client.ExecuteScript(rpcCtx, &pb.ExecuteScriptRequest{
		ScriptName:     name,
		ScriptContents: contents,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[exit=%d status=%s error=%q]\n%s",
		resp.GetExitCode(), resp.GetStatus(), resp.GetErrorMessage(), resp.GetOutput()), nil
}
