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

// AgentRuntimeSpec defines the desired state of AgentRuntime.
type AgentRuntimeSpec struct {
	// +optional
	Ports []Port `json:"ports,omitempty"`

	// +kubebuilder:validation:Required
	Template *SandboxTemplate `json:"sandboxTemplate"`

	// +optional
	Provisioning *ProvisioningSpec `json:"provisioning,omitempty"`
}

// AgentRuntimeStatus defines the observed state of AgentRuntime.
type AgentRuntimeStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=agrt

// AgentRuntime is the Schema for the agentruntimes API.
type AgentRuntime struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// +required
	Spec AgentRuntimeSpec `json:"spec"`

	// +optional
	Status AgentRuntimeStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// AgentRuntimeList contains a list of AgentRuntime.
type AgentRuntimeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRuntime `json:"items"`
}

// AgentSessionSpec defines the desired state of AgentSession
type AgentSessionSpec struct {
	// +optional
	Ports []Port `json:"ports,omitempty"`

	// +optional
	Template *SandboxTemplate `json:"sandboxTemplate,omitempty"`

	// +optional
	RuntimeRef *RuntimeReference `json:"runtimeRef,omitempty"`

	// +kubebuilder:default="15m"
	// +optional
	SessionTimeout *metav1.Duration `json:"sessionTimeout,omitempty"`

	// +kubebuilder:default="2h"
	// +optional
	MaxSessionDuration *metav1.Duration `json:"maxSessionDuration,omitempty"`

	// +optional
	Provisioning *ProvisioningSpec `json:"provisioning,omitempty"`
}

type RuntimeReference struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// AgentSessionStatus defines the observed state of AgentSession.
type AgentSessionStatus struct {
	// conditions represent the current state of the AgentSession resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	PodIP string `json:"podIP,omitempty"`

	// +optional
	Phase string `json:"phase,omitempty"`

	// +optional
	ClaimName string `json:"claimName,omitempty"`

	// +optional
	SandboxName string `json:"sandboxName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ags

// AgentSession is the Schema for the agentsessions API
type AgentSession struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of AgentSession
	// +required
	Spec AgentSessionSpec `json:"spec"`

	// status defines the observed state of AgentSession
	// +optional
	Status AgentSessionStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// AgentSessionList contains a list of AgentSession
type AgentSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentSession `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRuntime{}, &AgentRuntimeList{}, &AgentSession{}, &AgentSessionList{})
}
