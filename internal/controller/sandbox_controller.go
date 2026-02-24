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
	"github.com/Fl0rencess720/agentland/pkg/common/observability"
	commonutils "github.com/Fl0rencess720/agentland/pkg/common/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// SandboxReconciler reconciles a Sandbox object.
type SandboxReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Tracer          trace.Tracer
	ImagePullPolicy corev1.PullPolicy
}

func (r *SandboxReconciler) startSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	tracer := r.Tracer
	if tracer == nil {
		tracer = otel.Tracer("controller.sandbox")
	}
	return tracer.Start(ctx, name)
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
	ctx = observability.ExtractContextFromAnnotations(ctx, sandbox.Annotations)
	ctx, span := r.startSpan(ctx, "controller.sandbox.reconcile")
	defer span.End()
	span.SetAttributes(
		attribute.String("request.id", observability.RequestIDFromContext(ctx)),
		attribute.String("agentland.session_id", sandbox.Name),
		attribute.String("k8s.namespace", sandbox.Namespace),
	)

	if !sandbox.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	oldStatus := sandbox.Status.DeepCopy()

	pod, err := r.reconcilePod(ctx, sandbox)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "reconcile pod failed")
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
			if errors.IsConflict(err) {
				return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
			}
			span.RecordError(err)
			span.SetStatus(codes.Error, "update sandbox status failed")
			return ctrl.Result{}, err
		}
	}

	if sandbox.Status.Phase != string(corev1.PodRunning) || sandbox.Status.PodIP == "" {
		return ctrl.Result{RequeueAfter: commonutils.DefaultRequeueInterval}, nil
	}

	span.AddEvent("sandbox.running", trace.WithAttributes(attribute.String("sandbox.pod_ip", sandbox.Status.PodIP)))
	logger.V(1).Info("sandbox ready", "sandbox", sandbox.Name, "podIP", sandbox.Status.PodIP)
	return ctrl.Result{}, nil
}

func (r *SandboxReconciler) reconcilePod(ctx context.Context, sandbox *agentlandv1alpha1.Sandbox) (*corev1.Pod, error) {
	logger := log.FromContext(ctx)
	ctx, span := r.startSpan(ctx, "controller.sandbox.reconcile_pod")
	defer span.End()

	if podName := sandbox.Annotations[commonutils.PodNameAnnotation]; podName != "" {
		adopted := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: sandbox.Namespace}, adopted); err == nil {
			if adopted.Labels == nil {
				adopted.Labels = map[string]string{}
			}
			adopted.Labels[commonutils.SandboxLabel] = commonutils.NameHash(sandbox.Name)
			if controllerRef := metav1.GetControllerOf(adopted); controllerRef == nil {
				if err := controllerutil.SetControllerReference(sandbox, adopted, r.Scheme); err != nil {
					span.RecordError(err)
					span.SetStatus(codes.Error, "set adopted pod owner failed")
					return nil, err
				}
			}
			if err := r.Update(ctx, adopted); err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, "update adopted pod failed")
				return nil, err
			}
			span.SetAttributes(
				attribute.String("pod.name", adopted.Name),
				attribute.String("sandbox.path", "adopted_warm_pod"),
			)
			return adopted, nil
		}
	}

	podList := &corev1.PodList{}
	selector := labels.SelectorFromSet(labels.Set{commonutils.SandboxLabel: commonutils.NameHash(sandbox.Name)})
	if err := r.List(ctx, podList, &client.ListOptions{Namespace: sandbox.Namespace, LabelSelector: selector}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "list sandbox pods failed")
		return nil, err
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.DeletionTimestamp.IsZero() {
			span.SetAttributes(
				attribute.String("pod.name", pod.Name),
				attribute.String("sandbox.path", "existing_pod"),
			)
			return pod, nil
		}
	}

	if sandbox.Spec.Template == nil {
		span.SetStatus(codes.Error, "sandboxTemplate is required")
		return nil, fmt.Errorf("sandboxTemplate is required")
	}

	labels := map[string]string{commonutils.SandboxLabel: commonutils.NameHash(sandbox.Name)}
	pullPolicy := r.ImagePullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullAlways
	}
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
				ImagePullPolicy: pullPolicy,
				Command:         sandbox.Spec.Template.Command,
				Args:            sandbox.Spec.Template.Args,
				VolumeMounts: []corev1.VolumeMount{{
					Name:      sandboxJWTVolumeName,
					MountPath: "/var/run/agentland/jwt",
					ReadOnly:  true,
				}, {
					Name:      workspaceVolumeName,
					MountPath: workspaceMountPath,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: sandboxJWTVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: "gateway-sandbox-jwt-public-key",
					},
				},
			}, {
				Name: workspaceVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}},
		},
	}

	if err := controllerutil.SetControllerReference(sandbox, pod, r.Scheme); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "set new pod owner failed")
		return nil, err
	}
	if err := r.Create(ctx, pod); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.V(1).Info("sandbox pod already exists", "pod", pod.Name)
			span.SetAttributes(
				attribute.String("pod.name", pod.Name),
				attribute.String("sandbox.path", "already_exists"),
			)
			return pod, nil
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "create sandbox pod failed")
		return nil, err
	}
	span.SetAttributes(
		attribute.String("pod.name", pod.Name),
		attribute.String("sandbox.path", "cold_create"),
	)
	return pod, nil
}

func (r *SandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Tracer == nil {
		r.Tracer = otel.Tracer("controller.sandbox")
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&agentlandv1alpha1.Sandbox{}).
		Owns(&corev1.Pod{}).
		Named("sandbox").
		Complete(r)
}
