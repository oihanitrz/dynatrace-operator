package pod

import (
	"context"
	"testing"

	"github.com/Dynatrace/dynatrace-operator/pkg/api/exp"
	"github.com/Dynatrace/dynatrace-operator/pkg/api/latest/dynakube"
	"github.com/Dynatrace/dynatrace-operator/pkg/api/latest/dynakube/activegate"
	"github.com/Dynatrace/dynatrace-operator/pkg/api/latest/dynakube/oneagent"
	"github.com/Dynatrace/dynatrace-operator/pkg/api/scheme/fake"
	"github.com/Dynatrace/dynatrace-operator/pkg/consts"
	"github.com/Dynatrace/dynatrace-operator/pkg/injection/namespace/bootstrapperconfig"
	"github.com/Dynatrace/dynatrace-operator/pkg/util/installconfig"
	"github.com/Dynatrace/dynatrace-operator/pkg/util/kubeobjects/container"
	dtwebhook "github.com/Dynatrace/dynatrace-operator/pkg/webhook/mutation/pod/common"
	"github.com/Dynatrace/dynatrace-operator/pkg/webhook/mutation/pod/common/events"
	oacommon "github.com/Dynatrace/dynatrace-operator/pkg/webhook/mutation/pod/oneagent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	customImage = "custom-image"
)

func TestIsEnabled(t *testing.T) {
	type testCase struct {
		title      string
		podMods    func(*corev1.Pod)
		dkMods     func(*dynakube.DynaKube)
		withCSI    bool
		withoutCSI bool
	}

	cases := []testCase{
		{
			title:      "nothing enabled => not enabled",
			podMods:    func(p *corev1.Pod) {},
			dkMods:     func(dk *dynakube.DynaKube) {},
			withCSI:    false,
			withoutCSI: false,
		},

		{
			title:   "only OA enabled, without FF => not enabled",
			podMods: func(p *corev1.Pod) {},
			dkMods: func(dk *dynakube.DynaKube) {
				dk.Spec.OneAgent.ApplicationMonitoring = &oneagent.ApplicationMonitoringSpec{}
			},
			withCSI:    false,
			withoutCSI: false,
		},

		{
			title:   "OA + FF enabled => enabled with no csi",
			podMods: func(p *corev1.Pod) {},
			dkMods: func(dk *dynakube.DynaKube) {
				dk.Spec.OneAgent.ApplicationMonitoring = &oneagent.ApplicationMonitoringSpec{}
				dk.Annotations = map[string]string{exp.OANodeImagePullKey: "true"}
			},
			withCSI:    false,
			withoutCSI: true,
		},
		{
			title: "OA + FF enabled + correct Volume-Type => enabled",
			podMods: func(p *corev1.Pod) {
				p.Annotations = map[string]string{oacommon.AnnotationVolumeType: oacommon.EphemeralVolumeType}
			},
			dkMods: func(dk *dynakube.DynaKube) {
				dk.Spec.OneAgent.ApplicationMonitoring = &oneagent.ApplicationMonitoringSpec{}
				dk.Annotations = map[string]string{exp.OANodeImagePullKey: "true"}
			},
			withCSI:    true,
			withoutCSI: true,
		},
		{
			title: "OA + FF enabled + incorrect Volume-Type => disabled",
			podMods: func(p *corev1.Pod) {
				p.Annotations = map[string]string{oacommon.AnnotationVolumeType: oacommon.CSIVolumeType}
			},
			dkMods: func(dk *dynakube.DynaKube) {
				dk.Spec.OneAgent.ApplicationMonitoring = &oneagent.ApplicationMonitoringSpec{}
				dk.Annotations = map[string]string{exp.OANodeImagePullKey: "true"}
			},
			withCSI:    false,
			withoutCSI: false,
		},
	}
	for _, test := range cases {
		t.Run(test.title, func(t *testing.T) {
			pod := &corev1.Pod{}
			test.podMods(pod)

			dk := &dynakube.DynaKube{}
			test.dkMods(dk)

			req := &dtwebhook.MutationRequest{BaseRequest: &dtwebhook.BaseRequest{Pod: pod, DynaKube: *dk}}

			assert.Equal(t, test.withCSI, IsEnabled(req))

			installconfig.SetModulesOverride(t, installconfig.Modules{CSIDriver: false})

			assert.Equal(t, test.withoutCSI, IsEnabled(req))
		})
	}
}

