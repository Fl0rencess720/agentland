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

	agentlandv1alpha1 "github.com/Fl0rencess720/agentland/api/v1alpha1"
	commonutils "github.com/Fl0rencess720/agentland/pkg/common/utils"
)

// SandboxPoolReconciler reconciles a SandboxPool object.
type SandboxPoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxpools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxpools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete

func (r *SandboxPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pool := &agentlandv1alpha1.SandboxPool{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !pool.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if pool.Spec.Template == nil {
		return ctrl.Result{}, fmt.Errorf("sandboxTemplate is required")
	}

	oldStatus := pool.Status.DeepCopy()

	activePods, err := r.listPoolPods(ctx, pool)
	if err != nil {
		return ctrl.Result{}, err
	}

	desired := pool.Spec.Replicas
	current := int32(len(activePods))

	pool.Status.Replicas = current
	ready := int32(0)
	for i := range activePods {
		if commonutils.IsPodReady(&activePods[i]) {
			ready++
		}
	}
	pool.Status.ReadyReplicas = ready

	if current < desired {
		for i := int32(0); i < desired-current; i++ {
			if err := r.createPoolPod(ctx, pool); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, r.updatePoolStatus(ctx, oldStatus, pool)
	}

	if current > desired {
		sort.Slice(activePods, func(i, j int) bool {
			return activePods[i].CreationTimestamp.After(activePods[j].CreationTimestamp.Time)
		})
		for i := int32(0); i < current-desired; i++ {
			pod := &activePods[i]
			if err := r.Delete(ctx, pod); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, r.updatePoolStatus(ctx, oldStatus, pool)
	}

	if err := r.updatePoolStatus(ctx, oldStatus, pool); err != nil {
		return ctrl.Result{}, err
	}

	if pool.Status.ReadyReplicas != pool.Spec.Replicas {
		return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
	}
	return ctrl.Result{}, nil
}

func (r *SandboxPoolReconciler) listPoolPods(ctx context.Context, pool *agentlandv1alpha1.SandboxPool) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	selector := labels.SelectorFromSet(labels.Set{commonutils.PoolLabel: commonutils.NameHash(pool.Name)})
	if err := r.List(ctx, podList, &client.ListOptions{Namespace: pool.Namespace, LabelSelector: selector}); err != nil {
		return nil, err
	}

	active := make([]corev1.Pod, 0, len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		if controllerRef := metav1.GetControllerOf(pod); controllerRef == nil {
			if err := controllerutil.SetControllerReference(pool, pod, r.Scheme); err == nil {
				if err := r.Update(ctx, pod); err != nil {
					return nil, err
				}
			}
		} else if controllerRef.UID != pool.UID {
			continue
		}
		active = append(active, *pod)
	}
	return active, nil
}

func (r *SandboxPoolReconciler) createPoolPod(ctx context.Context, pool *agentlandv1alpha1.SandboxPool) error {
	labelsMap := map[string]string{
		commonutils.PoolLabel:        commonutils.NameHash(pool.Name),
		commonutils.ProfileHashLabel: commonutils.NameHash(pool.Spec.Profile),
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", pool.Name),
			Namespace:    pool.Namespace,
			Labels:       labelsMap,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:            "main",
				Image:           pool.Spec.Template.Image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         pool.Spec.Template.Command,
				Args:            pool.Spec.Template.Args,
			}},
		},
	}
	if err := controllerutil.SetControllerReference(pool, pod, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, pod)
}

func (r *SandboxPoolReconciler) updatePoolStatus(ctx context.Context, oldStatus *agentlandv1alpha1.SandboxPoolStatus, pool *agentlandv1alpha1.SandboxPool) error {
	if equality.Semantic.DeepEqual(oldStatus, &pool.Status) {
		return nil
	}
	return r.Status().Update(ctx, pool)
}

func (r *SandboxPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentlandv1alpha1.SandboxPool{}).
		Owns(&corev1.Pod{}).
		Named("sandboxpool").
		Complete(r)
}
