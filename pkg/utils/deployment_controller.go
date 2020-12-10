package utils

import (
	"fmt"
	"strconv"
	"strings"

	operatorapi "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-storage-operator/pkg/generated"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	"github.com/openshift/library-go/pkg/operator/status"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/kubernetes"
)

type DeploymentOptions struct {
	Required       *appsv1.Deployment
	ControllerName string
	OpStatus       *operatorapi.OperatorStatus
	EventRecorder  events.Recorder
	KubeClient     kubernetes.Interface
	OperatorClient v1helpers.OperatorClient
	TargetVersion  string
	VersionGetter  status.VersionGetter
	VersionName    string
}

func CreateDeployment(depOpts DeploymentOptions) (*appsv1.Deployment, error) {
	lastGeneration := resourcemerge.ExpectedDeploymentGeneration(depOpts.Required, depOpts.OpStatus.Generations)
	deployment, _, err := resourceapply.ApplyDeployment(depOpts.KubeClient.AppsV1(), depOpts.EventRecorder, depOpts.Required, lastGeneration)
	if err != nil {
		// This will set Degraded condition
		return nil, err
	}

	// Available: at least one replica is running
	deploymentAvailable := operatorapi.OperatorCondition{
		Type: depOpts.ControllerName + operatorapi.OperatorStatusTypeAvailable,
	}
	if deployment.Status.AvailableReplicas > 0 {
		deploymentAvailable.Status = operatorapi.ConditionTrue
	} else {
		deploymentAvailable.Status = operatorapi.ConditionFalse
		deploymentAvailable.Reason = "WaitDeployment"
		deploymentAvailable.Message = "Waiting for a Deployment pod to start"
	}

	// Not progressing: all replicas are at the latest version && Deployment generation matches
	deploymentProgressing := operatorapi.OperatorCondition{
		Type: depOpts.ControllerName + operatorapi.OperatorStatusTypeProgressing,
	}
	if deployment.Status.ObservedGeneration != deployment.Generation {
		deploymentProgressing.Status = operatorapi.ConditionTrue
		deploymentProgressing.Reason = "NewGeneration"
		msg := fmt.Sprintf("desired generation %d, current generation %d", deployment.Generation, deployment.Status.ObservedGeneration)
		deploymentProgressing.Message = msg
	} else {
		if deployment.Spec.Replicas != nil {
			if deployment.Status.UpdatedReplicas == *deployment.Spec.Replicas {
				deploymentProgressing.Status = operatorapi.ConditionFalse
				// All replicas were updated, set the version
				depOpts.VersionGetter.SetVersion(depOpts.VersionName, depOpts.TargetVersion)
			} else {
				msg := fmt.Sprintf("%d out of %d pods running", deployment.Status.UpdatedReplicas, *deployment.Spec.Replicas)
				deploymentProgressing.Status = operatorapi.ConditionTrue
				deploymentProgressing.Reason = "WaitDeployment"
				deploymentProgressing.Message = msg
			}
		}
	}

	depOpts.OpStatus.ReadyReplicas = deployment.Status.ReadyReplicas

	updateGenerationFn := func(newStatus *operatorapi.OperatorStatus) error {
		if deployment != nil {
			resourcemerge.SetDeploymentGeneration(&newStatus.Generations, deployment)
		}
		return nil
	}

	if _, _, err := v1helpers.UpdateStatus(depOpts.OperatorClient,
		v1helpers.UpdateConditionFn(deploymentAvailable),
		v1helpers.UpdateConditionFn(deploymentProgressing),
		updateGenerationFn,
	); err != nil {
		return nil, err
	}
	return deployment, nil
}

// GetRequiredDeployment returns a deployment from given assset after replacing necessary strings and setting
// correct log level.
func GetRequiredDeployment(deploymentAsset string, spec *operatorapi.OperatorSpec, replacers ...*strings.Replacer) *appsv1.Deployment {
	deploymentString := string(generated.MustAsset(deploymentAsset))

	for _, replacer := range replacers {
		// Replace images
		if replacer != nil {
			deploymentString = replacer.Replace(deploymentString)
		}
	}

	// Replace log level
	logLevel := getLogLevel(spec.LogLevel)
	deploymentString = strings.ReplaceAll(deploymentString, "${LOG_LEVEL}", strconv.Itoa(logLevel))

	deployment := resourceread.ReadDeploymentV1OrDie([]byte(deploymentString))
	return deployment
}

func getLogLevel(logLevel operatorapi.LogLevel) int {
	switch logLevel {
	case operatorapi.Normal, "":
		return 2
	case operatorapi.Debug:
		return 4
	case operatorapi.Trace:
		return 6
	case operatorapi.TraceAll:
		return 100
	default:
		return 2
	}
}