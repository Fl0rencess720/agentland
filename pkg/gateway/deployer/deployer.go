package deployer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
)

const defaultPort int32 = 8080

var (
	digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

type Config struct {
	Namespace       string
	BaseDomain      string
	IngressClass    string
	TLSSecret       string
	RuntimeClass    string
	ImagePullSecret string
	Port            int32
	Replicas        int32
	Timeout         time.Duration
	CPURequest      string
	MemoryRequest   string
	CPULimit        string
	MemoryLimit     string
}

type Request struct {
	ProjectID string
	ReleaseID string
	ImageRef  string
	Digest    string
}

type Result struct {
	URL            string `json:"deployment_url"`
	Hostname       string `json:"deployment_hostname"`
	DeploymentName string `json:"deployment_name"`
}

type Deployer struct {
	client      kubernetes.Interface
	config      Config
	waitRollout func(context.Context, string, int64) error
}

func New(config Config) (*Deployer, error) {
	restConfig, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return NewForClient(client, config)
}

func NewForClient(client kubernetes.Interface, config Config) (*Deployer, error) {
	if client == nil {
		return nil, errors.New("Kubernetes client is required")
	}
	config.Namespace = strings.TrimSpace(config.Namespace)
	config.BaseDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(config.BaseDomain)), ".")
	config.IngressClass = strings.TrimSpace(config.IngressClass)
	config.TLSSecret = strings.TrimSpace(config.TLSSecret)
	config.RuntimeClass = strings.TrimSpace(config.RuntimeClass)
	config.ImagePullSecret = strings.TrimSpace(config.ImagePullSecret)
	if config.Port == 0 {
		config.Port = defaultPort
	}
	if config.Replicas == 0 {
		config.Replicas = 1
	}
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Minute
	}
	if config.CPURequest == "" {
		config.CPURequest = "50m"
	}
	if config.MemoryRequest == "" {
		config.MemoryRequest = "64Mi"
	}
	if config.CPULimit == "" {
		config.CPULimit = "500m"
	}
	if config.MemoryLimit == "" {
		config.MemoryLimit = "512Mi"
	}
	if issues := validation.IsDNS1123Label(config.Namespace); len(issues) != 0 {
		return nil, fmt.Errorf("invalid application namespace: %s", strings.Join(issues, "; "))
	}
	if issues := validation.IsDNS1123Subdomain(config.BaseDomain); len(issues) != 0 {
		return nil, fmt.Errorf("invalid application base domain: %s", strings.Join(issues, "; "))
	}
	for name, value := range map[string]string{
		"ingress class": config.IngressClass, "TLS secret": config.TLSSecret,
		"runtime class": config.RuntimeClass, "image pull secret": config.ImagePullSecret,
	} {
		if value != "" {
			if issues := validation.IsDNS1123Subdomain(value); len(issues) != 0 {
				return nil, fmt.Errorf("invalid %s: %s", name, strings.Join(issues, "; "))
			}
		}
	}
	if config.Port < 1 || config.Port > 65535 || config.Replicas < 1 || config.Replicas > 20 {
		return nil, errors.New("application port or replica count is outside the allowed range")
	}
	if config.Timeout < 30*time.Second || config.Timeout > 24*time.Hour {
		return nil, errors.New("application deployment timeout must be between 30 seconds and 24 hours")
	}
	quantities := make(map[string]resource.Quantity, 4)
	for name, value := range map[string]string{
		"CPU request": config.CPURequest, "memory request": config.MemoryRequest,
		"CPU limit": config.CPULimit, "memory limit": config.MemoryLimit,
	} {
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", name, err)
		}
		quantities[name] = quantity
	}
	cpuRequest, cpuLimit := quantities["CPU request"], quantities["CPU limit"]
	memoryRequest, memoryLimit := quantities["memory request"], quantities["memory limit"]
	if cpuRequest.Cmp(cpuLimit) > 0 || memoryRequest.Cmp(memoryLimit) > 0 {
		return nil, errors.New("application resource requests cannot exceed limits")
	}
	d := &Deployer{client: client, config: config}
	d.waitRollout = d.waitForRollout
	return d, nil
}

