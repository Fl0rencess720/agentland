package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestSandboxStatusFromPod(t *testing.T) {
	t.Parallel()

	readyPod := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.8",
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	notReadyPod := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.9",
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionFalse,
			}},
		},
	}
	failedPod := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed,
		},
	}

	cases := []struct {
		name      string
		pod       *corev1.Pod
		wantPhase string
		wantIP    string
	}{
		{name: "nil pod", pod: nil, wantPhase: "Pending", wantIP: ""},
		{name: "running but not ready", pod: notReadyPod, wantPhase: "Pending", wantIP: ""},
		{name: "running and ready", pod: readyPod, wantPhase: string(corev1.PodRunning), wantIP: "10.0.0.8"},
		{name: "failed pod", pod: failedPod, wantPhase: string(corev1.PodFailed), wantIP: ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			phase, ip := sandboxStatusFromPod(tc.pod)
			if phase != tc.wantPhase || ip != tc.wantIP {
				t.Fatalf("sandboxStatusFromPod() = (%q, %q), want (%q, %q)", phase, ip, tc.wantPhase, tc.wantIP)
			}
		})
	}
}
