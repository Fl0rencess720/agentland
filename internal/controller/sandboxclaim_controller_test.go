package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentlandv1alpha1 "github.com/Fl0rencess720/agentland/api/v1alpha1"
	commonutils "github.com/Fl0rencess720/agentland/pkg/common/utils"
)

func TestAdoptWarmPodTouchesPoolForBackfill(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := agentlandv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add agentland scheme: %v", err)
	}

	pool := &agentlandv1alpha1.SandboxPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-a",
			Namespace: "agentland-sandboxes",
			UID:       types.UID("pool-uid"),
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-a",
			Namespace: "agentland-sandboxes",
			Labels: map[string]string{
				commonutils.PoolLabel:        commonutils.NameHash("pool-a"),
				commonutils.ProfileHashLabel: commonutils.NameHash("default"),
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: agentlandv1alpha1.GroupVersion.String(),
				Kind:       "SandboxPool",
				Name:       "pool-a",
				UID:        pool.UID,
				Controller: boolPtr(true),
			}},
		},
	}
	claim := &agentlandv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-1",
			Namespace: "agentland-sandboxes",
			UID:       types.UID("claim-uid"),
		},
	}

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pool.DeepCopy(), pod.DeepCopy()).
		Build()
	r := &SandboxClaimReconciler{Client: cli}

	loadedPod := &corev1.Pod{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, loadedPod); err != nil {
		t.Fatalf("get pod: %v", err)
	}

	if err := r.adoptWarmPod(context.Background(), claim, loadedPod); err != nil {
		t.Fatalf("adoptWarmPod: %v", err)
	}

	gotPod := &corev1.Pod{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, gotPod); err != nil {
		t.Fatalf("get adopted pod: %v", err)
	}
	if _, ok := gotPod.Labels[commonutils.PoolLabel]; ok {
		t.Fatalf("pool label must be removed after adoption")
	}
	if _, ok := gotPod.Labels[commonutils.ProfileHashLabel]; ok {
		t.Fatalf("profile-hash label must be removed after adoption")
	}
	if gotPod.Labels[commonutils.SandboxLabel] != commonutils.NameHash(claim.Name) {
		t.Fatalf("sandbox label mismatch: %q", gotPod.Labels[commonutils.SandboxLabel])
	}
	if gotPod.Labels[commonutils.ClaimUIDLabel] != string(claim.UID) {
		t.Fatalf("claim uid label mismatch: %q", gotPod.Labels[commonutils.ClaimUIDLabel])
	}
	if len(gotPod.OwnerReferences) != 0 {
		t.Fatalf("ownerReferences must be cleared after adoption")
	}

	gotPool := &agentlandv1alpha1.SandboxPool{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}, gotPool); err != nil {
		t.Fatalf("get pool: %v", err)
	}
	if gotPool.Annotations[commonutils.PoolBackfillTouchAnnotation] == "" {
		t.Fatalf("pool must be touched to trigger backfill reconcile")
	}
}

func boolPtr(v bool) *bool { return &v }
