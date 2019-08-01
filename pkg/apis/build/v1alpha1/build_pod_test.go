package v1alpha1_test

import (
	"fmt"
	"testing"

	"github.com/knative/pkg/kmeta"
	"github.com/sclevine/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/pivotal/build-service-system/pkg/apis/build/v1alpha1"
)

func TestBuildPod(t *testing.T) {
	spec.Run(t, "Test Build Pod", testBuildPod)
}

func testBuildPod(t *testing.T, when spec.G, it spec.S) {
	const (
		namespace      = "some-namespace"
		buildName      = "build-name"
		builderImage   = "somebuilder/123"
		serviceAccount = "someserviceaccount"
	)
	resources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("256M"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("128M"),
		},
	}

	build := &v1alpha1.Build{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildName,
			Namespace: namespace,
			Labels: map[string]string{
				"some/label": "to-pass-through",
			},
		},
		Spec: v1alpha1.BuildSpec{
			Image:          "someimage/name",
			ServiceAccount: serviceAccount,
			Builder:        builderImage,
			Env: []corev1.EnvVar{
				{Name: "keyA", Value: "valueA"},
				{Name: "keyB", Value: "valueB"},
			},
			Resources: resources,
			Source: v1alpha1.Source{
				Git: v1alpha1.Git{
					URL:      "giturl.com/git.git",
					Revision: "gitrev1234",
				},
			},
			CacheName:            "some-cache-name",
			AdditionalImageNames: []string{"someimage/name:tag2", "someimage/name:tag3"},
		},
	}

	secrets := []corev1.Secret{
		{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name: "git-secret-1",
				Annotations: map[string]string{
					v1alpha1.GITSecretAnnotationPrefix: "https://github.com",
				},
			},
			StringData: map[string]string{
				"username": "username",
				"password": "password",
			},
			Type: corev1.SecretTypeBasicAuth,
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "docker-secret-1",
				Annotations: map[string]string{
					v1alpha1.DOCKERSecretAnnotationPrefix: "acr.io",
				},
			},
			Type: corev1.SecretTypeBasicAuth,
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "random-secret-1",
			},
			Type: corev1.SecretTypeBasicAuth,
		},
	}
	config := v1alpha1.BuildPodConfig{
		GitInitImage:   "git/init:image",
		BuildInitImage: "build/init:image",
		CredsInitImage: "creds/init:image",
		NopImage:       "no/op:image",
	}

	when("BuildPod", func() {
		it("creates a pod with a builder owner reference", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, pod.ObjectMeta, metav1.ObjectMeta{
				Name:      build.PodName(),
				Namespace: namespace,
				Labels: map[string]string{
					"some/label": "to-pass-through",
				},
				OwnerReferences: []metav1.OwnerReference{
					*kmeta.NewControllerRef(build),
				},
			})
		})

		it("creates a pod with a correct service account", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, serviceAccount, pod.Spec.ServiceAccountName)
		})

		it("creates init containers with all the build steps", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Len(t, pod.Spec.InitContainers, len([]string{
				"creds-init",
				"git-init",
				"prepare",
				"detect",
				"restore",
				"analyze",
				"build",
				"export",
				"cache",
			}))
		})

		it("configures creds init", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, pod.Spec.InitContainers[0].Name, "creds-init")
			assert.Equal(t, pod.Spec.InitContainers[0].Image, config.CredsInitImage)
			assert.Equal(t, []string{
				"-basic-git=git-secret-1=https://github.com",
				"-basic-docker=docker-secret-1=acr.io",
			}, pod.Spec.InitContainers[0].Args)
			assert.Equal(t, []corev1.VolumeMount{
				{
					Name:      "secret-volume-git-secret-1",
					MountPath: "/var/build-secrets/git-secret-1",
				},
				{
					Name:      "secret-volume-docker-secret-1",
					MountPath: "/var/build-secrets/docker-secret-1",
				},
				{
					Name:      "home-dir",
					MountPath: "/builder/home",
				},
			}, pod.Spec.InitContainers[0].VolumeMounts)
		})

		it("configures git init with the build's source", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, pod.Spec.InitContainers[1].Name, "git-init")
			assert.Equal(t, pod.Spec.InitContainers[1].Image, config.GitInitImage)
			assert.Equal(t, pod.Spec.InitContainers[1].Args, []string{
				"-url",
				build.Spec.Source.Git.URL,
				"-revision",
				build.Spec.Source.Git.Revision,
			})
		})

		it("configures prepare step with the build setup", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, pod.Spec.InitContainers[2].Name, "prepare")
			assert.Equal(t, pod.Spec.InitContainers[2].Image, config.BuildInitImage)
			assert.Equal(t, pod.Spec.InitContainers[2].Env, []corev1.EnvVar{
				{
					Name:  "BUILDER",
					Value: build.Spec.Builder,
				},
				{
					Name:  "PLATFORM_ENV_VARS",
					Value: `[{"name":"keyA","value":"valueA"},{"name":"keyB","value":"valueB"}]`,
				},
				{
					Name:  "HOME",
					Value: "/builder/home",
				},
			})
			assert.Len(t, pod.Spec.InitContainers[2].VolumeMounts, len([]string{
				"layers-dir",
				"cache-dir",
				"platform-dir",
				"workspace-dir",
				"home-dir",
			}))
		})

		it("configures detect step", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, pod.Spec.InitContainers[3].Name, "detect")
			assert.Equal(t, pod.Spec.InitContainers[3].Image, builderImage)
			assert.Len(t, pod.Spec.InitContainers[3].VolumeMounts, len([]string{
				"layers-dir",
				"platform-dir",
				"workspace-dir",
			}))
		})

		it("configures restore step", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, pod.Spec.InitContainers[4].Name, "restore")
			assert.Equal(t, pod.Spec.InitContainers[4].Image, builderImage)
			assert.Len(t, pod.Spec.InitContainers[4].VolumeMounts, len([]string{
				"layers-dir",
				"home-dir",
			}))
		})

		it("configures analyze step", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, pod.Spec.InitContainers[5].Name, "analyze")
			assert.Equal(t, pod.Spec.InitContainers[5].Image, builderImage)
			assert.Len(t, pod.Spec.InitContainers[5].VolumeMounts, len([]string{
				"layers-dir",
				"workspace-dir",
				"home-dir",
			}))
			assert.Equal(t, []string{
				"-layers=/layers",
				"-helpers=false",
				"-group=/layers/group.toml",
				"-analyzed=/layers/analyzed.toml",
				build.Spec.Image,
			}, pod.Spec.InitContainers[5].Args)
		})

		it("configures build step", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, pod.Spec.InitContainers[6].Name, "build")
			assert.Equal(t, pod.Spec.InitContainers[6].Image, builderImage)
			assert.Len(t, pod.Spec.InitContainers[6].VolumeMounts, len([]string{
				"layers-dir",
				"platform-dir",
				"workspace-dir",
			}))
		})

		it("configures export step", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, pod.Spec.InitContainers[7].Name, "export")
			assert.Equal(t, pod.Spec.InitContainers[7].Image, builderImage)
			assert.Len(t, pod.Spec.InitContainers[7].VolumeMounts, len([]string{
				"layers-dir",
				"workspace-dir",
				"home-dir",
			}))
			assert.Equal(t, []string{
				"-layers=/layers",
				"-helpers=false",
				"-app=/workspace",
				"-group=/layers/group.toml",
				"-analyzed=/layers/analyzed.toml",
				build.Spec.Image,
				"someimage/name:tag2",
				"someimage/name:tag3",
			}, pod.Spec.InitContainers[7].Args)
		})

		it("configures cache step", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assert.Equal(t, pod.Spec.InitContainers[8].Name, "cache")
			assert.Equal(t, pod.Spec.InitContainers[8].Image, builderImage)
			assert.Len(t, pod.Spec.InitContainers[8].VolumeMounts, len([]string{
				"layers-dir",
				"cache-dir",
			}))
		})

		it("configures the builder image and resources in all lifecycle steps", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			for _, container := range pod.Spec.InitContainers {
				if container.Name != "creds-init" && container.Name != "git-init" && container.Name != "prepare" {
					assert.Equal(t, builderImage, container.Image, fmt.Sprintf("image on container '%s'", container.Name))
					assert.Equal(t, resources, container.Resources, fmt.Sprintf("resources on container '%s'", container.Name))
				}
			}
		})

		it("creates a pod with reusable cache when name is provided", func() {
			pod, err := build.BuildPod(config, nil)
			require.NoError(t, err)

			require.Len(t, pod.Spec.Volumes, 5)
			assert.Equal(t, corev1.Volume{
				Name: "cache-dir",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "some-cache-name"},
				},
			}, pod.Spec.Volumes[0])
		})

		it("creates a pod with empty cache when no name is provided", func() {
			build.Spec.CacheName = ""
			pod, err := build.BuildPod(config, nil)
			require.NoError(t, err)

			require.Len(t, pod.Spec.Volumes, 5)
			assert.Equal(t, corev1.Volume{
				Name: "cache-dir",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}, pod.Spec.Volumes[0])
		})

		it("attach volumes for secrets", func() {
			pod, err := build.BuildPod(config, secrets)
			require.NoError(t, err)

			assertSecretPresent(t, pod, "git-secret-1")
			assertSecretPresent(t, pod, "docker-secret-1")
			assertSecretNotPresent(t, pod, "random-secret-1")
		})
	})
}

func assertSecretPresent(t *testing.T, pod *corev1.Pod, secretName string) {
	assert.True(t, isSecretPresent(t, pod, secretName), fmt.Sprintf("secret '%s' not present", secretName))
}

func assertSecretNotPresent(t *testing.T, pod *corev1.Pod, secretName string) {
	assert.False(t, isSecretPresent(t, pod, secretName), fmt.Sprintf("secret '%s' not present", secretName))
}

func isSecretPresent(t *testing.T, pod *corev1.Pod, secretName string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == fmt.Sprintf(v1alpha1.SecretTemplateName, secretName) {
			assert.Equal(t, corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: secretName,
				},
			}, volume.VolumeSource)
			return true
		}
	}
	return false
}