func TestHandleImpl(t *testing.T) {
	initSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      consts.BootstrapperInitSecretName,
			Namespace: testNamespaceName,
		},
	}

	certsSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      consts.BootstrapperInitCertsSecretName,
			Namespace: testNamespaceName,
		},
	}

	t.Run("no init secret + no init secret source => no injection + only annotation", func(t *testing.T) {
		injector := createTestWebhookBase()
		clt := fake.NewClient()
		injector.apiReader = clt

		request := createTestMutationRequest(getTestDynakube())

		err := injector.handle(request)
		require.NoError(t, err)

		isInjected, ok := request.Pod.Annotations[oacommon.AnnotationInjected]
		require.True(t, ok)
		assert.Equal(t, "false", isInjected)

		reason, ok := request.Pod.Annotations[oacommon.AnnotationReason]
		require.True(t, ok)
		assert.Equal(t, NoBootstrapperConfigReason, reason)
	})

	t.Run("no init secret and no certs + source (both) => replicate (both) + inject", func(t *testing.T) {
		injector := createTestWebhookBase()
		request := createTestMutationRequest(getTestDynakubeWithAGCerts())

		source := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bootstrapperconfig.GetSourceConfigSecretName(request.DynaKube.Name),
				Namespace: request.DynaKube.Namespace,
			},
			Data: map[string][]byte{"data": []byte("beep")},
		}
		sourceCerts := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bootstrapperconfig.GetSourceCertsSecretName(request.DynaKube.Name),
				Namespace: request.DynaKube.Namespace,
			},
			Data: map[string][]byte{"certs": []byte("very secure")},
		}
		clt := fake.NewClient(&source, &sourceCerts)
		injector.kubeClient = clt
		injector.apiReader = clt

		err := injector.handle(request)
		require.NoError(t, err)

		var replicated corev1.Secret
		err = clt.Get(context.Background(), client.ObjectKey{Name: consts.BootstrapperInitSecretName, Namespace: request.Namespace.Name}, &replicated)
		require.NoError(t, err)
		assert.Equal(t, source.Data, replicated.Data)

		var replicatedCerts corev1.Secret
		err = clt.Get(context.Background(), client.ObjectKey{Name: consts.BootstrapperInitCertsSecretName, Namespace: request.Namespace.Name}, &replicatedCerts)
		require.NoError(t, err)
		assert.Equal(t, sourceCerts.Data, replicatedCerts.Data)

		isInjected, ok := request.Pod.Annotations[oacommon.AnnotationInjected]
		require.True(t, ok)
		assert.Equal(t, "true", isInjected)

		_, ok = request.Pod.Annotations[oacommon.AnnotationReason]
		require.False(t, ok)
	})

	t.Run("no init and no certs, but don't replicate certs because we don't need it (AG is not enabled)", func(t *testing.T) {
		injector := createTestWebhookBase()
		request := createTestMutationRequest(getTestDynakube())

		source := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bootstrapperconfig.GetSourceConfigSecretName(request.DynaKube.Name),
				Namespace: request.DynaKube.Namespace,
			},
			Data: map[string][]byte{"data": []byte("beep")},
		}

		sourceCerts := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bootstrapperconfig.GetSourceCertsSecretName(request.DynaKube.Name),
				Namespace: request.DynaKube.Namespace,
			},
			Data: map[string][]byte{"certs": []byte("very secure")},
		}
		clt := fake.NewClient(&source, &sourceCerts)
		injector.kubeClient = clt
		injector.apiReader = clt

		err := injector.handle(request)
		require.NoError(t, err)

		var replicated corev1.Secret
		err = clt.Get(context.Background(), client.ObjectKey{Name: consts.BootstrapperInitSecretName, Namespace: request.Namespace.Name}, &replicated)
		require.NoError(t, err)
		assert.Equal(t, source.Data, replicated.Data)

		var replicatedCerts corev1.Secret
		err = clt.Get(context.Background(), client.ObjectKey{Name: consts.BootstrapperInitCertsSecretName, Namespace: request.Namespace.Name}, &replicatedCerts)
		require.Error(t, err)
		require.True(t, k8sErrors.IsNotFound(err))
		assert.Empty(t, replicatedCerts.Data)

		isInjected, ok := request.Pod.Annotations[oacommon.AnnotationInjected]
		require.True(t, ok)
		assert.Equal(t, "true", isInjected)

		_, ok = request.Pod.Annotations[oacommon.AnnotationReason]
		require.False(t, ok)
	})

	t.Run("no codeModulesImage => no injection + only annotation", func(t *testing.T) {
		injector := createTestWebhookBase()
		injector.apiReader = fake.NewClient(&initSecret, &certsSecret)

		request := createTestMutationRequest(&dynakube.DynaKube{})

		err := injector.handle(request)
		require.NoError(t, err)

		isInjected, ok := request.Pod.Annotations[oacommon.AnnotationInjected]
		require.True(t, ok)
		assert.Equal(t, "false", isInjected)

		reason, ok := request.Pod.Annotations[oacommon.AnnotationReason]
		require.True(t, ok)
		assert.Equal(t, NoCodeModulesImageReason, reason)
	})

	t.Run("happy path", func(t *testing.T) {
		injector := createTestWebhookBase()
		injector.apiReader = fake.NewClient(&initSecret, &certsSecret)

		request := createTestMutationRequest(getTestDynakube())

		err := injector.handle(request)
		require.NoError(t, err)

		isInjected, ok := request.Pod.Annotations[oacommon.AnnotationInjected]
		require.True(t, ok)
		assert.Equal(t, "true", isInjected)

		_, ok = request.Pod.Annotations[oacommon.AnnotationReason]
		require.False(t, ok)

		installContainer := container.FindInitContainerInPodSpec(&request.Pod.Spec, dtwebhook.InstallContainerName)
		require.NotNil(t, installContainer)
		assert.Len(t, installContainer.Env, 3)
		assert.Len(t, installContainer.Args, 15)
	})
}

