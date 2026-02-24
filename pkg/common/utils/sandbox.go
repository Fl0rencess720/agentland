package utils

import (
	"fmt"
	"hash/fnv"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	PoolLabel         = "agentland.fl0rencess720.app/pool"
	ProfileHashLabel  = "agentland.fl0rencess720.app/profile-hash"
	SandboxLabel      = "agentland.fl0rencess720.app/sandbox-name-hash"
	ClaimUIDLabel     = "agentland.fl0rencess720.app/claim-uid"
	PodNameAnnotation = "agentland.fl0rencess720.app/pod-name"
)

const (
	DefaultRequeueInterval  = 500 * time.Millisecond
	ConflictRequeueInterval = 100 * time.Millisecond
	FallbackRequeueInterval = 2 * time.Second
)

func NameHash(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return fmt.Sprintf("%x", h.Sum32())
}

func IsPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
