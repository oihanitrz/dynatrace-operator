package pod

import (
	"fmt"

	"github.com/Dynatrace/dynatrace-operator/pkg/consts"
	"github.com/Dynatrace/dynatrace-operator/pkg/injection/namespace/bootstrapperconfig"
	"github.com/Dynatrace/dynatrace-operator/pkg/util/kubeobjects/container"
	"github.com/Dynatrace/dynatrace-operator/pkg/util/kubeobjects/secret"
	maputils "github.com/Dynatrace/dynatrace-operator/pkg/util/map"
	dtwebhook "github.com/Dynatrace/dynatrace-operator/pkg/webhook"
	metacommon "github.com/Dynatrace/dynatrace-operator/pkg/webhook/mutation/pod/metadata"
	oacommon "github.com/Dynatrace/dynatrace-operator/pkg/webhook/mutation/pod/oneagent"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func IsEnabled(mutationRequest *dtwebhook.MutationRequest) bool {
	ffEnabled := mutationRequest.DynaKube.FF().IsNodeImagePull()
	oaEnabled := oacommon.IsEnabled(mutationRequest.BaseRequest)

	defaultVolumeType := oacommon.EphemeralVolumeType
	if mutationRequest.DynaKube.OneAgent().IsCSIAvailable() {
		defaultVolumeType = oacommon.CSIVolumeType
	}

	correctVolumeType := maputils.GetField(mutationRequest.Pod.Annotations, oacommon.AnnotationVolumeType, defaultVolumeType) == oacommon.EphemeralVolumeType

	return ffEnabled && oaEnabled && correctVolumeType
}

func (wh *webhook) handle(mutationRequest *dtwebhook.MutationRequest) error {
	wh.recorder.Setup(mutationRequest)

	if !wh.isInputSecretPresent(mutationRequest, bootstrapperconfig.GetSourceConfigSecretName(mutationRequest.DynaKube.Name), consts.BootstrapperInitSecretName) {
		return nil
	}

	if mutationRequest.DynaKube.IsAGCertificateNeeded() || mutationRequest.DynaKube.Spec.TrustedCAs != "" {
		if !wh.isInputSecretPresent(mutationRequest, bootstrapperconfig.GetSourceCertsSecretName(mutationRequest.DynaKube.Name), consts.BootstrapperInitCertsSecretName) {
			return nil
		}
	}

	if wh.isInjected(mutationRequest) {
		if wh.handlePodReinvocation(mutationRequest) {
			log.Info("reinvocation policy applied", "podName", mutationRequest.PodName())
			wh.recorder.SendPodUpdateEvent()

			return nil
		}

		log.Info("no change, all containers already injected", "podName", mutationRequest.PodName())
	} else {
		if err := wh.handlePodMutation(mutationRequest); err != nil {
			return err
		}
	}

	setDynatraceInjectedAnnotation(mutationRequest)

	log.Info("injection finished for pod", "podName", mutationRequest.PodName(), "namespace", mutationRequest.Namespace.Name)

	return nil
}

func (wh *webhook) isInjected(mutationRequest *dtwebhook.MutationRequest) bool {
	installContainer := container.FindInitContainerInPodSpec(&mutationRequest.Pod.Spec, dtwebhook.InstallContainerName)
	if installContainer != nil {
		log.Info("Dynatrace init-container already present, skipping mutation, doing reinvocation", "containerName", dtwebhook.InstallContainerName)

		return true
	}

	return false
}

func (wh *webhook) handlePodMutation(mutationRequest *dtwebhook.MutationRequest) error {
	mutationRequest.InstallContainer = createInitContainerBase(mutationRequest.Pod, mutationRequest.DynaKube, wh.isOpenShift)

	err := addContainerAttributes(mutationRequest)
	if err != nil {
		return err
	}

	updated := oacommon.Mutate(mutationRequest)
	if !updated {
		oacommon.SetNotInjectedAnnotations(mutationRequest.Pod, NoMutationNeededReason)
	}

	err = addPodAttributes(mutationRequest)
	if err != nil {
		log.Info("failed to add pod attributes to init-container")

		return err
	}

	err = metacommon.Mutate(wh.metaClient, mutationRequest)
	if err != nil {
		return err
	}

	oacommon.SetInjectedAnnotation(mutationRequest.Pod)

	addInitContainerToPod(mutationRequest.Pod, mutationRequest.InstallContainer)
	wh.recorder.SendPodInjectEvent()

	return nil
}

func (wh *webhook) handlePodReinvocation(mutationRequest *dtwebhook.MutationRequest) bool {
	mutationRequest.InstallContainer = container.FindInitContainerInPodSpec(&mutationRequest.Pod.Spec, dtwebhook.InstallContainerName)

	err := addContainerAttributes(mutationRequest)
	if err != nil {
		log.Error(err, "error during reinvocation for updating the init-container, failed to update container-attributes on the init container")

		return false
	}

	updated := oacommon.Reinvoke(mutationRequest.BaseRequest)

	return updated
}

func (wh *webhook) isInputSecretPresent(mutationRequest *dtwebhook.MutationRequest, sourceSecretName, targetSecretName string) bool {
	err := wh.replicateSecret(mutationRequest, sourceSecretName, targetSecretName)

	if k8serrors.IsNotFound(err) {
		log.Info(fmt.Sprintf("unable to copy source of %s as it is not available, injection not possible", sourceSecretName), "pod", mutationRequest.PodName())

		oacommon.SetNotInjectedAnnotations(mutationRequest.Pod, NoBootstrapperConfigReason)

		return false
	}

	if err != nil {
		log.Error(err, fmt.Sprintf("unable to verify, if %s is available, injection not possible", sourceSecretName))

		oacommon.SetNotInjectedAnnotations(mutationRequest.Pod, NoBootstrapperConfigReason)

		return false
	}

	return true
}

func (wh *webhook) replicateSecret(mutationRequest *dtwebhook.MutationRequest, sourceSecretName, targetSecretName string) error {
	var initSecret corev1.Secret

	secretObjectKey := client.ObjectKey{Name: targetSecretName, Namespace: mutationRequest.Namespace.Name}
	err := wh.apiReader.Get(mutationRequest.Context, secretObjectKey, &initSecret)

	if k8serrors.IsNotFound(err) {
		log.Info(targetSecretName+" is not available, trying to replicate", "pod", mutationRequest.PodName())

		return bootstrapperconfig.Replicate(mutationRequest.Context, mutationRequest.DynaKube, secret.Query(wh.kubeClient, wh.apiReader, log), sourceSecretName, targetSecretName, mutationRequest.Namespace.Name)
	}

	return nil
}

func setDynatraceInjectedAnnotation(mutationRequest *dtwebhook.MutationRequest) {
	if mutationRequest.Pod.Annotations == nil {
		mutationRequest.Pod.Annotations = make(map[string]string)
	}

	mutationRequest.Pod.Annotations[dtwebhook.AnnotationDynatraceInjected] = "true"
	delete(mutationRequest.Pod.Annotations, dtwebhook.AnnotationDynatraceReason)
}
