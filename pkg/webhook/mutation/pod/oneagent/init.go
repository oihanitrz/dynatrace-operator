package oneagent

import (
	"net/url"

	"github.com/Dynatrace/dynatrace-bootstrapper/cmd"
	"github.com/Dynatrace/dynatrace-bootstrapper/cmd/configure"
	"github.com/Dynatrace/dynatrace-bootstrapper/cmd/move"
	"github.com/Dynatrace/dynatrace-operator/cmd/bootstrapper"
	"github.com/Dynatrace/dynatrace-operator/pkg/api/latest/dynakube"
	"github.com/Dynatrace/dynatrace-operator/pkg/consts"
	maputils "github.com/Dynatrace/dynatrace-operator/pkg/util/map"
	dtwebhook "github.com/Dynatrace/dynatrace-operator/pkg/webhook/mutation/pod/common"
	"github.com/Dynatrace/dynatrace-operator/pkg/webhook/mutation/pod/common/arg"
	corev1 "k8s.io/api/core/v1"
)

func mutateInitContainer(mutationRequest *dtwebhook.MutationRequest, installPath string) error {
	isCSI := IsCSIVolume(mutationRequest.BaseRequest)
	isSelfExtractingImage := IsSelfExtractingImage(mutationRequest.BaseRequest, isCSI)

	if isCSI {
		addCSIBinVolume(
			mutationRequest.Pod,
			mutationRequest.DynaKube.Name,
			mutationRequest.DynaKube.FF().GetCSIMaxRetryTimeout().String())
	} else {
		addEmptyDirBinVolume(mutationRequest.Pod)
	}

	if isSelfExtractingImage {
		mutationRequest.InstallContainer.Command = []string{}
	} else if !isCSI {
		downloadArgs := []arg.Arg{
			{Name: bootstrapper.TargetVersionFlag, Value: mutationRequest.DynaKube.OneAgent().GetCodeModulesVersion()},
			{Name: bootstrapper.TechnologiesFlag, Value: url.QueryEscape(maputils.GetField(mutationRequest.Pod.Annotations, AnnotationTechnologies, "all"))},
			{Name: bootstrapper.FlavorFlag, Value: maputils.GetField(mutationRequest.Pod.Annotations, AnnotationFlavor, "")},
		}

		mutationRequest.InstallContainer.Args = append(mutationRequest.InstallContainer.Args, arg.ConvertArgsToStrings(downloadArgs)...)
	}

	addInitVolumeMounts(mutationRequest.InstallContainer)

	return addInitArgs(*mutationRequest.Pod, mutationRequest.InstallContainer, mutationRequest.DynaKube, installPath)
}

func addInitArgs(pod corev1.Pod, initContainer *corev1.Container, dk dynakube.DynaKube, installPath string) error {
	args := []arg.Arg{
		{Name: cmd.SourceFolderFlag, Value: consts.AgentCodeModuleSource},
		{Name: cmd.TargetFolderFlag, Value: binInitMountPath},
		{Name: configure.InstallPathFlag, Value: installPath},
	}

	if dk.OneAgent().IsCloudNativeFullstackMode() {
		tenantUUID, err := dk.TenantUUID()
		if err != nil {
			return err
		}

		args = append(args, arg.Arg{Name: configure.IsFullstackFlag}, arg.Arg{Name: configure.TenantFlag, Value: tenantUUID})
	}

	if technology := getTechnology(pod, dk); technology != "" {
		args = append(args, arg.Arg{Name: move.TechnologyFlag, Value: technology})
	}

	if initContainer.Args == nil {
		initContainer.Args = []string{}
	}

	initContainer.Args = append(initContainer.Args, arg.ConvertArgsToStrings(args)...)

	return nil
}

func getTechnology(pod corev1.Pod, dk dynakube.DynaKube) string {
	if technology, ok := pod.Annotations[AnnotationTechnologies]; ok {
		return technology
	}

	technology := dk.FF().GetNodeImagePullTechnology()
	if technology != "" {
		return technology
	}

	return ""
}

func HasPodUserSet(ctx *corev1.PodSecurityContext) bool {
	return ctx != nil && ctx.RunAsUser != nil
}

func HasPodGroupSet(ctx *corev1.PodSecurityContext) bool {
	return ctx != nil && ctx.RunAsGroup != nil
}

func IsNonRoot(ctx *corev1.SecurityContext) bool {
	return ctx != nil &&
		(ctx.RunAsUser != nil && *ctx.RunAsUser != RootUserGroup) &&
		(ctx.RunAsGroup != nil && *ctx.RunAsGroup != RootUserGroup)
}
