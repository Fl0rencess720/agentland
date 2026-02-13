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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
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

// AgentSessionReconciler reconciles an AgentSession object
type AgentSessionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=agentsessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=agentsessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=agentsessions/finalizers,verbs=update
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=agentruntimes,verbs=get;list;watch
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxes/status,verbs=get;update;patch

func (r *AgentSessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	agentSession := &agentlandv1alpha1.AgentSession{}
	if err := r.Get(ctx, req.NamespacedName, agentSession); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !agentSession.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(agentSession, "agentsession.finalizers.agentland.fl0rencess720.app") {
			log.Info("AgentSession is being deleted")
			return ctrl.Result{}, nil
		}
		log.Info("AgentSession is being deleted")
		return ctrl.Result{}, nil
	}

	resolved, result, err := r.resolveSessionConfig(ctx, agentSession)
	if err != nil {
		return ctrl.Result{}, err
	}
	if result != nil {
		return *result, nil
	}

	mode := agentlandv1alpha1.ProvisioningModeDirect
	if resolved.Provisioning != nil && resolved.Provisioning.Mode != "" {
		mode = resolved.Provisioning.Mode
	}

	if mode == agentlandv1alpha1.ProvisioningModeDirect {
		return r.reconcileDirect(ctx, agentSession, resolved)
	}
	return r.reconcileViaClaim(ctx, agentSession, resolved, mode)
}

type resolvedSessionConfig struct {
	Template     *agentlandv1alpha1.SandboxTemplate
	Provisioning *agentlandv1alpha1.ProvisioningSpec
}

func (r *AgentSessionReconciler) resolveSessionConfig(ctx context.Context, agentSession *agentlandv1alpha1.AgentSession) (*resolvedSessionConfig, *ctrl.Result, error) {
	resolved := &resolvedSessionConfig{}

	if agentSession.Spec.RuntimeRef != nil {
		runtimeNamespace := agentSession.Spec.RuntimeRef.Namespace
		if runtimeNamespace == "" {
			runtimeNamespace = agentSession.Namespace
		}

		runtimeObj := &agentlandv1alpha1.AgentRuntime{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: runtimeNamespace, Name: agentSession.Spec.RuntimeRef.Name}, runtimeObj); err != nil {
			if errors.IsNotFound(err) {
				if statusErr := r.markAgentSessionFailed(ctx, agentSession, "RuntimeNotFound",
					fmt.Sprintf("runtimeRef %s/%s not found", runtimeNamespace, agentSession.Spec.RuntimeRef.Name)); statusErr != nil {
					return nil, nil, statusErr
				}
				result := ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}
				return nil, &result, nil
			}
			return nil, nil, err
		}

		if runtimeObj.Spec.Template != nil {
			resolved.Template = runtimeObj.Spec.Template.DeepCopy()
		}
		if runtimeObj.Spec.Provisioning != nil {
			tmp := *runtimeObj.Spec.Provisioning
			resolved.Provisioning = &tmp
		}
	}

	if agentSession.Spec.Template != nil {
		resolved.Template = agentSession.Spec.Template.DeepCopy()
	}
	if agentSession.Spec.Provisioning != nil {
		tmp := *agentSession.Spec.Provisioning
		resolved.Provisioning = &tmp
	}

	if resolved.Template == nil || resolved.Template.Image == "" {
		if err := r.markAgentSessionFailed(ctx, agentSession, "TemplateMissing",
			"effective sandboxTemplate is empty; set runtimeRef or sandboxTemplate"); err != nil {
			return nil, nil, err
		}
		return nil, &ctrl.Result{}, nil
	}

	return resolved, nil, nil
}

func (r *AgentSessionReconciler) markAgentSessionFailed(ctx context.Context, agentSession *agentlandv1alpha1.AgentSession, reason, message string) error {
	oldStatus := agentSession.Status.DeepCopy()
	agentSession.Status.Phase = "Failed"
	agentSession.Status.PodIP = ""
	agentSession.Status.ClaimName = ""
	agentSession.Status.SandboxName = ""

	meta.SetStatusCondition(&agentSession.Status.Conditions, metav1.Condition{
		Type:               "Accepted",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: agentSession.Generation,
		LastTransitionTime: metav1.Now(),
	})

	if !equality.Semantic.DeepEqual(oldStatus, &agentSession.Status) {
		return r.Status().Update(ctx, agentSession)
	}
	return nil
}

