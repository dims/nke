// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package site

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/controller"
)

func TestDetectLayout(t *testing.T) {
	for name, tc := range map[string]struct {
		files   []string
		want    Layout
		wantErr bool
	}{
		"deploy":  {files: []string{"deploy/nke-deploy.sh"}, want: LayoutDeploy},
		"build":   {files: []string{"build/use-nke-sbom.sh"}, want: LayoutBuild},
		"both":    {files: []string{"deploy/nke-deploy.sh", "build/use-nke-sbom.sh"}, want: LayoutDeploy},
		"neither": {files: []string{"README.md"}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init", "-q")
			for _, f := range tc.files {
				writeFile(t, filepath.Join(repo, filepath.FromSlash(f)), "#")
			}
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-q", "-m", "layout")
			got, err := DetectLayout(context.Background(), repo, "HEAD")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestLayoutInstall(t *testing.T) {
	for name, tc := range map[string]struct {
		layout     Layout
		deployPath string
		want       string
	}{
		"deploy": {
			layout:     LayoutDeploy,
			deployPath: "deploy/nico",
			want:       "bash " + shellQuote(assetsDir+"/deploy/nke-deploy.sh") + " install --deploy-path " + shellQuote(assetsDir+"/deploy/nico"),
		},
		"build": {
			layout:     LayoutBuild,
			deployPath: "controller/deploy/nke-sbom",
			want:       "bash " + shellQuote(assetsDir+"/build/use-nke-sbom.sh") + " install --sbom " + shellQuote(assetsDir+"/controller/deploy") + " --target " + shellQuote("nke-sbom"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.layout.install(tc.deployPath); got != tc.want {
				t.Errorf("install = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCheckControllerNameFollowsTheDeployPath(t *testing.T) {
	const deployed = "cloud-workflow-engine"
	for name, tc := range map[string]struct {
		layout  Layout
		pinned  string
		wantErr bool
	}{
		"unset adopts the deploy path's name": {layout: LayoutDeploy},
		"legacy layout reads its own release": {layout: LayoutBuild},
		"pinned to the same name passes":      {layout: LayoutDeploy, pinned: deployed},
		"pinned to another name fails":        {layout: LayoutDeploy, pinned: "nke-controller", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			assets := t.TempDir()
			writeFile(t, filepath.Join(assets, filepath.FromSlash(tc.layout.releasePath())), "release:\n  namespace: "+deployed+"\n")
			for _, env := range []string{controller.EnvNamespace, controller.EnvService, controller.LegacyEnvNamespace, controller.LegacyEnvService} {
				t.Setenv(env, "")
			}
			if tc.pinned != "" {
				t.Setenv(controller.EnvNamespace, tc.pinned)
				t.Setenv(controller.EnvService, tc.pinned)
			}
			err := CheckControllerName(assets, tc.layout)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), deployed) || !strings.Contains(err.Error(), tc.pinned) {
					t.Fatalf("err = %v, want both names", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if controller.Namespace() != deployed || controller.Service() != deployed {
				t.Errorf("the suite looks for %s/%s, want %s", controller.Namespace(), controller.Service(), deployed)
			}
		})
	}
}
