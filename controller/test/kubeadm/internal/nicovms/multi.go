//go:build kubeadm_bringup

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nicovms

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"
)

// NodeSpec is one element of a batch CreateNodes call.
type NodeSpec struct {
	Name             string // VM hostname (also passed to nico-vms as the instance name)
	UserData         string // rendered cloud-init body (from internal/cloudinit.Render)
	PhoneHomeEnabled bool   // typically true so "ready" == cloud-init done
}

// Node is the resolved per-VM result of a CreateNodes call.
type Node struct {
	Name       string
	InstanceID string
	IP         string
}

// CreateNodes fans the underlying CreateNode call out across all specs
// concurrently. Returns the resolved Nodes in the SAME ORDER as specs so the
// caller can rely on positional indexing (specs[0] → nodes[0] → vm-1 etc.).
//
// On any failure: cancels in-flight peers via the errgroup's context, then
// best-effort deletes whichever nodes did successfully create before
// returning the first error. Callers must NOT rely on partial cleanup —
// they still need to invoke proc.DeleteNode for any IDs they got back from
// a SUCCESSFUL CreateNodes return.
//
// nico-vms's libvirt backend handles parallel provisioning fine — concurrent
// disk overlay creation + virt-install calls don't conflict. ~3-5 min total
// wall-clock for 4 VMs vs ~12-20 min sequentially.
func (p *Process) CreateNodes(ctx context.Context, specs []NodeSpec) ([]Node, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	g, gctx := errgroup.WithContext(ctx)
	results := make([]Node, len(specs))
	// Track successful instance IDs even when a peer fails so we can clean
	// up the orphans. Guarded by mu because errgroup goroutines mutate it.
	var (
		mu      sync.Mutex
		created []string
	)

	for i, spec := range specs {
		i, spec := i, spec // capture
		g.Go(func() error {
			id, ip, err := p.CreateNode(gctx, spec.Name, spec.UserData, spec.PhoneHomeEnabled)
			// CreateNode may return a non-empty id even on error (instance
			// was created but never reached ready) — record it either way
			// so cleanup can target it.
			if id != "" {
				mu.Lock()
				created = append(created, id)
				mu.Unlock()
			}
			if err != nil {
				return fmt.Errorf("create %s: %w", spec.Name, err)
			}
			results[i] = Node{Name: spec.Name, InstanceID: id, IP: ip}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		// Best-effort cleanup of any siblings that did succeed. Use a fresh
		// context so cleanup runs even if the caller's ctx is cancelled.
		var cleanupErrs []error
		for _, id := range created {
			if derr := p.DeleteNode(context.Background(), id); derr != nil {
				cleanupErrs = append(cleanupErrs, derr)
			}
		}
		if len(cleanupErrs) > 0 {
			return nil, fmt.Errorf("CreateNodes failed: %w; cleanup errors: %w",
				err, errors.Join(cleanupErrs...))
		}
		return nil, fmt.Errorf("CreateNodes failed: %w", err)
	}
	return results, nil
}
