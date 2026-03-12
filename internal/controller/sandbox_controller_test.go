package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	agentlandv1alpha1 "github.com/Fl0rencess720/agentland/api/v1alpha1"
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

func TestBuildPodSpecFromTemplateLegacyFields(t *testing.T) {
	t.Parallel()

	spec, err := buildPodSpecFromTemplate(&agentlandv1alpha1.SandboxTemplate{
		Image:            "busybox:1.36",
		Command:          []string{"sleep"},
		Args:             []string{"3600"},
		RuntimeClassName: "kata-qemu",
	}, corev1.PullAlways)
	if err != nil {
		t.Fatalf("buildPodSpecFromTemplate() error = %v", err)
	}
	if len(spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(spec.Containers))
	}
	if spec.Containers[0].Image != "busybox:1.36" {
		t.Fatalf("unexpected image: %s", spec.Containers[0].Image)
	}
	if spec.RuntimeClassName == nil || *spec.RuntimeClassName != "kata-qemu" {
		t.Fatalf("runtimeClassName not propagated: %#v", spec.RuntimeClassName)
	}
	if !hasVolume(spec.Volumes, sandboxJWTVolumeName) || !hasVolume(spec.Volumes, workspaceVolumeName) {
		t.Fatalf("reserved volumes not injected: %#v", spec.Volumes)
	}
	if !hasMount(spec.Containers[0].VolumeMounts, sandboxJWTVolumeName, sandboxJWTMountPath) {
		t.Fatalf("sandbox jwt mount missing: %#v", spec.Containers[0].VolumeMounts)
	}
	if !hasMount(spec.Containers[0].VolumeMounts, workspaceVolumeName, workspaceMountPath) {
		t.Fatalf("workspace mount missing: %#v", spec.Containers[0].VolumeMounts)
	}
}

func TestBuildPodSpecFromTemplateOfficialPodSpec(t *testing.T) {
	t.Parallel()

	readyProbe := &corev1.Probe{}
	spec, err := buildPodSpecFromTemplate(&agentlandv1alpha1.SandboxTemplate{
		PodSpec: &corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            "korokd",
					Image:           "example/korokd:latest",
					Env:             []corev1.EnvVar{{Name: "ALPHA", Value: "1"}},
					ReadinessProbe:  readyProbe,
					ImagePullPolicy: corev1.PullIfNotPresent,
				},
				{
					Name:  "agent",
					Image: "example/agent:latest",
				},
			},
			InitContainers: []corev1.Container{{Name: "init", Image: "example/init:latest"}},
		},
	}, corev1.PullAlways)
	if err != nil {
		t.Fatalf("buildPodSpecFromTemplate() error = %v", err)
	}
	if len(spec.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(spec.Containers))
	}
	if spec.Containers[0].Env[0].Name != "ALPHA" {
		t.Fatalf("env not preserved: %#v", spec.Containers[0].Env)
	}
	if spec.Containers[0].ReadinessProbe == nil {
		t.Fatalf("readinessProbe not preserved")
	}
	if spec.Containers[0].ImagePullPolicy != corev1.PullIfNotPresent {
		t.Fatalf("existing pull policy should be preserved, got %s", spec.Containers[0].ImagePullPolicy)
	}
	if spec.Containers[1].ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("default pull policy not applied to second container: %s", spec.Containers[1].ImagePullPolicy)
	}
	if spec.InitContainers[0].ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("default pull policy not applied to init container: %s", spec.InitContainers[0].ImagePullPolicy)
	}
	for _, container := range spec.Containers {
		if !hasMount(container.VolumeMounts, sandboxJWTVolumeName, sandboxJWTMountPath) {
			t.Fatalf("sandbox jwt mount missing from %s: %#v", container.Name, container.VolumeMounts)
		}
		if !hasMount(container.VolumeMounts, workspaceVolumeName, workspaceMountPath) {
			t.Fatalf("workspace mount missing from %s: %#v", container.Name, container.VolumeMounts)
		}
	}
}

func TestBuildPodSpecFromTemplateRejectsReservedVolumeOverride(t *testing.T) {
	t.Parallel()

	_, err := buildPodSpecFromTemplate(&agentlandv1alpha1.SandboxTemplate{
		PodSpec: &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "agent", Image: "example/agent:latest"}},
			Volumes: []corev1.Volume{{
				Name: sandboxJWTVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}},
		},
	}, corev1.PullAlways)
	if err == nil {
		t.Fatal("expected reserved volume override to fail")
	}
}

func hasVolume(volumes []corev1.Volume, name string) bool {
	for _, volume := range volumes {
		if volume.Name == name {
			return true
		}
	}
	return false
}

func hasMount(mounts []corev1.VolumeMount, name, path string) bool {
	for _, mount := range mounts {
		if mount.Name == name && mount.MountPath == path {
			return true
		}
	}
	return false
}
