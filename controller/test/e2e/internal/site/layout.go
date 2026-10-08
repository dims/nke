// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package site

import (
	"context"
	"fmt"
	"os/exec"
	"path"
)

// Layout is where a commit keeps the customer deploy path and the script that
// installs it. The path moved from controller/deploy/nke-sbom, installed by
// build/use-nke-sbom.sh, to deploy/nico, installed by deploy/nke-deploy.sh. A
// released controller is qualified with the layout its own commit carries, so
// the suite has to speak both.
type Layout string

const (
	// LayoutDeploy is deploy/nke-deploy.sh installing deploy/nico.
	LayoutDeploy Layout = "deploy"

	// LayoutBuild is build/use-nke-sbom.sh installing controller/deploy/nke-sbom,
	// what controller releases up to v0.1.1 carry.
	LayoutBuild Layout = "build"
)

// DetectLayout is the layout at ref in repo: the one whose installer the
// commit tracks.
func DetectLayout(ctx context.Context, repo, ref string) (Layout, error) {
	for _, l := range []Layout{LayoutDeploy, LayoutBuild} {
		if exec.CommandContext(ctx, "git", "-C", repo, "cat-file", "-e", ref+":"+l.installer()).Run() == nil {
			return l, nil
		}
	}
	return "", fmt.Errorf("neither %s nor %s is tracked at %s in %s, so no deploy path can be installed from it",
		LayoutDeploy.installer(), LayoutBuild.installer(), ref, repo)
}

// installer is the install script, relative to the assets.
func (l Layout) installer() string {
	if l == LayoutBuild {
		return "build/use-nke-sbom.sh"
	}
	return "deploy/nke-deploy.sh"
}

// DefaultDeployPath is the customer deploy path, relative to the assets.
func (l Layout) DefaultDeployPath() string {
	if l == LayoutBuild {
		return "controller/deploy/nke-sbom"
	}
	return "deploy/nico"
}

// AssetPaths are what the Stack deployer runs from: the installer's directory,
// the deploy paths and the charts they install by relative path, the
// post-install script, and the NKTP chart.
func (l Layout) AssetPaths() []string {
	return []string{path.Dir(l.installer()), deployDir, hackDir, nktpChartDir}
}

// releasePath is the HelmRelease the deploy path installs the controller from,
// relative to the assets.
func (l Layout) releasePath() string { return l.DefaultDeployPath() + "/controller/release.yaml" }

// install is the install command for deployPath on the node, without the
// options the two scripts share.
func (l Layout) install(deployPath string) string {
	script := "bash " + shellQuote(path.Join(assetsDir, l.installer())) + " install"
	if l == LayoutBuild {
		return script + " --sbom " + shellQuote(path.Join(assetsDir, path.Dir(deployPath))) +
			" --target " + shellQuote(path.Base(deployPath))
	}
	return script + " --deploy-path " + shellQuote(path.Join(assetsDir, deployPath))
}
