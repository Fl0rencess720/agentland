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

// SandboxPoolSpec defines the desired state of SandboxPool.
type SandboxPoolSpec struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// +optional
	Profile string `json:"profile,omitempty"`

	// +kubebuilder:validation:Required
	Template *SandboxTemplate `json:"sandboxTemplate"`
}

// SandboxPoolStatus defines the observed state of SandboxPool.
type SandboxPoolStatus struct {
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sbxp
// SandboxPool is the Schema for the sandboxpools API.
type SandboxPool struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// +required
	Spec SandboxPoolSpec `json:"spec"`

	// +optional
	Status SandboxPoolStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true
// SandboxPoolList contains a list of SandboxPool.
type SandboxPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SandboxPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SandboxPool{}, &SandboxPoolList{})
}
