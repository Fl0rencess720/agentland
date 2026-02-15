package controller

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	defaultHarudImage = "harud:latest"
	defaultHarudPort  = int32(1885)

	sandboxJWTVolumeName = "sandbox-jwt-public-key"
	workspaceVolumeName  = "workspace"
	workspaceMountPath   = "/workspace"
	harudContainerName   = "harud"
)

func harudImageOrDefault(image string) string {
	trimmed := strings.TrimSpace(image)
	if trimmed == "" {
		return defaultHarudImage
	}
	return trimmed
}

func harudPortOrDefault(port int32) int32 {
	if port <= 0 || port > 65535 {
		return defaultHarudPort
	}
	return port
}

func buildHarudContainer(image string, port int32) corev1.Container {
	return corev1.Container{
		Name:            harudContainerName,
		Image:           harudImageOrDefault(image),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{{
			Name:          "harud-http",
			ContainerPort: harudPortOrDefault(port),
		}},
		Env: []corev1.EnvVar{{
			Name:  "AL_HARUD_WORKSPACE_ROOT",
			Value: workspaceMountPath,
		}},
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
