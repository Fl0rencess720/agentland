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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentlandv1alpha1 "github.com/Fl0rencess720/agentland/api/v1alpha1"
	commonutils "github.com/Fl0rencess720/agentland/pkg/common/utils"
)

// CodeInterpreterReconciler reconciles a CodeInterpreter object
type CodeInterpreterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=codeinterpreters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=codeinterpreters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=codeinterpreters/finalizers,verbs=update
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxes/status,verbs=get;update;patch

func (r *CodeInterpreterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ci := &agentlandv1alpha1.CodeInterpreter{}
	if err := r.Get(ctx, req.NamespacedName, ci); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !ci.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(ci, "codeinterpreter.finalizers.agentland.fl0rencess720.app") {
			log.Info("CodeInterpreter is being deleted")
			return ctrl.Result{}, nil
		}
		log.Info("CodeInterpreter is being deleted")
		return ctrl.Result{}, nil
	}

	mode := agentlandv1alpha1.ProvisioningModeDirect
	if ci.Spec.Provisioning != nil && ci.Spec.Provisioning.Mode != "" {
		mode = ci.Spec.Provisioning.Mode
	}

	if mode == agentlandv1alpha1.ProvisioningModeDirect {
		return r.reconcileDirect(ctx, ci)
	}
	return r.reconcileViaClaim(ctx, ci, mode)
}

func (r *CodeInterpreterReconciler) reconcileDirect(ctx context.Context, ci *agentlandv1alpha1.CodeInterpreter) (ctrl.Result, error) {
	profile := "default"
	if ci.Spec.Provisioning != nil && ci.Spec.Provisioning.Profile != "" {
		profile = ci.Spec.Provisioning.Profile
	}

	sandbox := &agentlandv1alpha1.Sandbox{}
	err := r.Get(ctx, client.ObjectKey{Namespace: ci.Namespace, Name: ci.Name}, sandbox)
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if errors.IsNotFound(err) {
		sandbox = &agentlandv1alpha1.Sandbox{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ci.Name,
				Namespace: ci.Namespace,
			},
			Spec: agentlandv1alpha1.SandboxSpec{
				Profile:  profile,
				ClaimRef: "",
				Template: ci.Spec.Template.DeepCopy(),
			},
		}
		if err := controllerutil.SetControllerReference(ci, sandbox, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, sandbox); err != nil && !errors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
	}

	return r.updateCodeInterpreterStatus(ctx, ci, "", ci.Name)
}

func (r *CodeInterpreterReconciler) reconcileViaClaim(ctx context.Context, ci *agentlandv1alpha1.CodeInterpreter, mode agentlandv1alpha1.ProvisioningMode) (ctrl.Result, error) {
	profile := "default"
	poolRef := ""
	if ci.Spec.Provisioning != nil {
		if ci.Spec.Provisioning.Profile != "" {
			profile = ci.Spec.Provisioning.Profile
		}
		poolRef = ci.Spec.Provisioning.PoolRef
	}

	fallback := agentlandv1alpha1.FallbackPolicyAllowColdStart
	if mode == agentlandv1alpha1.ProvisioningModePoolRequired {
		fallback = agentlandv1alpha1.FallbackPolicyForbidColdStart
	}

	claim := &agentlandv1alpha1.SandboxClaim{}
	err := r.Get(ctx, client.ObjectKey{Namespace: ci.Namespace, Name: ci.Name}, claim)
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if errors.IsNotFound(err) {
		claim = &agentlandv1alpha1.SandboxClaim{
			ObjectMeta: metav1.ObjectMeta{Name: ci.Name, Namespace: ci.Namespace},
			Spec: agentlandv1alpha1.SandboxClaimSpec{
				Profile:        profile,
				PoolRef:        poolRef,
				FallbackPolicy: fallback,
				Template:       ci.Spec.Template.DeepCopy(),
			},
		}
		if err := controllerutil.SetControllerReference(ci, claim, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, claim); err != nil {
			if !errors.IsAlreadyExists(err) {
				return ctrl.Result{}, err
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(claim), claim); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	if claim.Status.Phase == agentlandv1alpha1.SandboxClaimPhaseFailed {
		oldStatus := ci.Status.DeepCopy()
		ci.Status.ClaimName = ci.Name
		ci.Status.SandboxName = claim.Status.SandboxName
		ci.Status.Phase = string(agentlandv1alpha1.SandboxClaimPhaseFailed)
		ci.Status.PodIP = ""
		if !equality.Semantic.DeepEqual(oldStatus, &ci.Status) {
			if err := r.Status().Update(ctx, ci); err != nil {
				if !errors.IsConflict(err) {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
			}
		}
		return ctrl.Result{}, nil
	}

	return r.updateCodeInterpreterStatus(ctx, ci, ci.Name, ci.Name)
}

func (r *CodeInterpreterReconciler) updateCodeInterpreterStatus(ctx context.Context, ci *agentlandv1alpha1.CodeInterpreter, claimName, sandboxName string) (ctrl.Result, error) {
	oldStatus := ci.Status.DeepCopy()

	ci.Status.ClaimName = claimName
	ci.Status.SandboxName = sandboxName
	ci.Status.Phase = "Pending"
	ci.Status.PodIP = ""

	sandbox := &agentlandv1alpha1.Sandbox{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ci.Namespace, Name: sandboxName}, sandbox); err == nil {
		ci.Status.SandboxName = sandbox.Name
		ci.Status.Phase = sandbox.Status.Phase
		ci.Status.PodIP = sandbox.Status.PodIP
	} else if !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if !equality.Semantic.DeepEqual(oldStatus, &ci.Status) {
		if err := r.Status().Update(ctx, ci); err != nil {
			if !errors.IsConflict(err) {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
		}
	}

	if ci.Status.Phase != string(corev1.PodRunning) || ci.Status.PodIP == "" {
		return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CodeInterpreterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentlandv1alpha1.CodeInterpreter{}).
		Owns(&agentlandv1alpha1.SandboxClaim{}).
		Owns(&agentlandv1alpha1.Sandbox{}).
		Named("codeinterpreter").
		Complete(r)
}
