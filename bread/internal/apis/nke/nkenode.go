// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nke

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// NKENode is one worker machine of a NodePool as its tenant sees it: the per-machine record
// break-fix (UC-010) negotiates over. The controller creates one per machine, owned by the pool,
// and deletes it once the machine is released; tenants never create or delete one. Tenants write
// spec and read status. Kind NKENode, not Node, so `kubectl get nodes` keeps meaning the workload
// cluster's nodes.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=nkenodes,shortName=nkeno,scope=Namespaced
// +kubebuilder:selectablefield:JSONPath=`.status.clusterName`
// +kubebuilder:selectablefield:JSONPath=`.status.nodePoolName`
// +kubebuilder:printcolumn:name="POOL",type=string,JSONPath=`.status.nodePoolName`
// +kubebuilder:printcolumn:name="READY",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="HEALTHY",type=string,JSONPath=`.status.conditions[?(@.type=="Healthy")].status`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`
type NKENode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the tenant's intent for this one machine.
	// +kubebuilder:validation:Required
	// +nke:tenant
	Spec NKENodeSpec `json:"spec"`

	// Status is what the controller last observed.
	// +kubebuilder:validation:Optional
	// +nke:tenant
	Status NKENodeStatus `json:"status,omitempty"`
}

// NKENodeList is a list of NKENodes.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type NKENodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NKENode `json:"items"`
}

// NKENodeSpec holds the tenant's verbs on a machine: return it, and (placeholder) accept the
// operator's request to repair it.
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.retire) || (has(self.retire) && self.retire == oldSelf.retire)",message="retire is immutable once set"
type NKENodeSpec struct {
	// Retire is the tenant-initiated return of this machine (UC-010, UC-015: "naming a specific
	// unhealthy node for removal"); immutable once set. NKE cordons the node, drains it under the
	// pool's rollout budget, detaches it and returns it to the provider (NKE-DS9-001); the pool's
	// desired count is unchanged, so the pool reconciler provisions a replacement and deletes this
	// object once Retiring completes. Today this is DELETE /nodes/{id} with a machineHealthIssue.
	// +kubebuilder:validation:Optional
	// +nke:tenant
	Retire *RetireRequest `json:"retire,omitempty"`

	// PLACEHOLDER (UC-010): the tenant's acceptance of the operator's open repair request,
	// naming the request it answers, so a withdrawn and reopened request is never accepted by a
	// stale write. The Azure Scheduled Events shape (approve by EventId); GCE's
	// upcomingMaintenance has one pending window and no id.
	//
	//   AcceptRepair string `json:"acceptRepair,omitempty"`   // == status.repairRequest.id
	//
	// The pool's repairAcceptance: Automatic covers the standing case; the request's acceptBy
	// covers the SRD's "no response in X days". Not implemented in NKE.
}

// RetireRequest is the tenant's reason for returning a machine.
type RetireRequest struct {
	// Reason says why the machine is being returned, for the operator's record; today NKE's
	// machineHealthIssue.summary. The provider's issue category is the machine-provider
	// adapter's to fill in, not the tenant's.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=1024
	// +nke:tenant
	Reason string `json:"reason,omitempty"`
}

// NKENodeStatus carries the machine's place (cluster and pool), its labels and conditions.
type NKENodeStatus struct {
	// ObservedGeneration is the metadata.generation this status reflects.
	// +kubebuilder:validation:Optional
	// +nke:tenant
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ClusterName is the metadata.name of the NKECluster the machine belongs to, denormalised
	// from the owning pool; a selectable field.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=55
	// +nke:tenant
	ClusterName string `json:"clusterName,omitempty"`

	// NodePoolName is the metadata.name of the owning NodePool; a selectable field.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=55
	// +nke:tenant
	NodePoolName string `json:"nodePoolName,omitempty"`

	// Labels are the labels observed on the workload cluster's node: the pool template's and the
	// platform's topology labels (NKE-DS9-020).
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxProperties=64
	// +nke:tenant
	Labels map[string]string `json:"labels,omitempty"`

	// PLACEHOLDER (UC-010): the operator's open request to take the machine for repair. Written
	// by the operator (operator view), read by the tenant, answered by spec.acceptRepair.
	//
	//   RepairRequest *RepairRequest `json:"repairRequest,omitempty"`
	//   // RepairRequest {id string; requestedAt Time; reason string; acceptBy *Time;
	//   //                acceptedBy Tenant|Policy|Deadline; acceptedAt *Time}
	//
	// Lifecycle: open, then accepted, then Retiring, then deleted; or withdrawn, when the operator
	// clears the block. Not implemented in NKE.

	// Conditions are Ready (the node is Ready in the workload cluster), Healthy (the machine
	// provider reports the machine healthy and it is not marked for repair) and Retiring (True
	// from a retire until the machine is released and this object deleted: reason Pending while
	// the pool holds the release for its rollout budget, Releasing once the pool clears it; False
	// with reason None).
	//
	// PLACEHOLDER conditions, added with their features: RepairRequested (UC-010; reasons
	// AwaitingTenant, Accepted, None, Withdrawn) and the readiness validation conditions
	// Validated, NetworkValidated, AcceleratorValidated (NKE-DS9-012; see validation.go).
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +nke:tenant
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Internal is controller-only state neither role sees.
	// +kubebuilder:validation:Optional
	Internal NKENodeInternalState `json:"internal,omitempty"`
}

// NKENodeInternalState is the controller-only state of a machine. Provider identity (machine and
// instance ids, addresses) is not copied here; the CAPI Machine this object owns carries it.
type NKENodeInternalState struct {
	// BoundPoolUID is the uid of the NodePool the machine was created for; a different uid under
	// the same name is a replaced pool, which never adopts the machine.
	// +kubebuilder:validation:Optional
	BoundPoolUID types.UID `json:"boundPoolUID,omitempty"`

	// Replaces is the metadata.name of the retired NKENode this one was provisioned to replace.
	// +kubebuilder:validation:Optional
	Replaces string `json:"replaces,omitempty"`

	// ReplacedBy is the metadata.name of the NKENode provisioned to replace this one; set while
	// this one is Retiring, so pool-level replacement knows the retirement is complete.
	// +kubebuilder:validation:Optional
	ReplacedBy string `json:"replacedBy,omitempty"`

	// ObservedReady is whether the node is Ready in the workload cluster.
	// +kubebuilder:validation:Optional
	ObservedReady bool `json:"observedReady,omitempty"`

	// ObservedHealthy is whether the machine provider reports the machine healthy.
	// +kubebuilder:validation:Optional
	ObservedHealthy bool `json:"observedHealthy,omitempty"`

	// ObservedLabels are the labels observed on the workload node; the reconciler projects them
	// to status.labels.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxProperties=64
	ObservedLabels map[string]string `json:"observedLabels,omitempty"`
}