func (d *Deployer) Deploy(ctx context.Context, request Request) (*Result, error) {
	request.ProjectID, request.ReleaseID = strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.ReleaseID)
	if !idPattern.MatchString(request.ProjectID) || !idPattern.MatchString(request.ReleaseID) || !digestPattern.MatchString(request.Digest) {
		return nil, errors.New("project, release and image digest are required")
	}
	image, err := immutableImage(request.ImageRef, request.Digest)
	if err != nil {
		return nil, err
	}
	name, hostname := applicationIdentity(request.ProjectID, d.config.BaseDomain)
	if issues := validation.IsDNS1123Subdomain(hostname); len(issues) != 0 {
		return nil, fmt.Errorf("generated application hostname is invalid: %s", strings.Join(issues, "; "))
	}
	deployments := d.client.AppsV1().Deployments(d.config.Namespace)
	previous, getErr := deployments.Get(ctx, name, metav1.GetOptions{})
	created := apierrors.IsNotFound(getErr)
	if getErr != nil && !created {
		return nil, fmt.Errorf("read existing application deployment: %w", getErr)
	}
	if err = d.reconcileService(ctx, name, request.ProjectID); err != nil {
		return nil, err
	}
	if err = d.reconcileIngress(ctx, name, hostname, request.ProjectID); err != nil {
		return nil, err
	}
	generation, err := d.reconcileDeployment(ctx, name, request, image)
	if err != nil {
		return nil, err
	}
	rolloutCtx, cancel := context.WithTimeout(ctx, d.config.Timeout)
	defer cancel()
	if err = d.waitRollout(rolloutCtx, name, generation); err != nil {
		d.rollback(context.WithoutCancel(ctx), name, previous, created)
		return nil, fmt.Errorf("application rollout failed: %w", err)
	}
	scheme := "http"
	if d.config.TLSSecret != "" {
		scheme = "https"
	}
	return &Result{URL: scheme + "://" + hostname, Hostname: hostname, DeploymentName: name}, nil
}

func applicationIdentity(projectID, baseDomain string) (string, string) {
	sum := sha256.Sum256([]byte(projectID))
	suffix := hex.EncodeToString(sum[:8])
	name := "app-" + suffix
	return name, name + "." + baseDomain
}

func immutableImage(imageRef, digest string) (string, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" || strings.ContainsAny(imageRef, " \t\r\n") || !digestPattern.MatchString(digest) {
		return "", errors.New("valid image reference and digest are required")
	}
	if at := strings.LastIndex(imageRef, "@"); at >= 0 {
		imageRef = imageRef[:at]
	}
	if colon, slash := strings.LastIndex(imageRef, ":"), strings.LastIndex(imageRef, "/"); colon > slash {
		imageRef = imageRef[:colon]
	}
	if imageRef == "" {
		return "", errors.New("image repository is empty")
	}
	return imageRef + "@" + digest, nil
}

func (d *Deployer) reconcileDeployment(ctx context.Context, name string, request Request, image string) (int64, error) {
	labels := applicationLabels(name)
	quantity := func(value string) resource.Quantity { return resource.MustParse(value) }
	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: d.config.Namespace, Labels: labels, Annotations: map[string]string{
			"agentland.dev/project-id": request.ProjectID, "agentland.dev/release-id": request.ReleaseID,
		}},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(d.config.Replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxUnavailable: ptr.To(intstr.FromInt32(0)), MaxSurge: ptr.To(intstr.FromInt32(1)),
			}},
			ProgressDeadlineSeconds: ptr.To(int32(d.config.Timeout.Seconds())),
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
				AutomountServiceAccountToken:  ptr.To(false),
				RuntimeClassName:              optionalString(d.config.RuntimeClass),
				SecurityContext:               &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				TerminationGracePeriodSeconds: ptr.To(int64(15)),
				ImagePullSecrets:              optionalPullSecret(d.config.ImagePullSecret),
				Volumes:                       []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				Containers: []corev1.Container{{
					Name: "application", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: d.config.Port}},
					Env:   []corev1.EnvVar{{Name: "PORT", Value: fmt.Sprint(d.config.Port)}, {Name: "HOST", Value: "0.0.0.0"}, {Name: "NODE_ENV", Value: "production"}},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: quantity(d.config.CPURequest), corev1.ResourceMemory: quantity(d.config.MemoryRequest)},
						Limits:   corev1.ResourceList{corev1.ResourceCPU: quantity(d.config.CPULimit), corev1.ResourceMemory: quantity(d.config.MemoryLimit)},
					},
					SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
					VolumeMounts:    []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
					StartupProbe:    &corev1.Probe{ProbeHandler: tcpProbe(d.config.Port), PeriodSeconds: 2, FailureThreshold: 60},
					ReadinessProbe:  &corev1.Probe{ProbeHandler: tcpProbe(d.config.Port), PeriodSeconds: 5, FailureThreshold: 3},
					LivenessProbe:   &corev1.Probe{ProbeHandler: tcpProbe(d.config.Port), PeriodSeconds: 10, FailureThreshold: 3},
				}},
			}},
		},
	}
	api := d.client.AppsV1().Deployments(d.config.Namespace)
	existing, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		created, createErr := api.Create(ctx, desired, metav1.CreateOptions{})
		if createErr != nil {
			return 0, fmt.Errorf("create application deployment: %w", createErr)
		}
		return created.Generation, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read application deployment: %w", err)
	}
	desired.ResourceVersion = existing.ResourceVersion
	updated, err := api.Update(ctx, desired, metav1.UpdateOptions{})
	if err != nil {
		return 0, fmt.Errorf("update application deployment: %w", err)
	}
	return updated.Generation, nil
}

