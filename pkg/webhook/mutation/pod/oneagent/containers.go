package oneagent

import (
	"github.com/Dynatrace/dynatrace-operator/pkg/api/latest/dynakube"
	"github.com/Dynatrace/dynatrace-operator/pkg/util/kubeobjects/env"
	maputils "github.com/Dynatrace/dynatrace-operator/pkg/util/map"
	dtwebhook "github.com/Dynatrace/dynatrace-operator/pkg/webhook/mutation/pod/common"
	corev1 "k8s.io/api/core/v1"
)

const (
	isInjectedEnv = "DT_CM_INJECTED"
)

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
