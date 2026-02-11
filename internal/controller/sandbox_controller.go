package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentlandv1alpha1 "github.com/Fl0rencess720/agentland/api/v1alpha1"
	commonutils "github.com/Fl0rencess720/agentland/pkg/common/utils"
)

// SandboxReconciler reconciles a Sandbox object.
type SandboxReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=sandboxes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete

func (r *SandboxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	sandbox := &agentlandv1alpha1.Sandbox{}
	if err := r.Get(ctx, req.NamespacedName, sandbox); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !sandbox.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	oldStatus := sandbox.Status.DeepCopy()

	pod, err := r.reconcilePod(ctx, sandbox)
	if err != nil {
		return ctrl.Result{}, err
	}

	if pod == nil {
		sandbox.Status.Phase = "Pending"
		sandbox.Status.PodIP = ""
	} else {
		sandbox.Status.Phase = string(pod.Status.Phase)
		sandbox.Status.PodIP = pod.Status.PodIP
	}

	if !equality.Semantic.DeepEqual(oldStatus, &sandbox.Status) {
		if err := r.Status().Update(ctx, sandbox); err != nil {
			return ctrl.Result{}, err
		}
	}

	if sandbox.Status.Phase != string(corev1.PodRunning) || sandbox.Status.PodIP == "" {
		return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
	}

	logger.V(1).Info("sandbox ready", "sandbox", sandbox.Name, "podIP", sandbox.Status.PodIP)
	return ctrl.Result{}, nil
}

func (r *SandboxReconciler) reconcilePod(ctx context.Context, sandbox *agentlandv1alpha1.Sandbox) (*corev1.Pod, error) {
	logger := log.FromContext(ctx)

	if podName := sandbox.Annotations[commonutils.PodNameAnnotation]; podName != "" {
		adopted := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: sandbox.Namespace}, adopted); err == nil {
			if adopted.Labels == nil {
				adopted.Labels = map[string]string{}
			}
			adopted.Labels[commonutils.SandboxLabel] = commonutils.NameHash(sandbox.Name)
			if controllerRef := metav1.GetControllerOf(adopted); controllerRef == nil {
				if err := controllerutil.SetControllerReference(sandbox, adopted, r.Scheme); err != nil {
					return nil, err
				}
			}
			if err := r.Update(ctx, adopted); err != nil {
				return nil, err
			}
			return adopted, nil
		}
	}

	podList := &corev1.PodList{}
	selector := labels.SelectorFromSet(labels.Set{commonutils.SandboxLabel: commonutils.NameHash(sandbox.Name)})
	if err := r.List(ctx, podList, &client.ListOptions{Namespace: sandbox.Namespace, LabelSelector: selector}); err != nil {
		return nil, err
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.DeletionTimestamp.IsZero() {
			return pod, nil
		}
	}

	if sandbox.Spec.Template == nil {
		return nil, fmt.Errorf("sandboxTemplate is required")
	}

	labels := map[string]string{commonutils.SandboxLabel: commonutils.NameHash(sandbox.Name)}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sandbox.Name,
			Namespace: sandbox.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:            "main",
				Image:           sandbox.Spec.Template.Image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         sandbox.Spec.Template.Command,
				Args:            sandbox.Spec.Template.Args,
			}},
		},
	}

	if err := controllerutil.SetControllerReference(sandbox, pod, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, pod); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.V(1).Info("sandbox pod already exists", "pod", pod.Name)
			return pod, nil
		}
		return nil, err
	}
	return pod, nil
}

func (r *SandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentlandv1alpha1.Sandbox{}).
		Owns(&corev1.Pod{}).
		Named("sandbox").
		Complete(r)
}
