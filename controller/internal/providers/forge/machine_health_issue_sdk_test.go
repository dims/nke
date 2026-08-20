// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"encoding/json"
	"testing"

	forgesdk "github.com/NVIDIA/infra-controller/rest-api/sdk/standard"
	"github.com/stretchr/testify/require"
)

// The SDK marks `details` required and emits it unconditionally, so an unset
// value serializes to `"details": null`, which its own decoder rejects. Guard
// the round trip rather than the field, because reading the conversion does not
// reveal the problem.
func TestToForgeSDKMachineHealthIssueRoundTrips(t *testing.T) {
	details := "disk 3 reported uncorrectable errors"
	for name, in := range map[string]*MachineHealthIssue{
		"without details": {Category: hardware, Summary: "Node removed via NKE remediation"},
		"with details":    {Category: hardware, Summary: "Node removed via NKE remediation", Details: &details},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(toForgeSDKMachineHealthIssue(in))
			require.NoError(t, err)
			require.NotContains(t, string(body), `"details":null`)

			var out forgesdk.MachineHealthIssue
			require.NoError(t, json.Unmarshal(body, &out), "the SDK must accept what we send")
		})
	}
}

func TestToForgeSDKMachineHealthIssueNil(t *testing.T) {
	require.Nil(t, toForgeSDKMachineHealthIssue(nil))
}