func TestIsInjected(t *testing.T) {
	t.Run("init-container present == injected", func(t *testing.T) {
		injector := createTestWebhookBase()

		assert.True(t, injector.isInjected(createTestMutationRequestWithInjectedPod(getTestDynakube())))
	})

	t.Run("init-container NOT present != injected", func(t *testing.T) {
		injector := createTestWebhookBase()

		assert.False(t, injector.isInjected(createTestMutationRequest(getTestDynakube())))
	})
}

func createTestWebhookBase() *webhook {
	return &webhook{
		recorder: events.NewRecorder(record.NewFakeRecorder(10)),
	}
}

func getTestDynakubeWithAGCerts() *dynakube.DynaKube {
	dk := getTestDynakube()
	dk.Spec.ActiveGate = activegate.Spec{
		Capabilities: []activegate.CapabilityDisplayName{
			activegate.DynatraceAPICapability.DisplayName,
		},
		TLSSecretName: "ag-certs",
	}

	return dk
}

var testResourceRequirements = corev1.ResourceRequirements{
	Limits: map[corev1.ResourceName]resource.Quantity{
		corev1.ResourceCPU:    resource.MustParse("100m"),
		corev1.ResourceMemory: resource.MustParse("100Mi"),
	},
}

func getTestDynakubeNoInitLimits() *dynakube.DynaKube {
	return &dynakube.DynaKube{
		ObjectMeta: getTestDynakubeMeta(),
		Spec: dynakube.DynaKubeSpec{
			OneAgent: getAppMonSpec(nil),
		},
	}
}

func getAppMonSpec(initResources *corev1.ResourceRequirements) oneagent.Spec {
	return oneagent.Spec{
		ApplicationMonitoring: &oneagent.ApplicationMonitoringSpec{
			AppInjectionSpec: oneagent.AppInjectionSpec{
				InitResources:    initResources,
				CodeModulesImage: customImage,
			}},
	}
}

func createTestMutationRequestWithInjectedPod(dk *dynakube.DynaKube) *dtwebhook.MutationRequest {
	return dtwebhook.NewMutationRequest(context.Background(), *getTestNamespace(), nil, getInjectedPod(), *dk)
}

func getInjectedPod() *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPodName,
			Namespace: testNamespaceName,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            "container",
					Image:           "alpine",
					SecurityContext: getTestSecurityContext(),
				},
			},
			InitContainers: []corev1.Container{
				{
					Name:  "init-container",
					Image: "alpine",
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "volume",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
		},
	}
	installContainer := createInitContainerBase(pod, *getTestDynakube(), false)
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, *installContainer)

	return pod
}

func TestSetDynatraceInjectedAnnotation(t *testing.T) {
	t.Run("add annotation", func(t *testing.T) {
		request := dtwebhook.MutationRequest{
			BaseRequest: &dtwebhook.BaseRequest{
				Pod: &corev1.Pod{},
			},
		}

		setDynatraceInjectedAnnotation(&request)

		require.Len(t, request.Pod.Annotations, 1)
		assert.Equal(t, "true", request.Pod.Annotations[dtwebhook.AnnotationDynatraceInjected])
	})

	t.Run("remove reason annotation", func(t *testing.T) {
		request := dtwebhook.MutationRequest{
			BaseRequest: &dtwebhook.BaseRequest{
				Pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							dtwebhook.AnnotationDynatraceReason: "beep",
						},
					},
				},
			},
		}

		setDynatraceInjectedAnnotation(&request)

		require.Len(t, request.Pod.Annotations, 1)
		assert.Equal(t, "true", request.Pod.Annotations[dtwebhook.AnnotationDynatraceInjected])
	})
}