func (r *AgentSessionReconciler) reconcileDirect(ctx context.Context, agentSession *agentlandv1alpha1.AgentSession, resolved *resolvedSessionConfig) (ctrl.Result, error) {
	profile := "default"
	if resolved.Provisioning != nil && resolved.Provisioning.Profile != "" {
		profile = resolved.Provisioning.Profile
	}

	sandbox := &agentlandv1alpha1.Sandbox{}
	err := r.Get(ctx, client.ObjectKey{Namespace: agentSession.Namespace, Name: agentSession.Name}, sandbox)
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if errors.IsNotFound(err) {
		sandbox = &agentlandv1alpha1.Sandbox{
			ObjectMeta: metav1.ObjectMeta{
				Name:      agentSession.Name,
				Namespace: agentSession.Namespace,
			},
			Spec: agentlandv1alpha1.SandboxSpec{
				Profile:  profile,
				ClaimRef: "",
				Template: resolved.Template.DeepCopy(),
			},
		}
		if err := controllerutil.SetControllerReference(agentSession, sandbox, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, sandbox); err != nil && !errors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
	}

	return r.updateAgentSessionStatus(ctx, agentSession, "", agentSession.Name)
}

func (r *AgentSessionReconciler) reconcileViaClaim(ctx context.Context, agentSession *agentlandv1alpha1.AgentSession, resolved *resolvedSessionConfig, mode agentlandv1alpha1.ProvisioningMode) (ctrl.Result, error) {
	profile := "default"
	poolRef := ""
	if resolved.Provisioning != nil {
		if resolved.Provisioning.Profile != "" {
			profile = resolved.Provisioning.Profile
		}
		poolRef = resolved.Provisioning.PoolRef
	}

	fallback := agentlandv1alpha1.FallbackPolicyAllowColdStart
	if mode == agentlandv1alpha1.ProvisioningModePoolRequired {
		fallback = agentlandv1alpha1.FallbackPolicyForbidColdStart
	}

	claim := &agentlandv1alpha1.SandboxClaim{}
	err := r.Get(ctx, client.ObjectKey{Namespace: agentSession.Namespace, Name: agentSession.Name}, claim)
	if err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if errors.IsNotFound(err) {
		claim = &agentlandv1alpha1.SandboxClaim{
			ObjectMeta: metav1.ObjectMeta{Name: agentSession.Name, Namespace: agentSession.Namespace},
			Spec: agentlandv1alpha1.SandboxClaimSpec{
				Profile:        profile,
				PoolRef:        poolRef,
				FallbackPolicy: fallback,
				Template:       resolved.Template.DeepCopy(),
			},
		}
		if err := controllerutil.SetControllerReference(agentSession, claim, r.Scheme); err != nil {
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
		oldStatus := agentSession.Status.DeepCopy()
		agentSession.Status.ClaimName = agentSession.Name
		agentSession.Status.SandboxName = claim.Status.SandboxName
		agentSession.Status.Phase = string(agentlandv1alpha1.SandboxClaimPhaseFailed)
		agentSession.Status.PodIP = ""
		if !equality.Semantic.DeepEqual(oldStatus, &agentSession.Status) {
			if err := r.Status().Update(ctx, agentSession); err != nil {
				if !errors.IsConflict(err) {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
			}
		}
		return ctrl.Result{}, nil
	}

	return r.updateAgentSessionStatus(ctx, agentSession, agentSession.Name, agentSession.Name)
}

func (r *AgentSessionReconciler) updateAgentSessionStatus(ctx context.Context, agentSession *agentlandv1alpha1.AgentSession, claimName, sandboxName string) (ctrl.Result, error) {
	oldStatus := agentSession.Status.DeepCopy()

	agentSession.Status.ClaimName = claimName
	agentSession.Status.SandboxName = sandboxName
	agentSession.Status.Phase = "Pending"
	agentSession.Status.PodIP = ""

	sandbox := &agentlandv1alpha1.Sandbox{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: agentSession.Namespace, Name: sandboxName}, sandbox); err == nil {
		agentSession.Status.SandboxName = sandbox.Name
		agentSession.Status.Phase = sandbox.Status.Phase
		agentSession.Status.PodIP = sandbox.Status.PodIP
	} else if !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	if !equality.Semantic.DeepEqual(oldStatus, &agentSession.Status) {
		if err := r.Status().Update(ctx, agentSession); err != nil {
			if !errors.IsConflict(err) {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
		}
	}

	if agentSession.Status.Phase != string(corev1.PodRunning) || agentSession.Status.PodIP == "" {
		return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentSessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentlandv1alpha1.AgentSession{}).
		Owns(&agentlandv1alpha1.SandboxClaim{}).
		Owns(&agentlandv1alpha1.Sandbox{}).
		Named("agentsession").
		Complete(r)
}
