package deployer

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDeployCreatesStableRuntimeResources(t *testing.T) {
	client := fake.NewSimpleClientset()
	d, err := NewForClient(client, Config{
		Namespace: "agentland-apps", BaseDomain: "apps.example.com", IngressClass: "nginx", TLSSecret: "wildcard-apps",
		ImagePullSecret: "registry-pull",
	})
	if err != nil {
		t.Fatal(err)
	}
	d.waitRollout = func(context.Context, string, int64) error { return nil }
	digest := "sha256:" + strings.Repeat("a", 64)
	result, err := d.Deploy(context.Background(), Request{
		ProjectID: "project_1", ReleaseID: "publication_1", ImageRef: "registry.example/team/project_1:publication_1", Digest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.URL != "https://"+result.Hostname || !strings.HasSuffix(result.Hostname, ".apps.example.com") {
		t.Fatalf("unexpected result: %+v", result)
	}
	deployment, err := client.AppsV1().Deployments("agentland-apps").Get(context.Background(), result.DeploymentName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	if container.Image != "registry.example/team/project_1@"+digest || container.Ports[0].ContainerPort != 8080 {
		t.Fatalf("unexpected application container: %+v", container)
	}
	if deployment.Spec.Template.Spec.AutomountServiceAccountToken == nil || *deployment.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("application pod must not mount a Kubernetes service account token")
	}
	service, err := client.CoreV1().Services("agentland-apps").Get(context.Background(), result.DeploymentName, metav1.GetOptions{})
	if err != nil || service.Spec.Ports[0].Port != 80 {
		t.Fatalf("unexpected service: %+v %v", service, err)
	}
	ingress, err := client.NetworkingV1().Ingresses("agentland-apps").Get(context.Background(), result.DeploymentName, metav1.GetOptions{})
	if err != nil || ingress.Spec.Rules[0].Host != result.Hostname || ingress.Spec.TLS[0].SecretName != "wildcard-apps" {
		t.Fatalf("unexpected ingress: %+v %v", ingress, err)
	}

	second, err := d.Deploy(context.Background(), Request{
		ProjectID: "project_1", ReleaseID: "publication_2", ImageRef: "registry.example/team/project_1:publication_2", Digest: "sha256:" + strings.Repeat("b", 64),
	})
	if err != nil || second.Hostname != result.Hostname {
		t.Fatalf("project hostname changed between releases: %+v %v", second, err)
	}
}

func TestDeployRemovesFirstDeploymentWhenRolloutFails(t *testing.T) {
	client := fake.NewSimpleClientset()
	d, err := NewForClient(client, Config{Namespace: "agentland-apps", BaseDomain: "apps.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	d.waitRollout = func(context.Context, string, int64) error { return errors.New("image pull failed") }
	_, err = d.Deploy(context.Background(), Request{
		ProjectID: "project_1", ReleaseID: "publication_1", ImageRef: "registry.example/team/project_1:publication_1", Digest: "sha256:" + strings.Repeat("a", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "image pull failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	name, _ := applicationIdentity("project_1", "apps.example.com")
	if _, getErr := client.AppsV1().Deployments("agentland-apps").Get(context.Background(), name, metav1.GetOptions{}); getErr == nil {
		t.Fatal("failed first rollout left a deployment behind")
	}
}

func TestNewForClientRejectsMissingDomain(t *testing.T) {
	_, err := NewForClient(fake.NewSimpleClientset(), Config{Namespace: "agentland-apps"})
	if err == nil {
		t.Fatal("missing application base domain was accepted")
	}
}
