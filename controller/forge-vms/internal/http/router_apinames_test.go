// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: LicenseRef-NvidiaProprietary
//
// NVIDIA CORPORATION, its affiliates and licensors retain all intellectual
// property and proprietary rights in and to this material, related
// documentation and any modifications thereto. Any use, reproduction,
// disclosure or distribution of this material and related documentation
// without an express license agreement from NVIDIA CORPORATION or
// its affiliates is strictly prohibited.

package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"testing"
)

// The v2 routes are registered once per API name. A client on any of the three
// SDKs must reach the same handler, and a name missing from that list produces a
// 404 that no build or lint step can see -- which is how the SDK rename to
// "nico" went unnoticed.
func TestV2RoutesServeEveryAPIName(t *testing.T) {
	mux := nethttp.NewServeMux()
	registerV2 := func(m *nethttp.ServeMux, method, suffix string, h nethttp.HandlerFunc) {
		for _, apiName := range v2APINames {
			m.HandleFunc(method+" /v2/org/{org}/"+apiName+"/"+suffix, h)
		}
	}
	registerV2(mux, "GET", "instance", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
	})

	for _, apiName := range []string{"nico", "carbide", "forge"} {
		path := "/v2/org/testorg/" + apiName + "/instance"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodGet, path, nil))
		if rec.Code != nethttp.StatusOK {
			t.Errorf("%s = %d, want %d", path, rec.Code, nethttp.StatusOK)
		}
	}
}
