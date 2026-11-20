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
	"errors"
	"fmt"
	"reflect"
	"sync"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const pendingSwitch = "middleware.cn/package-switch-pending"

var errInvalidDefinition = errors.New("invalid package definition")

// Reconciles for different package versions have different workqueue keys.
// Serialize local handlers; the API journal also survives leader changes.
var componentLocks [64]sync.Mutex

func lockPackageComponent(component string) func() {
	hash := sha256.Sum256([]byte(component))
	lock := &componentLocks[int(hash[0])%len(componentLocks)]
	lock.Lock()
	return lock.Unlock
}

func packageComponent(s *corev1.Secret) string {
	if component := s.Labels[v1.LabelComponent]; component != "" {
		return component
	}
	return s.Name
}
func hasPackageRequest(s *corev1.Secret) bool {
	_, install := s.Annotations[v1.LabelInstall]
	_, uninstall := s.Annotations[v1.LabelUnInstall]
	return install || uninstall || s.Annotations[pendingSwitch] != ""
}

// Patch only changed metadata against a fresh resourceVersion. This is used
// for both ends of the switch so a replaced archive cannot be enabled/retired.
func patchSwitchSecret(ctx context.Context, cli client.Client, name string, uid types.UID, mutate func(*corev1.Secret)) error {
	s := new(corev1.Secret)
	if err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKey{Namespace: packages.DataNamespace, Name: name}, s); err != nil {
		return err
	}
	if s.UID != uid {
		return fmt.Errorf("package Secret %s was replaced during activation", name)
	}
	before := s.DeepCopy()
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	mutate(s)
	if reflect.DeepEqual(before.Labels, s.Labels) && reflect.DeepEqual(before.Annotations, s.Annotations) && reflect.DeepEqual(before.Finalizers, s.Finalizers) {
		return nil
	}
	return cli.Patch(ctx, s, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

func activeSources(ctx context.Context, cli client.Client, target *corev1.Secret) ([]switchSource, error) {
	reader := k8s.NewManagedResourceWriter(cli).Reader
	var enabled corev1.SecretList
	err := reader.List(ctx, &enabled, client.InNamespace(packages.DataNamespace),
		client.MatchingLabelsSelector{Selector: consts.OpenSaolaProjectSelector(v1.LabelProject)},
		client.MatchingLabels{v1.LabelComponent: target.Labels[v1.LabelComponent], v1.LabelEnabled: "true"})
	if err != nil {
		return nil, err
	}
	var sources []switchSource
	for _, s := range enabled.Items {
		if s.Name == target.Name {
			continue
		}
		if s.DeletionTimestamp != nil {
			return nil, fmt.Errorf("active package %s is being uninstalled", s.Name)
		}
		mp := new(v1.MiddlewarePackage)
		if err := reader.Get(ctx, client.ObjectKey{Name: s.Name}, mp); err != nil {
			return nil, err
		}
		sources = append(sources, switchSource{Name: mp.Name, UID: mp.UID, SecretUID: s.UID, Version: mp.Spec.Version})
	}
	return sources, nil
}

func activatePackage(ctx context.Context, cli client.Client, target *corev1.Secret) error {
	component := packageComponent(target)
	record, j, err := readSwitchJournal(ctx, cli, component)
	if err != nil {
		return err
	}
	if record != nil {
		if j.Target != target.Name || j.TargetUID != target.UID {
			return fmt.Errorf("component %s is switching to package %s", component, j.Target)
		}
		return advanceSwitch(ctx, cli, target, record, j)
	}
	digest := packageReleaseDigest(target)
	_, install := target.Annotations[v1.LabelInstall]
	if !install || (target.Annotations[v1.AnnotationInstallDigest] == digest && target.Annotations[v1.AnnotationInstallError] != "") {
		if target.Annotations[pendingSwitch] == "" {
			return nil
		}
		return clearPendingSwitch(ctx, cli, target)
	}
	err = patchSwitchSecret(ctx, cli, target.Name, target.UID, func(s *corev1.Secret) {
		s.Annotations[pendingSwitch] = component
		controllerutil.AddFinalizer(s, v1.FinalizerPackageSecret)
	})
	if err != nil {
		return err
	}
	j = &switchJournal{Phase: "planning", Target: target.Name, TargetUID: target.UID, Digest: digest}
	record, err = createSwitchJournal(ctx, cli, component, target, j)
	if err != nil {
		return err
	}
	return advanceSwitch(ctx, cli, target, record, j)
}

func recordInstallError(ctx context.Context, cli client.Client, target *corev1.Secret, digest, message string) error {
	return patchSwitchSecret(ctx, cli, target.Name, target.UID, func(s *corev1.Secret) {
		s.Annotations[v1.AnnotationInstallDigest] = digest
		s.Annotations[v1.AnnotationInstallError] = truncateBytes(message, 1024)
	})
}

func clearPendingSwitch(ctx context.Context, cli client.Client, target *corev1.Secret) error {
	return patchSwitchSecret(ctx, cli, target.Name, target.UID, func(s *corev1.Secret) { delete(s.Annotations, pendingSwitch) })
}

func advanceSwitch(ctx context.Context, cli client.Client, target, record *corev1.Secret, j *switchJournal) error {
	switch j.Phase {
	case "planning":
		return startSwitch(ctx, cli, target, record, j)
	case "prepared", "rollback":
		if err := undoSwitch(ctx, cli, target, record, j); err != nil {
			return err
		}
		if !j.Terminal {
			if j.Failure != "" {
				return errors.New(j.Failure)
			}
			return fmt.Errorf("interrupted package switch rolled back; retry activation of %s", target.Name)
		}
		return nil
	case "committed":
		return finishSwitch(ctx, cli, target, record, j)
	default:
		return fmt.Errorf("unknown phase %q for package %s", j.Phase, j.Target)
	}
}

func startSwitch(ctx context.Context, cli client.Client, target, record *corev1.Secret, j *switchJournal) error {
	mp := new(v1.MiddlewarePackage)
	if err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKey{Name: target.Name}, mp); err != nil {
		return err
	}
	if packageReleaseDigest(target) != j.Digest {
		return failSwitch(ctx, cli, target, record, j, fmt.Errorf("package archive changed before activation"))
	}
	pkg, err := packages.Get(ctx, cli, target.Name)
	if err != nil {
		return failSwitch(ctx, cli, target, record, j, err)
	}
	j.Sources, err = activeSources(ctx, cli, target)
	if err != nil {
		return failSwitch(ctx, cli, target, record, j, err)
	}
	token := string(record.UID)
	writes, err := prepareDefinitions(ctx, cli, mp, pkg, j, token)
	if err != nil {
		// No writes have happened; discard the partial preflight plan.
		j.Changes = nil
		return failSwitch(ctx, cli, target, record, j, err)
	}
	// Older installed archives may predate the package finalizer. Pin them
	// before handover so their owners cannot be garbage collected mid-switch.
	for _, src := range j.Sources {
		err := patchSwitchSecret(ctx, cli, src.Name, src.SecretUID, func(s *corev1.Secret) { controllerutil.AddFinalizer(s, v1.FinalizerPackageSecret) })
		if err != nil {
			j.Changes = nil
			return failSwitch(ctx, cli, target, record, j, err)
		}
	}
	j.Phase = "prepared"
	if err := saveSwitchJournal(ctx, cli, record, j); err != nil {
		return err
	}
	for i, next := range writes {
		step := j.Changes[i]
		if err := writeDefinition(ctx, cli, next, step.Before == nil, false); err != nil {
			return failSwitch(ctx, cli, target, record, j, fmt.Errorf("publish %s %s: %w", step.Key.Kind, step.Key.Name, err))
		}
	}
	for i, step := range j.Changes {
		live, err := readDefinition(ctx, cli, step.Key)
		if err != nil {
			return failSwitch(ctx, cli, target, record, j, err)
		}
		if live == nil {
			return failSwitch(ctx, cli, target, record, j, fmt.Errorf("published %s %s is missing", step.Key.Kind, step.Key.Name))
		}
		hash, err := definitionHash(live)
		if err != nil {
			return failSwitch(ctx, cli, target, record, j, err)
		}
		if live.GetUID() != writes[i].GetUID() || hash != step.Expected {
			return failSwitch(ctx, cli, target, record, j, fmt.Errorf("package definition %s %s did not converge", step.Key.Kind, step.Key.Name))
		}
	}
	current := new(corev1.Secret)
	if err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKeyFromObject(target), current); err != nil {
		return failSwitch(ctx, cli, target, record, j, err)
	}
	_, requested := current.Annotations[v1.LabelInstall]
	if current.UID != j.TargetUID || current.DeletionTimestamp != nil || !requested || packageReleaseDigest(current) != j.Digest {
		return failSwitch(ctx, cli, target, record, j, fmt.Errorf("package activation request changed before commit"))
	}
	j.Phase = "committed"
	if err := saveSwitchJournal(ctx, cli, record, j); err != nil {
		return err
	}
	return finishSwitch(ctx, cli, target, record, j)
}

