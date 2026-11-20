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
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	"github.com/harmonycloud/opensaola/internal/service/status"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

func normalizedPackageLabels(source map[string]string) map[string]string {
	labels := make(map[string]string, len(source)+1)
	for key, value := range source {
		labels[key] = value
	}
	labels[v1.LabelProject] = consts.ProjectOpenSaola
	return labels
}

type packageUsage struct {
	Kind      string
	Namespace string
	Name      string
	Baseline  string
	State     string
}

func findPackageUsages(ctx context.Context, cli client.Client, packageName string) ([]packageUsage, error) {
	selector := client.MatchingLabels{v1.LabelPackageName: packageName}
	var usages []packageUsage

	reader := k8s.NewManagedResourceWriter(cli).Reader
	var middlewares v1.MiddlewareList
	if err := reader.List(ctx, &middlewares, selector); err != nil {
		return nil, fmt.Errorf("list Middleware by package %q: %w", packageName, err)
	}
	for _, item := range middlewares.Items {
		usages = append(usages, packageUsage{
			Kind:      "middleware",
			Namespace: item.Namespace,
			Name:      item.Name,
			Baseline:  item.Spec.Baseline,
			State:     string(item.Status.State),
		})
	}

	var operators v1.MiddlewareOperatorList
	if err := reader.List(ctx, &operators, selector); err != nil {
		return nil, fmt.Errorf("list MiddlewareOperator by package %q: %w", packageName, err)
	}
	for _, item := range operators.Items {
		usages = append(usages, packageUsage{
			Kind:      "middlewareoperator",
			Namespace: item.Namespace,
			Name:      item.Name,
			Baseline:  item.Spec.Baseline,
			State:     string(item.Status.State),
		})
	}

	sort.Slice(usages, func(i, j int) bool {
		left := usages[i].Kind + "/" + usages[i].Namespace + "/" + usages[i].Name
		right := usages[j].Kind + "/" + usages[j].Namespace + "/" + usages[j].Name
		return left < right
	})
	return usages, nil
}

func formatPackageUsageMessage(packageName string, usages []packageUsage) string {
	if len(usages) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "package %q is still used; uninstall deployed Middleware or MiddlewareOperator resources first:", packageName)
	for _, usage := range usages {
		fmt.Fprintf(&b, " %s %s/%s", usage.Kind, usage.Namespace, usage.Name)
		if usage.Baseline != "" {
			fmt.Fprintf(&b, "(baseline=%s", usage.Baseline)
			if usage.State != "" {
				fmt.Fprintf(&b, ",state=%s", usage.State)
			}
			b.WriteString(")")
		} else if usage.State != "" {
			fmt.Fprintf(&b, "(state=%s)", usage.State)
		}
	}
	return b.String()
}

