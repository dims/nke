// SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nke/controller/api"
)

func TestOpenAPIErrorResponseContract(t *testing.T) {
	const (
		conflictResponseRef            = "#/components/responses/Conflict"
		internalServerErrorResponseRef = "#/components/responses/InternalServerError"
		genericErrorSchemaRef          = "#/components/schemas/GenericError"
	)

	spec, err := api.GetSwagger()
	require.NoError(t, err)

	for _, name := range []string{"Conflict", "InternalServerError"} {
		response := spec.Components.Responses[name]
		require.NotNil(t, response, "missing reusable %s response", name)
		require.NotNil(t, response.Value, "unresolved reusable %s response", name)

		mediaType := response.Value.Content.Get("application/json")
		require.NotNil(t, mediaType, "%s response must use application/json", name)
		require.NotNil(t, mediaType.Schema, "%s response must declare a schema", name)
		assert.Equal(t, genericErrorSchemaRef, mediaType.Schema.Ref)
	}

	var conflictOperations []string
	for path, pathItem := range spec.Paths.Map() {
		for method, operation := range pathItem.Operations() {
			operationName := method + " " + path

			internalServerError := operation.Responses.Value("500")
			require.NotNil(t, internalServerError, "%s must declare a 500 response", operationName)
			assert.Equal(t, internalServerErrorResponseRef, internalServerError.Ref)

			if conflict := operation.Responses.Value("409"); conflict != nil {
				assert.Equal(t, conflictResponseRef, conflict.Ref)
				conflictOperations = append(conflictOperations, operationName)
			}
		}
	}

	assert.ElementsMatch(t, []string{
		"POST /clusters",
		"PATCH /clusters/{clusterID}",
		"DELETE /clusters/{clusterID}",
		"POST /nodePools",
		"PATCH /nodePools/{nodePoolID}",
		"DELETE /nodePools/{nodePoolID}",
		"POST /sites",
		"POST /vipAllocations",
	}, conflictOperations)
}
