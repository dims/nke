// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build gofail

package framework

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"k8s.io/client-go/tools/clientcmd"
)

// kubebuilderAssetsEnv holds the directory of the kube-apiserver and etcd
// binaries envtest runs. `make e2e-gofail` sets it from setup-envtest.
const kubebuilderAssetsEnv = "KUBEBUILDER_ASSETS"

// StartAPIServer runs a local kube-apiserver and etcd, and returns a kubeconfig
// path for them. Both stop when the test ends.
//
// The tested workflows never call the Kubernetes API, so a placeholder address
// used to be enough. It is not any more: the worker exits at startup without a
// reachable API, because NewRuntimeManager configures a per-namespace cache for
// corev1.Secret and a Cache.ByObject entry makes controller-runtime resolve the
// REST mapping eagerly rather than lazily.
//
// This is envtest, not a cluster. It runs two binaries and no kubelet, no
// scheduler and no controller manager.
func StartAPIServer(t testing.TB) string {
	t.Helper()

	if os.Getenv(kubebuilderAssetsEnv) == "" {
		t.Skipf("%s is unset; run `make e2e-gofail`, which resolves it with setup-envtest", kubebuilderAssetsEnv)
	}

	env := &envtest.Environment{}
	cfg, err := env.Start()
	require.NoError(t, err, "starting the envtest API server")
	t.Cleanup(func() {
		require.NoError(t, env.Stop(), "stopping the envtest API server")
	})

	kubeconfig := clientcmdapi.NewConfig()
	kubeconfig.Clusters[gofailContextName] = &clientcmdapi.Cluster{
		Server:                   cfg.Host,
		CertificateAuthorityData: cfg.CAData,
	}
	kubeconfig.AuthInfos[gofailContextName] = &clientcmdapi.AuthInfo{
		ClientCertificateData: cfg.CertData,
		ClientKeyData:         cfg.KeyData,
	}
	kubeconfig.Contexts[gofailContextName] = &clientcmdapi.Context{
		Cluster:  gofailContextName,
		AuthInfo: gofailContextName,
	}
	kubeconfig.CurrentContext = gofailContextName

	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(*kubeconfig, path))
	require.NoError(t, os.Chmod(path, 0o600))

	return path
}

const gofailContextName = "gofail-e2e"
