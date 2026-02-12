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

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentlandv1alpha1 "github.com/Fl0rencess720/agentland/api/v1alpha1"
)

// AgentRuntimeReconciler reconciles an AgentRuntime object.
type AgentRuntimeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=agentruntimes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=agentruntimes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=agentruntimes/finalizers,verbs=update

func (r *AgentRuntimeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	runtimeObj := &agentlandv1alpha1.AgentRuntime{}
	if err := r.Get(ctx, req.NamespacedName, runtimeObj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	oldStatus := runtimeObj.Status.DeepCopy()

	condition := metav1.Condition{
		Type:               "Accepted",
		ObservedGeneration: runtimeObj.Generation,
		Status:             metav1.ConditionTrue,
		Reason:             "TemplateValid",
		Message:            "sandboxTemplate is valid",
	}

	if runtimeObj.Spec.Template == nil || runtimeObj.Spec.Template.Image == "" {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "TemplateInvalid"
		condition.Message = "sandboxTemplate.image is required"
	}

	meta.SetStatusCondition(&runtimeObj.Status.Conditions, condition)

	if !equality.Semantic.DeepEqual(oldStatus, &runtimeObj.Status) {
		if err := r.Status().Update(ctx, runtimeObj); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRuntimeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentlandv1alpha1.AgentRuntime{}).
		Named("agentruntime").
		Complete(r)
}
