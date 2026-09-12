// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package nke is the hub (internal) version of the nke API group: the tenant kinds NKECluster,
// NodePool and NKENode, each with every field the controller needs.
//
// # One package, three views
//
// This package is the single source of truth. Each field is visible in one of three views:
//
//   - `+nke:tenant`: on the tenant API. Tenants read and, for spec, write it.
//   - `+nke:operator`: on the operator API only. Operators read and write it; tenants never see
//     it. The operator view is a superset of the tenant view.
//   - unmarked: controller-only. Stored on the internal projection, never served to either role.
//     Every unmarked field lives under status.internal.
//
// A generator emits the tenant and operator types from these markers, the `<Kind>Internal` type
// with every field, and the conversions between them. That generator and its output are a
// separate change; this package is the contract it consumes.
//
// # What is a field and what is a placeholder
//
// A live field is one the SRD (NKE DSX Managed K8s, PLC-L1) calls for and NKE implements today,
// or that the Kubernetes API convention requires (conditions, observedGeneration). Everything
// else the SRD calls for but NKE does not yet implement is a PLACEHOLDER: a commented-out field
// carrying the SRD identifier (UC-nnn, NKE-DS9-nnn). Placeholders record a loosely held shape so
// the name is not invented twice; each is revisited with its implementation and needs no
// discussion before then. There is no autoscaling, no per-node kubelet tuning and no tenant
// taints: none is in the SRD or in NKE.
//
// # How the live fields are actuated today
//
// NKE's current control-plane REST API is not the model for this shape; it is what this layer
// calls until the CAPI-backed path replaces it. The mapping is recorded so the implementer knows
// where each field lands, not so the shape follows it.
//
//	NKECluster.metadata.namespace            the org: Cluster.orgID
//	NKECluster.spec.networking               Cluster.network.podCIDR / serviceCIDR (create-only)
//	NKECluster.status.kubernetesVersion      ControlPlaneStatus.kubernetesVersion
//	NKECluster.status.endpoint               the kube-vip address (Cluster.controlPlaneVIP), port 6443
//	NKECluster.status.certificateAuthorityData
//	                                         the CA the tenantaccess controller publishes today
//	NKECluster.status.conditions Ready       provisioned / provisioning / deleting / controlPlaneUpgrading
//	NodePool.spec.clusterName                NodePool.clusterID
//	NodePool.spec.instanceType               NodePool.instanceTypeID (a name here, a UUID there)
//	NodePool.spec.machineImage.name          NodePool.osProfile
//	NodePool.spec.replicas                   NodePool.desiredNodeCount
//	NodePool.spec.template.metadata.labels   NodePool.nodeLabels
//	NodePool.spec.rollout...maxSurge         NodePool.maxSurge, recycleBudget
//	NodePool.status.internal.observedRevision
//	                                         NodePool.currentNodeSetID
//	NKENode.spec.retire                      DELETE /nodes/{id} with machineHealthIssue.summary = reason
//	NKENode.status.conditions Ready          NodeConditions.kubernetesReady
//	NKENode.status.conditions Healthy        NodeConditions.nodeAgentReady and not markedForRepair
//	NKENode.status.conditions Retiring       NodeConditions.deleting or markedForRecycling
//	NKENode.status.internal.replaces / replacedBy
//	                                         auto-mode node recycling
//
// Cluster inputs NKE takes today that are deliberately not fields here: cniPlugin and cniVersion
// belong to the core-runtime recipe (NKE-DS9-007) and arrive with the Release; etcdEncrypted and
// enableOidc are mandated (NKE-DS9-014, NKE-DS9-003), so they are not toggles; controlPlaneConfig,
// teleportEnabled, sshKeyGroupIDs and the capi* fields are operator-plane (UC-009) or provider
// configuration; siteID, vpcID and controlPlaneVIP are machine-provider concerns one layer down
// (machine-provider requirement 22). Pool inputs not carried: systemReservedMemory,
// kubeReservedMemory, configProfile, diskSelectionStrategy (operator or recipe), and
// infinibandPartitionID / nvLinkLogicalPartitionID (UC-022, NKE-DS9-019, P1 topology). Node
// fields not carried: forgeInstanceID, forgeMachineID, ip and nodeSetID are provider identity
// the CAPI objects hold; this API links to them by owner reference, not by copying them.
//
// The credential the tenant uses against the cluster's API server is not minted by this API.
// Tenants authenticate to the workload cluster with OIDC (UC-014, NKE-DS9-003); status carries
// the endpoint and CA so a client can assemble a kubeconfig, and the issuer details are a
// placeholder until the identity design lands. A credentials subresource is not decided.
//
// +kubebuilder:object:generate=true
// +k8s:deepcopy-gen=package
// +groupName=nke
package nke

// GroupName is the API group this package is the hub version of. It is a dotless group: the
// tenant API server serves it itself and needs no CRD, so the dot rule for CRD groups does not
// apply; the stored shape uses internal.nke.nvidia.com.
const GroupName = "nke"
