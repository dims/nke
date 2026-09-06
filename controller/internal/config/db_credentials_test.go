// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nke/controller/internal/config"
)

// Validate rejects the compiled-in dev-default database credentials, except in
// the test Forge environment, where they are what a local run is meant to use.
func TestValidateWeakDBCredentials(t *testing.T) {
	t.Parallel()

	base := config.Config{
		MetricsListenAddress:  "127.0.0.1:9090",
		SpireServerAPIAddress: "spire-server.spire-server.svc.cluster.local:8081",
		SpireJoinTokenTTL:     time.Hour,
	}
	const weak = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"

	tests := []struct {
		name                  string
		forgeEnvironment      string
		dbConnectionString    string
		cnpgSuperuserPassword string
		wantErr               bool
	}{
		{
			name:               "production rejects the dev default",
			forgeEnvironment:   "production",
			dbConnectionString: weak,
			wantErr:            true,
		},
		{
			name:                  "production accepts it once a password overrides it",
			forgeEnvironment:      "production",
			dbConnectionString:    weak,
			cnpgSuperuserPassword: "from-vault",
		},
		{
			name:               "production accepts credentials that are not the dev default",
			forgeEnvironment:   "production",
			dbConnectionString: "postgres://cwe:strong@localhost:5432/cwe?sslmode=disable",
		},
		{
			name:               "the test Forge environment accepts the dev default",
			forgeEnvironment:   config.ForgeEnvironmentTest,
			dbConnectionString: weak,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := base
			cfg.ForgeEnvironment = tt.forgeEnvironment
			cfg.DBConnectionString = tt.dbConnectionString
			cfg.CnpgSuperuserPassword = tt.cnpgSuperuserPassword

			err := cfg.Validate()
			if tt.wantErr {
				require.ErrorContains(t, err, "CNPG_SUPERUSER_PASSWORD")
				return
			}
			require.NoError(t, err)
		})
	}
}
