package kubernetes

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

func TestDeploymentUsesAuthAwareReadinessAndWritableSeed(t *testing.T) {
	data, err := os.ReadFile("deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var deployment struct {
		Spec struct {
			Replicas int `yaml:"replicas"`
			Template struct {
				Spec struct {
					InitContainers []struct {
						Name         string  `yaml:"name"`
						VolumeMounts []mount `yaml:"volumeMounts"`
					} `yaml:"initContainers"`
					Containers []struct {
						Name         string  `yaml:"name"`
						Liveness     probe   `yaml:"livenessProbe"`
						Readiness    probe   `yaml:"readinessProbe"`
						VolumeMounts []mount `yaml:"volumeMounts"`
					} `yaml:"containers"`
					Volumes []struct {
						Name     string         `yaml:"name"`
						EmptyDir map[string]any `yaml:"emptyDir"`
					} `yaml:"volumes"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &deployment); err != nil {
		t.Fatalf("parse deployment: %v", err)
	}

	if deployment.Spec.Replicas != 1 {
		t.Fatalf("replicas = %d, want 1", deployment.Spec.Replicas)
	}
	pod := deployment.Spec.Template.Spec
	if len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != "seed-codex-auth" {
		t.Fatalf("init containers = %#v", pod.InitContainers)
	}
	if len(pod.Containers) != 1 || pod.Containers[0].Name != "codex-chat-api" {
		t.Fatalf("containers = %#v", pod.Containers)
	}
	container := pod.Containers[0]
	if container.Liveness.HTTPGet.Path != "/health/live" {
		t.Fatalf("liveness path = %q", container.Liveness.HTTPGet.Path)
	}
	if container.Readiness.HTTPGet.Path != "/ready" || container.Readiness.FailureThreshold != 1 {
		t.Fatalf("readiness = %#v", container.Readiness)
	}
	if !hasMount(pod.InitContainers[0].VolumeMounts, "codex-auth-runtime", "/var/run/codex-auth") ||
		!hasMount(container.VolumeMounts, "codex-auth-runtime", "/var/run/codex-auth") {
		t.Fatal("init and application must share the writable auth runtime volume")
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0].Name != "codex-auth-runtime" || pod.Volumes[0].EmptyDir == nil {
		t.Fatalf("volumes = %#v, want codex-auth-runtime emptyDir", pod.Volumes)
	}
}

func TestReleaseWorkflowInstallsRuntimeHardeningPatch(t *testing.T) {
	workflow, err := os.ReadFile("../../.github/workflows/docker.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflow)
	for _, want := range []string{
		"path: k8s-control",
		"deploy/kubernetes/runtime-hardening-patch.yaml",
		"codex-chat-api-runtime-hardening.yaml",
		`git add "$FILE" "$PATCH"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q", want)
		}
	}

	patch, err := os.ReadFile("runtime-hardening-patch.yaml")
	if err != nil {
		t.Fatal(err)
	}
	patchText := string(patch)
	for _, want := range []string{"path: /ready", "emptyDir: {}", "key: CODEX_AUTH_JSON"} {
		if !strings.Contains(patchText, want) {
			t.Fatalf("runtime hardening patch missing %q", want)
		}
	}
}

type probe struct {
	HTTPGet struct {
		Path string `yaml:"path"`
	} `yaml:"httpGet"`
	FailureThreshold int `yaml:"failureThreshold"`
}

type mount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
}

func hasMount(mounts []mount, name, path string) bool {
	for _, item := range mounts {
		if item.Name == name && item.MountPath == path {
			return true
		}
	}
	return false
}