func finalizePackageSecret(ctx context.Context, cli client.Client, secret *corev1.Secret, mp *v1.MiddlewarePackage) error {
	if !controllerutil.ContainsFinalizer(secret, v1.FinalizerPackageSecret) {
		return nil
	}

	usages, usageErr := findPackageUsages(ctx, cli, secret.Name)
	if usageErr != nil {
		return usageErr
	}
	if len(usages) > 0 {
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		message := truncateBytes(formatPackageUsageMessage(secret.Name, usages), 1024)
		secret.Annotations[v1.AnnotationUninstallError] = message
		if err := k8s.UpdateSecret(ctx, cli, secret); err != nil {
			log.FromContext(ctx).Error(err, "failed to update package uninstall error", "name", secret.Name)
			return err
		}
		return fmt.Errorf("%s", message)
	}

	// A retry may observe the package already gone after a successful cleanup.
	if mp != nil {
		if err := handlePackageResources(ctx, cli, consts.HandleActionDelete, mp); err != nil {
			return err
		}
		if err := k8s.DeleteMiddlewarePackage(ctx, cli, mp); err != nil && !apiErrors.IsNotFound(err) {
			return err
		}
	}

	patch := client.MergeFromWithOptions(secret.DeepCopy(), client.MergeFromWithOptimisticLock{})
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels[v1.LabelEnabled] = "false"
	secret.Labels[v1.LabelProject] = consts.ProjectOpenSaola
	if secret.Annotations != nil {
		delete(secret.Annotations, v1.LabelUnInstall)
		delete(secret.Annotations, v1.AnnotationInstallDigest)
		delete(secret.Annotations, v1.AnnotationInstallError)
		delete(secret.Annotations, v1.AnnotationUninstallError)
	}
	controllerutil.RemoveFinalizer(secret, v1.FinalizerPackageSecret)
	return cli.Patch(ctx, secret, patch)
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
	if act != consts.HandleActionDelete && (secret.DeletionTimestamp != nil || hasPackageRequest(secret)) {
		component := packageComponent(secret)
		unlock := lockPackageComponent(component)
		defer unlock()
		if secret.DeletionTimestamp == nil {
			fresh := new(corev1.Secret)
			if err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKeyFromObject(secret), fresh); err != nil {
				return client.IgnoreNotFound(err)
			}
			if fresh.UID != secret.UID || packageComponent(fresh) != component {
				return fmt.Errorf("package %s changed during reconciliation", secret.Name)
			}
			secret = fresh
		}
		if secret.DeletionTimestamp != nil || (secret.Annotations[v1.LabelUnInstall] != "" && secret.Annotations[v1.LabelInstall] == "") {
			if err := settleBeforeUninstall(ctx, cli, secret); err != nil {
				return err
			}
		}
	}
	// 1. Get MiddlewarePackage
	reader := client.Reader(cli)
	if secret.DeletionTimestamp != nil || act == consts.HandleActionDelete {
		reader = k8s.NewManagedResourceWriter(cli).Reader
	}
	mp := new(v1.MiddlewarePackage)
	err := reader.Get(ctx, client.ObjectKey{Name: secret.Name}, mp)
	if apiErrors.IsNotFound(err) {
		mp = nil
	} else if err != nil {
		return err
	}
	switch act {
	case consts.HandleActionPublish, consts.HandleActionUpdate:
		if secret.GetDeletionTimestamp() != nil {
			return finalizePackageSecret(ctx, cli, secret, mp)
		}

		// If MiddlewarePackage already registered and no install/uninstall
		// action pending, skip tarball decompression entirely. This avoids
		// loading all 600+ packages into memory on every pod restart.
		_, hasInstall := secret.Annotations[v1.LabelInstall]
		_, hasUnInstall := secret.Annotations[v1.LabelUnInstall]
		if mp != nil && !hasInstall && !hasUnInstall && secret.Annotations[pendingSwitch] == "" {
			return nil
		}

		// Recovery uses the recorded preimages, including when the archive no
		// longer parses. Registration was completed before pendingSwitch was set.
		if secret.Annotations[pendingSwitch] != "" && (hasInstall || !hasUnInstall) {
			return activatePackage(ctx, cli, secret)
		}
		if hasInstall && secret.Annotations[v1.AnnotationInstallError] != "" && secret.Annotations[v1.AnnotationInstallDigest] == packageReleaseDigest(secret) {
			return nil
		}

		// Get package
		var pkg *packages.Package
		pkg, err = packages.Get(ctx, cli, secret.Name)
		if err == nil && (pkg.Metadata.Name == "" || pkg.Metadata.Version == "") {
			err = fmt.Errorf("%w: metadata.yaml requires name and version", packages.ErrInvalidArchive)
		}
		if err != nil {
			if hasInstall && errors.Is(err, packages.ErrInvalidArchive) {
				return recordInstallError(ctx, cli, secret, packageReleaseDigest(secret), err.Error())
			}
			return err
		}

		var (
			crds           []string
			baselines      []string
			actions        []string
			configurations []string
		)

		for path := range pkg.Files {
			dir, file, ok := strings.Cut(path, "/")
			if !ok || file == "" {
				continue
			}
			switch dir {
			case "crds":
				crds = append(crds, file)
			case "baselines":
				baselines = append(baselines, file)
			case "actions":
				actions = append(actions, file)
			case "configurations":
				configurations = append(configurations, file)
			}
		}
		sort.Strings(crds)
		sort.Strings(baselines)
		sort.Strings(actions)
		sort.Strings(configurations)

		mp = &v1.MiddlewarePackage{
			TypeMeta: metav1.TypeMeta{
				Kind:       "MiddlewarePackage",
				APIVersion: "middleware.cn/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:   secret.Name,
				Labels: normalizedPackageLabels(secret.Labels),
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

		if hasInstall || secret.Annotations[pendingSwitch] != "" {
			return activatePackage(ctx, cli, secret)
		}
		if hasUnInstall {
			usages, usageErr := findPackageUsages(ctx, cli, secret.Name)
			if usageErr != nil {
				return usageErr
			}
			if len(usages) > 0 {
				if secret.Annotations == nil {
					secret.Annotations = map[string]string{}
				}
				secret.Annotations[v1.AnnotationUninstallError] = truncateBytes(formatPackageUsageMessage(secret.Name, usages), 1024)
				delete(secret.Annotations, v1.LabelUnInstall)
				if secret.Labels == nil {
					secret.Labels = map[string]string{}
				}
				secret.Labels[v1.LabelProject] = consts.ProjectOpenSaola
				if err = k8s.UpdateSecret(ctx, cli, secret); err != nil {
					log.FromContext(ctx).Error(err, "failed to update package uninstall error", "name", secret.Name)
					return err
				}
				return nil
			}

			err = HandleResource(ctx, cli, consts.HandleActionDelete, secret.Name)
			if err != nil {
				return err
			}
			if secret.Labels == nil {
				secret.Labels = map[string]string{}
			}
			secret.Labels[v1.LabelEnabled] = "false"
			secret.Labels[v1.LabelProject] = consts.ProjectOpenSaola
			delete(secret.Annotations, v1.LabelUnInstall)
			delete(secret.Annotations, v1.AnnotationInstallDigest)
			delete(secret.Annotations, v1.AnnotationInstallError)
			delete(secret.Annotations, v1.AnnotationUninstallError)
			err = k8s.UpdateSecret(ctx, cli, secret)
			if err != nil {
				log.FromContext(ctx).Error(err, "failed to update package resource", "name", secret.Name)
				return err
			}
		}

	case consts.HandleActionDelete:
		if mp == nil {
			return nil
		}
		// An informer miss can also be a late event for a replaced Secret.
		// Confirm the package archive is gone before deleting its catalog entry.
		current := &metav1.PartialObjectMetadata{}
		current.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
		err = reader.Get(ctx, client.ObjectKey{Namespace: packages.DataNamespace, Name: secret.Name}, current)
		if !apiErrors.IsNotFound(err) {
			return err
		}
		usages, err := findPackageUsages(ctx, cli, mp.Name)
		if err != nil {
			return err
		}
		if len(usages) != 0 {
			return fmt.Errorf("%s", formatPackageUsageMessage(mp.Name, usages))
		}
		return client.IgnoreNotFound(k8s.DeleteMiddlewarePackage(ctx, cli, mp))
	}

	return nil
}

func HandleResource(ctx context.Context, cli client.Client, act consts.HandleAction, secretName string) error {
	if act == consts.HandleActionPublish {
		secret := new(corev1.Secret)
		if err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKey{Namespace: packages.DataNamespace, Name: secretName}, secret); err != nil {
			return err
		}
		before := secret.DeepCopy()
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[v1.LabelInstall] = "true"
		if err := cli.Patch(ctx, secret, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		return HandleSecret(ctx, cli, secret, act)
	}
	mp := new(v1.MiddlewarePackage)
	if err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKey{Name: secretName}, mp); err != nil {
		return err
	}
	return handlePackageResources(ctx, cli, act, mp)
}

