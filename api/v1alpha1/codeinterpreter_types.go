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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Port struct {
	Port uint32 `json:"port"`
}

type CodeInterpreterSandboxTemplate struct {
	// +kubebuilder:validation:Required
	Image string `json:"image"`
	// +optional
	Command []string `json:"command,omitempty"`
	// +optional
	Args []string `json:"args,omitempty"`
}

// CodeInterpreterSpec defines the desired state of CodeInterpreter
type CodeInterpreterSpec struct {
	// +optional
	Ports []Port `json:"ports,omitempty"`

	// +kubebuilder:validation:Required
	Template *CodeInterpreterSandboxTemplate `json:"sandboxTemplate"`

	// +kubebuilder:default="15m"
	// +optional
	SessionTimeout *metav1.Duration `json:"sessionTimeout,omitempty"`

	// +kubebuilder:default="2h"
	// +optional
	MaxSessionDuration *metav1.Duration `json:"maxSessionDuration,omitempty"`
}

// CodeInterpreterStatus defines the observed state of CodeInterpreter.
type CodeInterpreterStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the CodeInterpreter resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// 记录与 CodeInterpreter 相关的 Pod 的 IP 地址
	// +optional
	PodIP string `json:"podIP,omitempty"`

	// 记录当前状态，例如 "Pending", "Running", "Failed"
	// +optional
	Phase string `json:"phase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=codei

// CodeInterpreter is the Schema for the codeinterpreters API
type CodeInterpreter struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of CodeInterpreter
	// +required
	Spec CodeInterpreterSpec `json:"spec"`

	// status defines the observed state of CodeInterpreter
	// +optional
	Status CodeInterpreterStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// CodeInterpreterList contains a list of CodeInterpreter
type CodeInterpreterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CodeInterpreter `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CodeInterpreter{}, &CodeInterpreterList{})
}
