/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// FallbackPolicy controls whether cold-start fallback is allowed.
// +kubebuilder:validation:Enum=AllowColdStart;ForbidColdStart
type FallbackPolicy string

const (
	FallbackPolicyAllowColdStart  FallbackPolicy = "AllowColdStart"
	FallbackPolicyForbidColdStart FallbackPolicy = "ForbidColdStart"
)

// SandboxClaimPhase describes lifecycle phase of claim.
// +kubebuilder:validation:Enum=Pending;Bound;Failed
type SandboxClaimPhase string

const (
	SandboxClaimPhasePending SandboxClaimPhase = "Pending"
	SandboxClaimPhaseBound   SandboxClaimPhase = "Bound"
	SandboxClaimPhaseFailed  SandboxClaimPhase = "Failed"
)

// SandboxClaimSpec defines the desired state of SandboxClaim.
type SandboxClaimSpec struct {
	// +optional
	Profile string `json:"profile,omitempty"`

	// +optional
	PoolRef string `json:"poolRef,omitempty"`

	// +kubebuilder:default=AllowColdStart
	// +optional
	FallbackPolicy FallbackPolicy `json:"fallbackPolicy,omitempty"`

	// +kubebuilder:validation:Required
	Template *SandboxTemplate `json:"sandboxTemplate"`
}

// SandboxClaimStatus defines the observed state of SandboxClaim.
type SandboxClaimStatus struct {
	// +optional
	Phase SandboxClaimPhase `json:"phase,omitempty"`

	// +optional
	SandboxName string `json:"sandboxName,omitempty"`

	// +optional
	Reason string `json:"reason,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sbxc
// SandboxClaim is the Schema for the sandboxclaims API.
type SandboxClaim struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// +required
	Spec SandboxClaimSpec `json:"spec"`

	// +optional
	Status SandboxClaimStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true
// SandboxClaimList contains a list of SandboxClaim.
type SandboxClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SandboxClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SandboxClaim{}, &SandboxClaimList{})
}