func (d *Deployer) reconcileService(ctx context.Context, name, projectID string) error {
	labels := applicationLabels(name)
	desired := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: d.config.Namespace, Labels: labels, Annotations: map[string]string{"agentland.dev/project-id": projectID}}, Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeClusterIP, Selector: labels,
		Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(d.config.Port), Protocol: corev1.ProtocolTCP}},
	}}
	api := d.client.CoreV1().Services(d.config.Namespace)
	existing, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err = api.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create application service: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read application service: %w", err)
	}
	desired.ResourceVersion = existing.ResourceVersion
	desired.Spec.ClusterIP, desired.Spec.ClusterIPs = existing.Spec.ClusterIP, existing.Spec.ClusterIPs
	desired.Spec.IPFamilies, desired.Spec.IPFamilyPolicy = existing.Spec.IPFamilies, existing.Spec.IPFamilyPolicy
	if _, err = api.Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update application service: %w", err)
	}
	return nil
}

func (d *Deployer) reconcileIngress(ctx context.Context, name, hostname, projectID string) error {
	pathType := networkingv1.PathTypePrefix
	desired := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: d.config.Namespace, Labels: applicationLabels(name), Annotations: map[string]string{"agentland.dev/project-id": projectID}}, Spec: networkingv1.IngressSpec{
		IngressClassName: optionalString(d.config.IngressClass),
		Rules: []networkingv1.IngressRule{{Host: hostname, IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
			Path: "/", PathType: &pathType, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: name, Port: networkingv1.ServiceBackendPort{Number: 80}}},
		}}}}}},
	}}
	if d.config.TLSSecret != "" {
		desired.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{hostname}, SecretName: d.config.TLSSecret}}
	}
	api := d.client.NetworkingV1().Ingresses(d.config.Namespace)
	existing, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err = api.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create application ingress: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read application ingress: %w", err)
	}
	desired.ResourceVersion = existing.ResourceVersion
	if _, err = api.Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update application ingress: %w", err)
	}
	return nil
}

func (d *Deployer) waitForRollout(ctx context.Context, name string, generation int64) error {
	return wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		deployment, err := d.client.AppsV1().Deployments(d.config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, condition := range deployment.Status.Conditions {
			if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded" {
				return false, errors.New(condition.Message)
			}
		}
		replicas := d.config.Replicas
		return deployment.Status.ObservedGeneration >= generation && deployment.Status.UpdatedReplicas >= replicas &&
			deployment.Status.AvailableReplicas >= replicas && deployment.Status.UnavailableReplicas == 0, nil
	})
}

func (d *Deployer) rollback(ctx context.Context, name string, previous *appsv1.Deployment, created bool) {
	rollbackCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	api := d.client.AppsV1().Deployments(d.config.Namespace)
	if created {
		_ = api.Delete(rollbackCtx, name, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
		return
	}
	if previous == nil {
		return
	}
	current, err := api.Get(rollbackCtx, name, metav1.GetOptions{})
	if err != nil {
		return
	}
	previous.ResourceVersion = current.ResourceVersion
	_, _ = api.Update(rollbackCtx, previous, metav1.UpdateOptions{})
}

func applicationLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/part-of":    "agentland-applications",
		"app.kubernetes.io/managed-by": "agentland-gateway",
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return ptr.To(value)
}

func optionalPullSecret(value string) []corev1.LocalObjectReference {
	if value == "" {
		return nil
	}
	return []corev1.LocalObjectReference{{Name: value}}
}

func tcpProbe(port int32) corev1.ProbeHandler {
	return corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}
}
