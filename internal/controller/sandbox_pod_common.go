package controller

import (
	agentlandv1alpha1 "github.com/Fl0rencess720/agentland/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

const (
	sandboxJWTVolumeName = "sandbox-jwt-public-key"
	workspaceVolumeName  = "workspace"
	workspaceMountPath   = "/workspace"
)

func sandboxMainContainer(template *agentlandv1alpha1.SandboxTemplate, pullPolicy corev1.PullPolicy) corev1.Container {
	return corev1.Container{
		Name:            "main",
		Image:           template.Image,
		ImagePullPolicy: pullPolicy,
		Command:         template.Command,
		Args:            template.Args,
		Env:             template.Env,
		EnvFrom:         template.EnvFrom,
		VolumeMounts: []corev1.VolumeMount{{
			Name:      sandboxJWTVolumeName,
			MountPath: "/var/run/agentland/jwt",
			ReadOnly:  true,
		}, {
			Name:      workspaceVolumeName,
			MountPath: workspaceMountPath,
		}},
	}
}