// The archive supplies the name only. Ownership and delete preconditions must
// come from the current API object, which may have been replaced by a newer package.
func deletePackageResource(ctx context.Context, cli client.Client, mp *v1.MiddlewarePackage, name string, live client.Object) error {
	if err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKey{Name: name}, live); err != nil {
		return client.IgnoreNotFound(err)
	}
	return retireDefinition(ctx, cli, mp, live, false)
}

func retireDefinition(ctx context.Context, cli client.Client, mp *v1.MiddlewarePackage, live client.Object, dry bool) error {
	name := live.GetName()
	owned, err := packageOwnsDefinition(mp, live)
	if err != nil || !owned {
		return err
	}
	uid, rv := live.GetUID(), live.GetResourceVersion()
	if uid == "" || rv == "" {
		return fmt.Errorf("cannot verify identity of %T %q for deletion", live, name)
	}
	opts := []client.DeleteOption{client.Preconditions{UID: &uid, ResourceVersion: &rv}}
	if dry {
		opts = append(opts, client.DryRunAll)
	}
	if err := cli.Delete(ctx, live, opts...); err != nil && !apiErrors.IsNotFound(err) {
		return fmt.Errorf("delete %T %q owned by package %s: %w", live, name, mp.Name, err)
	}
	if !dry && len(live.GetFinalizers()) > 0 {
		// DELETE can be accepted while a finalizer still holds the old
		// definition. Keep the cleanup record until this UID is gone.
		gvk, err := cli.GroupVersionKindFor(live)
		if err != nil {
			return err
		}
		current := new(metav1.PartialObjectMetadata)
		current.SetGroupVersionKind(gvk)
		if err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKeyFromObject(live), current); err != nil {
			return client.IgnoreNotFound(err)
		}
		if current.UID == uid {
			return fmt.Errorf("waiting for finalizers on %s %s owned by package %s", gvk.Kind, name, mp.Name)
		}
	}
	return nil
}

func packageOwnsDefinition(mp *v1.MiddlewarePackage, live client.Object) (bool, error) {
	name := live.GetName()
	owner := metav1.GetControllerOfNoCopy(live)
	labels := live.GetLabels()
	pkgName, version := labels[v1.LabelPackageName], labels[v1.LabelPackageVersion]
	gvk := k8s.MiddlewarePackageGroupVersionKind()
	if owner == nil || owner.Kind != gvk.Kind || owner.APIVersion != gvk.GroupVersion().String() || owner.UID == "" || owner.Name == "" || owner.Name != pkgName || version == "" {
		return false, fmt.Errorf("cannot verify package ownership of %T %q: controller=%v, package=%q, version=%q", live, name, owner, pkgName, version)
	}
	if owner.Name != mp.Name && owner.UID != mp.UID {
		return false, nil
	}
	if owner.Name != mp.Name || owner.UID != mp.UID || version != mp.Spec.Version {
		return false, fmt.Errorf("package ownership conflict for %T %q: observed %s (UID %s, version %s), uninstalling %s (UID %s, version %s)", live, name, pkgName, owner.UID, version, mp.Name, mp.UID, mp.Spec.Version)
	}
	return true, nil
}