func failSwitch(ctx context.Context, cli client.Client, target, record *corev1.Secret, j *switchJournal, cause error) error {
	j.Phase = "rollback"
	j.Terminal = isTerminalInstallError(cause) || errors.Is(cause, errInvalidDefinition)
	j.Failure = truncateBytes(cause.Error(), 1024)
	if err := saveSwitchJournal(ctx, cli, record, j); err != nil {
		return err
	}
	if err := undoSwitch(ctx, cli, target, record, j); err != nil {
		return err
	}
	if !j.Terminal {
		return cause
	}
	return nil
}
func undoSwitch(ctx context.Context, cli client.Client, target, record *corev1.Secret, j *switchJournal) error {
	if err := rollbackDefinitions(ctx, cli, j, string(record.UID)); err != nil {
		return err
	}
	if j.Terminal {
		err := recordInstallError(ctx, cli, target, j.Digest, j.Failure)
		if err != nil {
			return err
		}
	}
	if err := deleteSwitchJournal(ctx, cli, record); err != nil {
		return err
	}
	if err := clearPendingSwitch(ctx, cli, target); err != nil {
		return err
	}

	return nil
}

func finishSwitch(ctx context.Context, cli client.Client, target, record *corev1.Secret, j *switchJournal) error {
	// Keep pendingSwitch until cleanup is complete. On a restart a Secret
	// create event resumes this even if the install request was already cleared.
	err := patchSwitchSecret(ctx, cli, target.Name, target.UID, func(s *corev1.Secret) {
		s.Labels[v1.LabelEnabled] = "true"
		s.Labels[v1.LabelProject] = consts.ProjectOpenSaola
		delete(s.Annotations, v1.AnnotationInstallError)
		delete(s.Annotations, v1.AnnotationInstallDigest)
		// enabled publishes the committed result. Retiring old definitions may still need retries.
	})
	if err != nil {
		return err
	}
	for _, src := range j.Sources {
		err := patchSwitchSecret(ctx, cli, src.Name, src.SecretUID, func(s *corev1.Secret) { delete(s.Labels, v1.LabelEnabled) })
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		mp := src.packageObject()
		for _, key := range src.Retire {
			if err := deletePackageResource(ctx, cli, mp, key.Name, key.object()); err != nil {
				return err
			}
		}
	}
	err = patchSwitchSecret(ctx, cli, target.Name, target.UID, func(s *corev1.Secret) { delete(s.Annotations, v1.LabelInstall) })
	if err != nil {
		return err
	}
	if err := deleteSwitchJournal(ctx, cli, record); err != nil {
		return err
	}
	return clearPendingSwitch(ctx, cli, target)
}

func settleBeforeUninstall(ctx context.Context, cli client.Client, target *corev1.Secret) error {
	record, j, err := readSwitchJournal(ctx, cli, packageComponent(target))
	if err != nil || record == nil {
		return err
	}
	if j.Target != target.Name || j.TargetUID != target.UID {
		return fmt.Errorf("component is switching to %s; retry uninstall after activation", j.Target)
	}
	if j.Phase == "committed" {
		return finishSwitch(ctx, cli, target, record, j)
	}
	if err := rollbackDefinitions(ctx, cli, j, string(record.UID)); err != nil {
		return err
	}
	return deleteSwitchJournal(ctx, cli, record)
}
