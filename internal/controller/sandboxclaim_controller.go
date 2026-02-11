package controller

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentlandv1alpha1 "github.com/Fl0rencess720/agentland/api/v1alpha1"
	commonutils "github.com/Fl0rencess720/agentland/pkg/common/utils"
)

// SandboxClaimReconciler reconciles a SandboxClaim object.
type SandboxClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete

func (r *SandboxClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	claim := &agentlandv1alpha1.SandboxClaim{}
	if err := r.Get(ctx, req.NamespacedName, claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !claim.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if claim.Spec.Template == nil {
		return ctrl.Result{}, fmt.Errorf("sandboxTemplate is required")
	}

	oldStatus := claim.Status.DeepCopy()

	sandbox := &agentlandv1alpha1.Sandbox{}
	err := r.Get(ctx, client.ObjectKey{Namespace: claim.Namespace, Name: claim.Name}, sandbox)
	if err == nil {
		claim.Status.SandboxName = sandbox.Name
		if sandbox.Status.Phase == string(corev1.PodRunning) && sandbox.Status.PodIP != "" {
			claim.Status.Phase = agentlandv1alpha1.SandboxClaimPhaseBound
			claim.Status.Reason = "Bound"
		} else {
			claim.Status.Phase = agentlandv1alpha1.SandboxClaimPhasePending
			claim.Status.Reason = "SandboxPending"
		}
		if err := r.updateClaimStatus(ctx, oldStatus, claim); err != nil {
			return ctrl.Result{}, err
		}
		if claim.Status.Phase != agentlandv1alpha1.SandboxClaimPhaseBound {
			return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
		}
		return ctrl.Result{}, nil
	}
	if !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	pod, err := r.selectWarmPod(ctx, claim)
	if err != nil {
		return ctrl.Result{}, err
	}

	if pod == nil && claim.Spec.FallbackPolicy == agentlandv1alpha1.FallbackPolicyForbidColdStart {
		claim.Status.Phase = agentlandv1alpha1.SandboxClaimPhaseFailed
		claim.Status.Reason = "NoWarmPod"
		if err := r.updateClaimStatus(ctx, oldStatus, claim); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if pod != nil {
		if err := r.adoptWarmPod(ctx, claim, pod); err != nil {
			return ctrl.Result{}, err
		}
		logger.V(1).Info("adopted warm pod", "claim", claim.Name, "pod", pod.Name)
	}

	sandbox = &agentlandv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:        claim.Name,
			Namespace:   claim.Namespace,
			Annotations: map[string]string{},
		},
		Spec: agentlandv1alpha1.SandboxSpec{
			Profile:  claim.Spec.Profile,
			ClaimRef: claim.Name,
			Template: claim.Spec.Template.DeepCopy(),
		},
	}
	if pod != nil {
		sandbox.Annotations[commonutils.PodNameAnnotation] = pod.Name
	}
	if err := controllerutil.SetControllerReference(claim, sandbox, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, sandbox); err != nil && !errors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}

	claim.Status.Phase = agentlandv1alpha1.SandboxClaimPhasePending
	claim.Status.SandboxName = claim.Name
	claim.Status.Reason = "SandboxCreating"
	if err := r.updateClaimStatus(ctx, oldStatus, claim); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
}

func (r *SandboxClaimReconciler) selectWarmPod(ctx context.Context, claim *agentlandv1alpha1.SandboxClaim) (*corev1.Pod, error) {
	podList := &corev1.PodList{}
	selectorSet := labels.Set{commonutils.ProfileHashLabel: commonutils.NameHash(claim.Spec.Profile)}
	if claim.Spec.PoolRef != "" {
		selectorSet[commonutils.PoolLabel] = commonutils.NameHash(claim.Spec.PoolRef)
	}
	selector := labels.SelectorFromSet(selectorSet)
	if err := r.List(ctx, podList, &client.ListOptions{Namespace: claim.Namespace, LabelSelector: selector}); err != nil {
		return nil, err
	}

	candidates := make([]*corev1.Pod, 0, len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		if controllerRef := metav1.GetControllerOf(pod); controllerRef != nil && controllerRef.Kind != "SandboxPool" {
			continue
		}
		candidates = append(candidates, pod)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		ir, jr := commonutils.IsPodReady(candidates[i]), commonutils.IsPodReady(candidates[j])
		if ir != jr {
			return ir
		}
		return candidates[i].CreationTimestamp.Before(&candidates[j].CreationTimestamp)
	})

	return candidates[0], nil
}

func (r *SandboxClaimReconciler) adoptWarmPod(ctx context.Context, claim *agentlandv1alpha1.SandboxClaim, pod *corev1.Pod) error {
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	delete(pod.Labels, commonutils.PoolLabel)
	delete(pod.Labels, commonutils.ProfileHashLabel)
	pod.Labels[commonutils.SandboxLabel] = commonutils.NameHash(claim.Name)
	pod.Labels[commonutils.ClaimUIDLabel] = string(claim.UID)
	pod.OwnerReferences = nil
	return r.Update(ctx, pod)
}

func (r *SandboxClaimReconciler) updateClaimStatus(ctx context.Context, oldStatus *agentlandv1alpha1.SandboxClaimStatus, claim *agentlandv1alpha1.SandboxClaim) error {
	if equality.Semantic.DeepEqual(oldStatus, &claim.Status) {
		return nil
	}
	return r.Status().Update(ctx, claim)
}

func (r *SandboxClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentlandv1alpha1.SandboxClaim{}).
		Complete(r)
}
