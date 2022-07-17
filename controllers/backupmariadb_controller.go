/*
Copyright 2022.

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

package controllers

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-multierror"
	databasev1alpha1 "github.com/mmontes11/mariadb-operator/api/v1alpha1"
	"github.com/mmontes11/mariadb-operator/pkg/builders"
	"github.com/mmontes11/mariadb-operator/pkg/conditions"
	"github.com/mmontes11/mariadb-operator/pkg/refresolver"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// BackupMariaDBReconciler reconciles a BackupMariaDB object
type BackupMariaDBReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	RefResolver       *refresolver.RefResolver
	ConditionComplete *conditions.ConditionComplete
}

//+kubebuilder:rbac:groups=database.mmontes.io,resources=backupmariadbs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=database.mmontes.io,resources=backupmariadbs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=database.mmontes.io,resources=backupmariadbs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *BackupMariaDBReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var backup databasev1alpha1.BackupMariaDB
	if err := r.Get(ctx, req.NamespacedName, &backup); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var pvcErr *multierror.Error
	if err := r.createPVC(ctx, &backup, req.NamespacedName); err != nil {
		pvcErr = multierror.Append(pvcErr, err)

		err = r.patchStatus(ctx, &backup, r.ConditionComplete.FailedPatcher("Failed creating PVC"))
		pvcErr = multierror.Append(pvcErr, err)

		return ctrl.Result{}, fmt.Errorf("error creating PVC: %v", pvcErr)
	}

	var jobErr *multierror.Error
	err := r.createJob(ctx, &backup, req.NamespacedName)
	jobErr = multierror.Append(jobErr, err)

	patcher, err := r.ConditionComplete.PatcherWithJob(ctx, err, req.NamespacedName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, fmt.Errorf("error getting patcher for BackupMariaDB: %v", err)
	}

	err = r.patchStatus(ctx, &backup, patcher)
	jobErr = multierror.Append(jobErr, err)

	if err := jobErr.ErrorOrNil(); err != nil {
		return ctrl.Result{}, fmt.Errorf("error creating Job: %v", err)
	}
	return ctrl.Result{}, nil
}

func (r *BackupMariaDBReconciler) createPVC(ctx context.Context, backup *databasev1alpha1.BackupMariaDB,
	key types.NamespacedName) error {
	var existingPvc v1.PersistentVolumeClaim
	if err := r.Get(ctx, key, &existingPvc); err == nil {
		return nil
	}

	pvcMeta := metav1.ObjectMeta{
		Name:      backup.Name,
		Namespace: backup.Namespace,
	}
	pvc := builders.BuildPVC(pvcMeta, &backup.Spec.Storage)
	if err := r.Create(ctx, pvc); err != nil {
		return fmt.Errorf("error creating PVC: %v", err)
	}
	return nil
}

func (r *BackupMariaDBReconciler) createJob(ctx context.Context, backup *databasev1alpha1.BackupMariaDB,
	key types.NamespacedName) error {
	var existingJob batchv1.Job
	if err := r.Get(ctx, key, &existingJob); err == nil {
		return nil
	}

	mariadb, err := r.RefResolver.GetMariaDB(ctx, backup.Spec.MariaDBRef, backup.Namespace)
	if err != nil {
		return fmt.Errorf("error getting MariaDB: %v", err)
	}

	job := builders.BuildBackupJob(backup, mariadb, key)
	if err := controllerutil.SetControllerReference(backup, job, r.Scheme); err != nil {
		return fmt.Errorf("error setting controller reference to Job: %v", err)
	}

	if err := r.Create(ctx, job); err != nil {
		return fmt.Errorf("error creating Job: %v", err)
	}
	return nil
}

func (r *BackupMariaDBReconciler) patchStatus(ctx context.Context, backup *databasev1alpha1.BackupMariaDB,
	patcher conditions.ConditionPatcher) error {
	patch := client.MergeFrom(backup.DeepCopy())
	patcher(&backup.Status)

	if err := r.Client.Status().Patch(ctx, backup, patch); err != nil {
		return fmt.Errorf("error patching BackupMariaDB status: %v", err)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BackupMariaDBReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&databasev1alpha1.BackupMariaDB{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
