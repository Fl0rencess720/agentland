package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	PoolLabel                   = "agentland.fl0rencess720.app/pool"
	ProfileHashLabel            = "agentland.fl0rencess720.app/profile-hash"
	SandboxLabel                = "agentland.fl0rencess720.app/sandbox-name-hash"
	ClaimUIDLabel               = "agentland.fl0rencess720.app/claim-uid"
	PodNameAnnotation           = "agentland.fl0rencess720.app/pod-name"
	PoolBackfillTouchAnnotation = "agentland.fl0rencess720.app/pool-backfill-touch-at"
)

const (
	DefaultRequeueInterval  = 500 * time.Millisecond
	ConflictRequeueInterval = 100 * time.Millisecond
	FallbackRequeueInterval = 2 * time.Second
)

const nameHashBytes = 16

func NameHash(name string) string {
	// Use SHA-256 and truncate to 128 bits (32 hex chars) to stay well within
	// the Kubernetes label value limit while providing strong collision resistance.
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:nameHashBytes])
}

func IsPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
