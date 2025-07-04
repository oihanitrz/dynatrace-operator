package oneagent

import (
	"github.com/Dynatrace/dynatrace-operator/pkg/api/latest/dynakube"
	"github.com/Dynatrace/dynatrace-operator/pkg/util/kubeobjects/env"
	maputils "github.com/Dynatrace/dynatrace-operator/pkg/util/map"
	dtwebhook "github.com/Dynatrace/dynatrace-operator/pkg/webhook/mutation/pod/common"
	corev1 "k8s.io/api/core/v1"
		metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	CSIVolumeType       = "csi"
	EphemeralVolumeType = "ephemeral"

	isInjectedEnv = "DT_CM_INJECTED"
)

type Mutator struct {}

func NewMutator(webhookImageName string) dtwebhook.Mutator {
	return &Mutator{}
}

func IsSelfExtractingImage(mutationRequest *dtwebhook.BaseRequest, isCSI bool) bool {
	ffEnabled := mutationRequest.DynaKube.FF().IsNodeImagePull()

	return ffEnabled && !isCSI
}

func IsCSIVolume(mutationRequest *dtwebhook.BaseRequest) bool {
	defaultVolumeType := EphemeralVolumeType
	if mutationRequest.DynaKube.OneAgent().IsCSIAvailable() {
		defaultVolumeType = CSIVolumeType
	}

	return maputils.GetField(mutationRequest.Pod.Annotations, AnnotationVolumeType, defaultVolumeType) == CSIVolumeType
}

func IsEnabled(request *dtwebhook.BaseRequest) bool {
	enabledOnPod := maputils.GetFieldBool(request.Pod.Annotations, AnnotationInject, request.DynaKube.FF().IsAutomaticInjection())
	enabledOnDynakube := request.DynaKube.OneAgent().GetNamespaceSelector() != nil

	matchesNamespaceSelector := true // if no namespace selector is configured, we just pass set this to true

	if request.DynaKube.OneAgent().GetNamespaceSelector().Size() > 0 {
		selector, _ := metav1.LabelSelectorAsSelector(request.DynaKube.OneAgent().GetNamespaceSelector())

		matchesNamespaceSelector = selector.Matches(labels.Set(request.Namespace.Labels))
	}

	return matchesNamespaceSelector && enabledOnPod && enabledOnDynakube
}

func (mut *Mutator) IsEnabled(request *dtwebhook.BaseRequest) bool {
	return IsEnabled(request)
}

func (mut *Mutator) IsInjected(request *dtwebhook.BaseRequest) bool {
	return maputils.GetFieldBool(request.Pod.Annotations, AnnotationInjected, false)
}

func SetInjectedAnnotation(pod *corev1.Pod) {
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	pod.Annotations[AnnotationInjected] = "true"
}

func SetNotInjectedAnnotations(pod *corev1.Pod, reason string) {
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	pod.Annotations[AnnotationInjected] = "false"
	pod.Annotations[AnnotationReason] = reason
}


func (mut *Mutator) Mutate(request *dtwebhook.MutationRequest) error {
	installPath := maputils.GetField(request.Pod.Annotations, AnnotationInstallPath, DefaultInstallPath)

	err := mutateInitContainer(request, installPath)
	if err != nil {
		return err
	}

	mutateUserContainers(request.BaseRequest, installPath)

	return nil
}

func (mut *Mutator) Reinvoke(request *dtwebhook.ReinvocationRequest) bool {
	installPath := maputils.GetField(request.Pod.Annotations, AnnotationInstallPath, DefaultInstallPath)

	return mutateUserContainers(request.BaseRequest, installPath)
}

func containerIsInjected(container corev1.Container) bool {
	return env.IsIn(container.Env, isInjectedEnv)
}

func mutateUserContainers(request *dtwebhook.BaseRequest, installPath string) bool {
	newContainers := request.NewContainers(containerIsInjected)
	for i := range newContainers {
		container := newContainers[i]
		addOneAgentToContainer(request.DynaKube, container, request.Namespace, installPath)
	}

	return len(newContainers) > 0
}

func addOneAgentToContainer(dk dynakube.DynaKube, container *corev1.Container, namespace corev1.Namespace, installPath string) {
	log.Info("adding OneAgent to container", "name", container.Name)

	addVolumeMounts(container, installPath)
	AddDeploymentMetadataEnv(container, dk)
	AddPreloadEnv(container, installPath)

	if dk.Spec.NetworkZone != "" {
		AddNetworkZoneEnv(container, dk.Spec.NetworkZone)
	}

	if dk.FF().IsLabelVersionDetection() {
		AddVersionDetectionEnvs(container, namespace)
	}

	setIsInjectedEnv(container)
}

func setIsInjectedEnv(container *corev1.Container) {
	container.Env = append(container.Env,
		corev1.EnvVar{
			Name:  isInjectedEnv,
			Value: "true",
		},
	)
}
