//go:build kubeadm_bringup

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package cloudinit renders the per-VM cloud-init template at
// ../cloud-init/user-data.yml.tmpl by substituting __KEY__ placeholders.
//
// We deliberately use the same sed-style placeholder convention as
// tests/smoke/vm/cloud-init/user-data.yml.tmpl (kubeadm Phase 2) and the
// preflight-1vm.sh script — so the template renders identically whether
// it's driven from bash or Go, and the same .tmpl file is reused by both.
package cloudinit

import (
	"fmt"
	"os"
	"strings"
)

// Vars holds the substitution values for one cloud-init render.
//
// The zero value is invalid — Validate() must pass before Render().
// Required fields match the placeholder list in
// cloud-init/user-data.yml.tmpl's header comment.
type Vars struct {
	Hostname             string // __HOSTNAME__
	PasswordHash         string // __PASSWORD_HASH__   (output of `openssl passwd -6 ...`)
	NkeAptRepoURL        string // __NKE_APT_REPO_URL__
	NkeAptGpgURL         string // __NKE_APT_GPG_URL__
	NkeAptSuite          string // __NKE_APT_SUITE__   (nke-noble, NOT noble)
	NkeAptComponent      string // __NKE_APT_COMPONENT__
	NkeKubeadmDeb        string // __NKE_KUBEADM_DEB__         e.g. nke-kubeadm-distro-1.34.2
	NkeKubeadmDebVersion string // __NKE_KUBEADM_DEB_VERSION__ e.g. 5
}

// Validate returns an error listing any required fields that are empty.
func (v Vars) Validate() error {
	var missing []string
	check := func(name, val string) {
		if val == "" {
			missing = append(missing, name)
		}
	}
	check("Hostname", v.Hostname)
	check("PasswordHash", v.PasswordHash)
	check("NkeAptRepoURL", v.NkeAptRepoURL)
	check("NkeAptGpgURL", v.NkeAptGpgURL)
	check("NkeAptSuite", v.NkeAptSuite)
	check("NkeAptComponent", v.NkeAptComponent)
	check("NkeKubeadmDeb", v.NkeKubeadmDeb)
	check("NkeKubeadmDebVersion", v.NkeKubeadmDebVersion)
	if len(missing) > 0 {
		return fmt.Errorf("cloudinit: required Vars fields empty: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Render reads templatePath and returns the cloud-init body with all
// __KEY__ placeholders substituted. The replacer applies values in a single
// pass — substituted values are not themselves re-scanned for placeholders.
func Render(templatePath string, vars Vars) (string, error) {
	if err := vars.Validate(); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("cloudinit: read template %s: %w", templatePath, err)
	}
	replacer := strings.NewReplacer(
		"__HOSTNAME__", vars.Hostname,
		"__PASSWORD_HASH__", vars.PasswordHash,
		"__NKE_APT_REPO_URL__", vars.NkeAptRepoURL,
		"__NKE_APT_GPG_URL__", vars.NkeAptGpgURL,
		"__NKE_APT_SUITE__", vars.NkeAptSuite,
		"__NKE_APT_COMPONENT__", vars.NkeAptComponent,
		"__NKE_KUBEADM_DEB__", vars.NkeKubeadmDeb,
		"__NKE_KUBEADM_DEB_VERSION__", vars.NkeKubeadmDebVersion,
	)
	out := replacer.Replace(string(raw))
	if strings.Contains(out, "__") {
		// Surface any unmapped placeholder rather than silently shipping a
		// VM that won't apt-install. Cheap guardrail; matches the sed-style
		// expectation that all placeholders must resolve.
		for _, line := range strings.Split(out, "\n") {
			if i := strings.Index(line, "__"); i >= 0 {
				j := strings.Index(line[i+2:], "__")
				if j >= 0 {
					return "", fmt.Errorf("cloudinit: unresolved placeholder in rendered template: %q", line[i:i+2+j+2])
				}
			}
		}
	}
	return out, nil
}
