package config

import "k8s.io/client-go/dynamic"

type Config struct {
	Port string `json:"port"`

	K8sClient *dynamic.DynamicClient

	KorokdImage string

	WarmPoolEnabled     bool
	WarmPoolDefaultMode string
	WarmPoolPoolRef     string
	WarmPoolProfile     string
}
