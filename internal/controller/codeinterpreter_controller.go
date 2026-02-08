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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentlandv1alpha1 "github.com/Fl0rencess720/agentland/api/v1alpha1"
)

// CodeInterpreterReconciler reconciles a CodeInterpreter object
type CodeInterpreterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=codeinterpreters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=codeinterpreters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentland.fl0rencess720.app,resources=codeinterpreters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the CodeInterpreter object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *CodeInterpreterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	codeInterpreter := &agentlandv1alpha1.CodeInterpreter{}
	if err := r.Get(ctx, req.NamespacedName, codeInterpreter); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !codeInterpreter.DeletionTimestamp.IsZero() {
		// 如果资源中没有当前 Controller 管理的终结器，则直接返回
		if !controllerutil.ContainsFinalizer(codeInterpreter, "codeinterpreter.finalizers.agentland.fl0rencess720.app") {
			log.Info("CodeInterpreter is being deleted")
			return ctrl.Result{}, nil
		}

		log.Info("CodeInterpreter is being deleted")
		return ctrl.Result{}, nil
	}

	pod := &corev1.Pod{}
	err := r.Get(ctx, req.NamespacedName, pod)

	if err != nil && errors.IsNotFound(err) {
		// Pod 不存在 -> 创建 Pod
		log.Info("Creating a new Pod", "Pod.Namespace", codeInterpreter.Namespace, "Pod.Name", codeInterpreter.Name)
		pod = r.buildPod(codeInterpreter)

		// 设置 OwnerReference，这样 CR 删了 Pod 也会自动删
		if err := controllerutil.SetControllerReference(codeInterpreter, pod, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}

		if err := r.Create(ctx, pod); err != nil {
			return ctrl.Result{}, err
		}

		// 创建完 Pod 后，更新 CR 状态为 Pending，并 Requeue 等待 Pod 启动
		codeInterpreter.Status.Phase = "Pending"
		if err := r.Status().Update(ctx, codeInterpreter); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: time.Millisecond * 500}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}

	// Pod 存在 -> 检查状态并同步到 CR
	podIP := pod.Status.PodIP
	phase := string(pod.Status.Phase)

	// 如果状态有变化，更新 CRD Status
	if codeInterpreter.Status.PodIP != podIP || codeInterpreter.Status.Phase != phase {
		codeInterpreter.Status.PodIP = podIP
		codeInterpreter.Status.Phase = phase
		if err := r.Status().Update(ctx, codeInterpreter); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Pod 尚未就绪时持续 Requeue，确保状态最终收敛到 Running + PodIP
	if phase != string(corev1.PodRunning) || podIP == "" {
		return ctrl.Result{RequeueAfter: time.Millisecond * 500}, nil
	}

	return ctrl.Result{}, nil
}

func (r *CodeInterpreterReconciler) buildPod(ci *agentlandv1alpha1.CodeInterpreter) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ci.Name,
			Namespace: ci.Namespace,
			Labels: map[string]string{
				"app":      "code-interpreter",
				"instance": ci.Name,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            "main",
					Image:           ci.Spec.Template.Image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         ci.Spec.Template.Command,
					Args:            ci.Spec.Template.Args,
				},
			},
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *CodeInterpreterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentlandv1alpha1.CodeInterpreter{}).
		Owns(&corev1.Pod{}).
		Named("codeinterpreter").
		Complete(r)
}
