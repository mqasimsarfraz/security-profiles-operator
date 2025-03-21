/*
Copyright 2020 The Kubernetes Authors.

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

package spod

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	spodv1alpha1 "sigs.k8s.io/security-profiles-operator/api/spod/v1alpha1"
	"sigs.k8s.io/security-profiles-operator/internal/pkg/config"
	"sigs.k8s.io/security-profiles-operator/internal/pkg/daemon/metrics"
	"sigs.k8s.io/security-profiles-operator/internal/pkg/manager/spod/bindata"
)

// CtxKey type for spod context keys.
type CtxKey string

const (
	// ManageWebhookKey value key used in the Setup.Context for ManageWebhook value.
	ManageWebhookKey  CtxKey = "ManageWebhook"
	selinuxdImageKey  string = "RELATED_IMAGE_SELINUXD"
	rbacProxyImageKey string = "RELATED_IMAGE_RBAC_PROXY"
)

// daemonTunables defines the parameters to tune/modify for the
// Security-Profiles-Operator-Daemon.
type daemonTunables struct {
	selinuxdImage    string
	rbacProxyImage   string
	logEnricherImage string
	watchNamespace   string
}

// Setup adds a controller that reconciles the SPOd DaemonSet.
func (r *ReconcileSPOd) Setup(
	ctx context.Context,
	mgr ctrl.Manager,
	met *metrics.Metrics,
) error {
	r.client = mgr.GetClient()
	r.log = ctrl.Log.WithName(r.Name())
	r.record = event.NewAPIRecorder(mgr.GetEventRecorderFor(r.Name()))

	dt, err := getTunables()
	if err != nil {
		return fmt.Errorf("get tunables: %w", err)
	}

	r.baseSPOd = getEffectiveSPOd(dt)

	if err := r.createConfigIfNotExist(ctx); err != nil {
		return fmt.Errorf("create config if not existing: %w", err)
	}

	r.scheme = mgr.GetScheme()
	r.watchNamespace = dt.watchNamespace
	r.namespace = config.GetOperatorNamespace()

	return ctrl.NewControllerManagedBy(mgr).
		Named(r.Name()).
		For(&spodv1alpha1.SecurityProfilesOperatorDaemon{}).
		Owns(&appsv1.DaemonSet{}).
		WithEventFilter(resource.NewPredicates(isInOperatorNamespace)).
		Complete(r)
}

func (r *ReconcileSPOd) createConfigIfNotExist(ctx context.Context) error {
	obj := bindata.DefaultSPOD.DeepCopy()
	obj.Namespace = config.GetOperatorNamespace()
	obj.Spec.StaticWebhookConfig = isStaticWebhook(ctx)

	if err := r.client.Create(ctx, obj); err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("create SecurityProfilesOperatorDaemon object: %w", err)
	}

	return nil
}

func isStaticWebhook(ctx context.Context) bool {
	v, ok := ctx.Value(ManageWebhookKey).(bool)
	if ok {
		return !v
	}
	// the webhook is by default managed by the operator
	return false
}

func getTunables() (*daemonTunables, error) {
	dt := &daemonTunables{}
	dt.watchNamespace = os.Getenv(config.RestrictNamespaceEnvKey)

	selinuxdImage := os.Getenv(selinuxdImageKey)
	if selinuxdImage == "" {
		return dt, errors.New("invalid selinuxd image")
	}
	dt.selinuxdImage = selinuxdImage

	rbacProxyImage := os.Getenv(rbacProxyImageKey)
	if rbacProxyImage == "" {
		return dt, errors.New("invalid rbac proxy image")
	}
	dt.rbacProxyImage = rbacProxyImage
	return dt, nil
}

func getEffectiveSPOd(dt *daemonTunables) *appsv1.DaemonSet {
	refSPOd := bindata.Manifest.DeepCopy()
	refSPOd.SetNamespace(config.GetOperatorNamespace())

	daemon := &refSPOd.Spec.Template.Spec.Containers[0]
	if dt.watchNamespace != "" {
		daemon.Env = append(daemon.Env, corev1.EnvVar{
			Name:  config.RestrictNamespaceEnvKey,
			Value: dt.watchNamespace,
		})
	}

	selinuxd := &refSPOd.Spec.Template.Spec.Containers[1]
	selinuxd.Image = dt.selinuxdImage

	logEnricher := &refSPOd.Spec.Template.Spec.Containers[2]
	logEnricher.Image = dt.logEnricherImage

	metrixCtr := &refSPOd.Spec.Template.Spec.Containers[4]
	metrixCtr.Image = dt.rbacProxyImage

	sepolImage := &refSPOd.Spec.Template.Spec.InitContainers[1]
	sepolImage.Image = dt.selinuxdImage // selinuxd ships the policies as well

	return refSPOd
}

func isInOperatorNamespace(obj runtime.Object) bool {
	spod, ok := obj.(*spodv1alpha1.SecurityProfilesOperatorDaemon)
	if !ok {
		return false
	}

	return spod.GetNamespace() == config.GetOperatorNamespace()
}
