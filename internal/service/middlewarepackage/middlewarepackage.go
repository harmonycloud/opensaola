/*
Copyright 2025 The OpenSaola Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package middlewarepackage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/harmonycloud/opensaola/internal/service/middlewareactionbaseline"
	"github.com/harmonycloud/opensaola/internal/service/middlewarebaseline"
	"github.com/harmonycloud/opensaola/internal/service/middlewareconfiguration"
	"github.com/harmonycloud/opensaola/internal/service/middlewareoperatorbaseline"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	"github.com/harmonycloud/opensaola/internal/service/status"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func packageReleaseDigest(secret *corev1.Secret) string {
	if secret == nil || secret.Data == nil {
		return ""
	}
	b := secret.Data[packages.Release]
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func truncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

func isTerminalInstallError(err error) bool {
	if err == nil {
		return false
	}
	// K8s API validation failures are content problems; should not tight-loop retry. Wait for package content change.
	if apiErrors.IsInvalid(err) || apiErrors.IsBadRequest(err) {
		return true
	}
	// Local parsing/conversion failures are also content problems
	msg := err.Error()
	return strings.Contains(msg, "error converting YAML to JSON") ||
		strings.Contains(msg, "yaml:") ||
		strings.Contains(msg, "invalid map key")
}

func Check(ctx context.Context, cli client.Client, mp *v1.MiddlewarePackage) error {
	if mp == nil {
		return nil
	}
	defer func() {
		log.FromContext(ctx).V(1).Info("finished checking MiddlewarePackage", "name", mp.Name)
	}()
	log.FromContext(ctx).V(1).Info("checking MiddlewarePackage", "name", mp.Name)

	conditionChecked := status.GetCondition(ctx, &mp.Status.Conditions, v1.CondTypeChecked)
	if conditionChecked.Status != metav1.ConditionTrue || conditionChecked.ObservedGeneration < mp.Generation {
		conditionChecked.Success(ctx, mp.Generation)
		if err := k8s.UpdateMiddlewarePackageStatus(ctx, cli, mp); err != nil {
			return err
		}
	}

	return nil
}

func HandleSecret(ctx context.Context, cli client.Client, secret *corev1.Secret, act consts.HandleAction) error {
	// 1. Get MiddlewarePackage
	mp, err := k8s.GetMiddlewarePackage(ctx, cli, secret.Name)
	if err != nil && !apiErrors.IsNotFound(err) {
		return err
	}
	switch act {
	case consts.HandleActionPublish, consts.HandleActionUpdate:
		// If MiddlewarePackage already registered and no install/uninstall
		// action pending, skip tarball decompression entirely. This avoids
		// loading all 600+ packages into memory on every pod restart.
		_, hasInstall := secret.Annotations[v1.LabelInstall]
		_, hasUnInstall := secret.Annotations[v1.LabelUnInstall]
		if mp != nil && !hasInstall && !hasUnInstall {
			return nil
		}

		// Get package
		var pkg *packages.Package
		pkg, err = packages.Get(ctx, cli, secret.Name)
		if err != nil {
			return err
		}

		var (
			crds           []string
			baselines      []string
			actions        []string
			configurations []string
		)

		for k := range pkg.Files {
			if strings.HasPrefix(k, "crds") {
				crds = append(crds, strings.Split(k, "/")[1])
			}
			if strings.HasPrefix(k, "configurations") {
				configurations = append(configurations, strings.Split(k, "/")[1])
			}
			if strings.HasPrefix(k, "actions") {
				actions = append(actions, strings.Split(k, "/")[1])
			}
			if strings.HasPrefix(k, "baselines") {
				baselines = append(baselines, strings.Split(k, "/")[1])
			}
		}

		mp = &v1.MiddlewarePackage{
			TypeMeta: metav1.TypeMeta{
				Kind:       "MiddlewarePackage",
				APIVersion: "middleware.cn/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:   secret.Name,
				Labels: secret.Labels,
			},
			Spec: v1.MiddlewarePackageSpec{
				Name:        pkg.Metadata.Name,
				Version:     pkg.Metadata.Version,
				Owner:       pkg.Metadata.Owner,
				Type:        pkg.Metadata.Type,
				Description: pkg.Metadata.Description,
				Catalog: v1.Catalog{
					Crds:           crds,
					Baselines:      baselines,
					Configurations: configurations,
					Actions:        actions,
				},
			},
		}

		err = k8s.CreateMiddlewarePackage(ctx, cli, mp)
		if err != nil && !apiErrors.IsAlreadyExists(err) {
			return err
		}
		if apiErrors.IsAlreadyExists(err) {
			err = k8s.UpdateMiddlewarePackage(ctx, cli, mp)
			if err != nil {
				return err
			}
		}

		if _, ok := secret.Annotations[v1.LabelInstall]; ok {
			digest := packageReleaseDigest(secret)
			if digest != "" && secret.Annotations[v1.AnnotationInstallDigest] == digest && secret.Annotations[v1.AnnotationInstallError] != "" {
				// Previous install failed with same package content: stop tight-loop retry; wait for Secret.Data change
				return nil
			}

			var enabledSecrets *corev1.SecretList
			enabledSecrets, err = k8s.GetSecrets(ctx, cli, packages.DataNamespace, client.MatchingLabels{
				v1.LabelEnabled:   "true",
				v1.LabelComponent: secret.Labels[v1.LabelComponent],
			})
			if err != nil {
				return err
			}

			for _, item := range enabledSecrets.Items {
				err = HandleResource(ctx, cli, consts.HandleActionDelete, item.Name)
				if err != nil {
					log.FromContext(ctx).Error(err, "failed to delete package resource", "name", item.Name)
					continue
				}
				delete(item.Labels, v1.LabelEnabled)
				err = k8s.UpdateSecret(ctx, cli, &item)
			}

			err = HandleResource(ctx, cli, consts.HandleActionPublish, secret.Name)
			if err != nil {
				if isTerminalInstallError(err) {
					if secret.Annotations == nil {
						secret.Annotations = map[string]string{}
					}
					secret.Annotations[v1.AnnotationInstallDigest] = digest
					secret.Annotations[v1.AnnotationInstallError] = truncateBytes(err.Error(), 1024)
					if updateErr := k8s.UpdateSecret(ctx, cli, secret); updateErr != nil {
						// Unable to persist the install error; return the original error so the queue retries (prevents silent failure)
						log.FromContext(ctx).Error(updateErr, "failed to persist install error to Secret", "name", secret.Name)
						return err
					}
					// Terminate this retry chain: wait for package content change (Secret.Data) before the next watch-triggered attempt
					return nil
				}
				log.FromContext(ctx).Error(err, "failed to publish package resources", "name", secret.Name)
				return err
			}

			secret.Labels[v1.LabelEnabled] = "true"
			delete(secret.Annotations, v1.LabelInstall)
			delete(secret.Annotations, v1.AnnotationInstallDigest)
			delete(secret.Annotations, v1.AnnotationInstallError)
			err = k8s.UpdateSecret(ctx, cli, secret)
			if err != nil {
				log.FromContext(ctx).Error(err, "failed to update package resource", "name", secret.Name)
				return err
			}
		} else if _, ok = secret.Annotations[v1.LabelUnInstall]; ok {
			err = HandleResource(ctx, cli, consts.HandleActionDelete, secret.Name)
			if err != nil {
				return err
			}
			secret.Labels[v1.LabelEnabled] = "false"
			delete(secret.Annotations, v1.LabelUnInstall)
			delete(secret.Annotations, v1.AnnotationInstallDigest)
			delete(secret.Annotations, v1.AnnotationInstallError)
			err = k8s.UpdateSecret(ctx, cli, secret)
			if err != nil {
				log.FromContext(ctx).Error(err, "failed to update package resource", "name", secret.Name)
				return err
			}
		}

	case consts.HandleActionDelete:
		if mp != nil {
			err = k8s.DeleteMiddlewarePackage(ctx, cli, mp)
			if err != nil {
				return err
			}
			// if mp.Labels[v1.LabelEnabled] == "true" {
			// 	err = HandleResource(ctx, cli, consts.HandleActionDelete, mp.Name)
			// 	if err != nil {
			// 		return err
			// 	}
			//
			// }
		}
	}

	return nil
}

// func HandlePackage(ctx context.Context, cli client.Client, mp *v1.MiddlewarePackage) error {
//	var err error
//	defer func() {
//		if err != nil {
//			logger.Log.Errorj(map[string]interface{}{
//				"amsg": "failed to handle MiddlewarePackage",
//				"name": mp.Name,
//				"err":  err.Error(),
//			})
//			return
//		} else {
//			logger.Log.Infoj(map[string]interface{}{
//				"amsg": "successfully handled MiddlewarePackage",
//				"name": mp.Name,
//			})
//		}
//	}()
//	logger.Log.Infoj(map[string]interface{}{
//		"amsg": "handling MiddlewarePackage",
//		"name": mp.Name,
//	})
//	//conditionChecked := status.GetCondition(ctx, &mp.Status.Conditions, v1.CondTypeChecked)
//	//if conditionChecked.Status == metav1.ConditionTrue {
//	//	err = HandleResource(ctx, cli, mp)
//	//	if err != nil {
//	//		return err
//	//	}
//	//}
//	return nil
// }

func HandleResource(ctx context.Context, cli client.Client, act consts.HandleAction, secretName string) error {
	var err error
	defer func() {
		if err != nil && !apiErrors.IsAlreadyExists(err) {
			log.FromContext(ctx).Error(err, "failed to handle MiddlewarePackage resources", "name", secretName)
			return
		} else {
			log.FromContext(ctx).Info("successfully handled MiddlewarePackage resources", "name", secretName)
		}
	}()

	log.FromContext(ctx).Info("handling MiddlewarePackage resources")

	// Get MiddlewarePackage
	var mp *v1.MiddlewarePackage
	mp, err = k8s.GetMiddlewarePackage(ctx, cli, secretName)
	if err != nil {
		return err
	}

	var pkg *packages.Package
	pkg, err = packages.Get(ctx, cli, secretName)
	if err != nil {
		return err
	}

	// Get the middleware baseline list from the package
	var middlewareBaseline []*v1.MiddlewareBaseline
	middlewareBaseline, err = packages.GetMiddlewareBaselines(ctx, cli, pkg.Name)
	if err != nil {
		return err
	}

	// Get the middleware operator baseline list from the package
	var middlewareOperatorBaseline []*v1.MiddlewareOperatorBaseline
	middlewareOperatorBaseline, err = packages.GetMiddlewareOperatorBaselines(ctx, cli, pkg.Name)
	if err != nil {
		return err
	}

	// Get the action baseline list from the package
	var middlewareActionBaselines []*v1.MiddlewareActionBaseline
	middlewareActionBaselines, err = packages.GetMiddlewareActionBaselines(ctx, cli, pkg.Name)
	if err != nil {
		return err
	}

	// Get the configurations list from the package
	var configurations map[string]*v1.MiddlewareConfiguration
	configurations, err = packages.GetConfigurations(ctx, cli, pkg.Name)
	if err != nil {
		return err
	}

	switch act {
	case consts.HandleActionPublish:

		// Publish middleware baselines
		var deployedMiddlewareBaseline []*v1.MiddlewareBaseline
		for _, baseline := range middlewareBaseline {
			log.FromContext(ctx).Info("start publishing MiddlewareBaseline", "name", baseline.Name)
			err = middlewarebaseline.Deploy(ctx, cli, pkg.Metadata.Name, pkg.Metadata.Version, pkg.Name, false, baseline, mp)
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				log.FromContext(ctx).Error(err, "failed to publish MiddlewareBaseline", "name", baseline.Name)
				// Rollback
				for _, baselineDelete := range deployedMiddlewareBaseline {
					log.FromContext(ctx).Info("start rolling back MiddlewareBaseline", "name", baselineDelete.Name)
					if rbErr := k8s.DeleteMiddlewareBaseline(ctx, cli, baselineDelete); rbErr != nil {
						log.FromContext(ctx).Info("rollback: failed to delete MiddlewareBaseline", "warning", true, "name", baselineDelete.Name, "err", rbErr)
					}
					log.FromContext(ctx).Info("finished rolling back MiddlewareBaseline", "name", baselineDelete.Name)
				}
				return err
			}
			log.FromContext(ctx).Info("finished publishing MiddlewareBaseline", "name", baseline.Name)
		}

		defer func() {
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				for _, baseline := range middlewareBaseline {
					log.FromContext(ctx).Info("start rolling back MiddlewareBaseline", "name", baseline.Name)
					if rbErr := k8s.DeleteMiddlewareBaseline(ctx, cli, baseline); rbErr != nil {
						log.FromContext(ctx).Info("rollback: failed to delete MiddlewareBaseline", "warning", true, "name", baseline.Name, "err", rbErr)
					}
					log.FromContext(ctx).Info("finished rolling back MiddlewareBaseline", "name", baseline.Name)
				}
			}
		}()

		// Publish middleware operator baselines
		var deployedMiddlewareOperatorBaseline []*v1.MiddlewareOperatorBaseline
		for _, operatorBaseline := range middlewareOperatorBaseline {
			log.FromContext(ctx).Info("start publishing MiddlewareOperatorBaseline", "name", operatorBaseline.Name)
			err = middlewareoperatorbaseline.Deploy(ctx, cli, pkg.Metadata.Name, pkg.Metadata.Version, pkg.Name, false, operatorBaseline, mp)
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				log.FromContext(ctx).Error(err, "failed to publish MiddlewareOperatorBaseline", "name", operatorBaseline.Name)
				// Rollback
				for _, operatorBaselineDelete := range deployedMiddlewareOperatorBaseline {
					log.FromContext(ctx).Info("start rolling back MiddlewareOperatorBaseline", "name", operatorBaselineDelete.Name)
					if rbErr := k8s.DeleteMiddlewareOperatorBaseline(ctx, cli, operatorBaselineDelete); rbErr != nil {
						log.FromContext(ctx).Info("rollback: failed to delete MiddlewareOperatorBaseline", "warning", true, "name", operatorBaselineDelete.Name, "err", rbErr)
					}
					log.FromContext(ctx).Info("finished rolling back MiddlewareOperatorBaseline", "name", operatorBaselineDelete.Name)
				}
				return err
			}
			log.FromContext(ctx).Info("finished publishing MiddlewareOperatorBaseline", "name", operatorBaseline.Name)
		}

		defer func() {
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				for _, operatorBaseline := range middlewareOperatorBaseline {
					log.FromContext(ctx).Info("start rolling back MiddlewareOperatorBaseline", "name", operatorBaseline.Name)
					if rbErr := k8s.DeleteMiddlewareOperatorBaseline(ctx, cli, operatorBaseline); rbErr != nil {
						log.FromContext(ctx).Info("rollback: failed to delete MiddlewareOperatorBaseline", "warning", true, "name", operatorBaseline.Name, "err", rbErr)
					}
					log.FromContext(ctx).Info("finished rolling back MiddlewareOperatorBaseline", "name", operatorBaseline.Name)
				}
			}
		}()

		// Publish action baselines
		var deployedMiddlewareActionBaseline []*v1.MiddlewareActionBaseline
		for _, actionBaseline := range middlewareActionBaselines {
			log.FromContext(ctx).Info("start publishing MiddlewareActionBaseline", "name", actionBaseline.Name)
			err = middlewareactionbaseline.Deploy(ctx, cli, pkg.Metadata.Name, pkg.Metadata.Version, pkg.Name, false, actionBaseline, mp)
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				log.FromContext(ctx).Error(err, "failed to publish MiddlewareActionBaseline", "name", actionBaseline.Name)
				// Rollback
				for _, actionBaselineDelete := range deployedMiddlewareActionBaseline {
					log.FromContext(ctx).Info("start rolling back MiddlewareActionBaseline", "name", actionBaselineDelete.Name)
					if rbErr := k8s.DeleteMiddlewareActionBaseline(ctx, cli, actionBaselineDelete); rbErr != nil {
						log.FromContext(ctx).Info("rollback: failed to delete MiddlewareActionBaseline", "warning", true, "name", actionBaselineDelete.Name, "err", rbErr)
					}
					log.FromContext(ctx).Info("finished rolling back MiddlewareActionBaseline", "name", actionBaselineDelete.Name)
				}
				return err
			}
			log.FromContext(ctx).Info("finished publishing MiddlewareActionBaseline", "name", actionBaseline.Name)
		}

		defer func() {
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				for _, actionBaseline := range middlewareActionBaselines {
					log.FromContext(ctx).Info("start rolling back MiddlewareActionBaseline", "name", actionBaseline.Name)
					if rbErr := k8s.DeleteMiddlewareActionBaseline(ctx, cli, actionBaseline); rbErr != nil {
						log.FromContext(ctx).Info("rollback: failed to delete MiddlewareActionBaseline", "warning", true, "name", actionBaseline.Name, "err", rbErr)
					}
					log.FromContext(ctx).Info("finished rolling back MiddlewareActionBaseline", "name", actionBaseline.Name)
				}
			}
		}()

		// Publish configurations
		var deployedConfigurations []*v1.MiddlewareConfiguration
		for _, configuration := range configurations {
			err = middlewareconfiguration.Deploy(ctx, cli, pkg.Metadata.Name, pkg.Metadata.Version, pkg.Name, false, configuration, mp)
			if err != nil && !apiErrors.IsAlreadyExists(err) {
				log.FromContext(ctx).Error(err, "failed to publish MiddlewareConfiguration", "name", configuration.Name)
				// Rollback
				for _, configurationDelete := range deployedConfigurations {
					log.FromContext(ctx).Info("start rolling back MiddlewareConfiguration", "name", configurationDelete.Name)
					if rbErr := k8s.DeleteMiddlewareConfiguration(ctx, cli, configurationDelete); rbErr != nil {
						log.FromContext(ctx).Info("rollback: failed to delete MiddlewareConfiguration", "warning", true, "name", configurationDelete.Name, "err", rbErr)
					}
					log.FromContext(ctx).Info("finished rolling back MiddlewareConfiguration", "name", configurationDelete.Name)
				}
				return err
			}
			log.FromContext(ctx).Info("finished publishing MiddlewareConfiguration", "name", configuration.Name)
			deployedConfigurations = append(deployedConfigurations, configuration)
		}
	case consts.HandleActionDelete:
		// Delete middleware baselines
		for _, baseline := range middlewareBaseline {
			log.FromContext(ctx).Info("start deleting MiddlewareBaseline", "name", baseline.Name)
			err = k8s.DeleteMiddlewareBaseline(ctx, cli, baseline)
			if err != nil {
				log.FromContext(ctx).Error(err, "failed to delete MiddlewareBaseline", "name", baseline.Name)
				continue
			}
			log.FromContext(ctx).Info("finished deleting MiddlewareBaseline", "name", baseline.Name)
		}

		// Delete middleware operator baselines
		for _, operatorBaseline := range middlewareOperatorBaseline {
			log.FromContext(ctx).Info("start deleting MiddlewareOperatorBaseline", "name", operatorBaseline.Name)
			err = k8s.DeleteMiddlewareOperatorBaseline(ctx, cli, operatorBaseline)
			if err != nil {
				log.FromContext(ctx).Error(err, "failed to delete MiddlewareOperatorBaseline", "name", operatorBaseline.Name)
				continue
			}
			log.FromContext(ctx).Info("finished deleting MiddlewareOperatorBaseline", "name", operatorBaseline.Name)
		}

		// Delete action baselines
		for _, actionBaseline := range middlewareActionBaselines {
			log.FromContext(ctx).Info("start deleting MiddlewareActionBaseline", "name", actionBaseline.Name)
			err = k8s.DeleteMiddlewareActionBaseline(ctx, cli, actionBaseline)
			if err != nil {
				log.FromContext(ctx).Error(err, "failed to delete MiddlewareActionBaseline", "name", actionBaseline.Name)
				continue
			}
			log.FromContext(ctx).Info("finished deleting MiddlewareActionBaseline", "name", actionBaseline.Name)
		}

		// Delete configurations
		for _, configuration := range configurations {
			log.FromContext(ctx).Info("start deleting MiddlewareConfiguration", "name", configuration.Name)
			err = k8s.DeleteMiddlewareConfiguration(ctx, cli, configuration)
			if err != nil {
				log.FromContext(ctx).Error(err, "failed to delete MiddlewareConfiguration", "name", configuration.Name)
				continue
			}
			log.FromContext(ctx).Info("finished deleting MiddlewareConfiguration", "name", configuration.Name)
		}

	}
	return nil
}
