/*
Copyright 2025 The KubeLB Authors.

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

package ccm

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	gwapicrd "k8c.io/kubelb/internal/resources/static/gateway-api"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	GatewayCRDControllerName = "gateway-crd-controller"
	GatewayAPIGroup          = "gateway.networking.k8s.io"
	BootstrapObjectName      = "gateway-api-bootstrap"
)

type GatewayCRDReconciler struct {
	client.Client
	Channel string
	Log     logr.Logger
}

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions/status,verbs=get;update;patch
func (r *GatewayCRDReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := r.Log.WithValues("name", req.NamespacedName)
	log.Info("Reconciling Gateway API CRDs")

	if r.Channel == "" {
		return reconcile.Result{}, fmt.Errorf("gateway API channel is not set")
	}

	channel := gwapicrd.Channel(r.Channel)
	if err := gwapicrd.InstallGatewayAPICRDs(ctx, log, r.Client, channel); err != nil {
		log.Error(err, "failed to install Gateway API CRDs")
		// Requeue after 30 seconds for retry
		return reconcile.Result{RequeueAfter: time.Second * 30}, fmt.Errorf("failed to install Gateway API CRDs: %w", err)
	}
	log.Info("Successfully installed Gateway API CRDs")

	return reconcile.Result{}, nil
}

func (r *GatewayCRDReconciler) resourceFilter() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return false // Only reconcile on update, delete, or bootstrap
		},
		UpdateFunc: isGatewayAPICRDUpdate,
		DeleteFunc: isGatewayAPICRDDelete,
	}
}

// isGatewayAPICRDUpdate determines if an update event should trigger reconciliation
func isGatewayAPICRDUpdate(e event.UpdateEvent) bool {
	oldCRD, ok := e.ObjectOld.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return false
	}
	newCRD, ok := e.ObjectNew.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return false
	}

	// Skip if no actual change
	if oldCRD.Generation == newCRD.Generation {
		return false
	}

	// Only reconcile Gateway API CRDs
	return isGatewayAPICRD(oldCRD) && isGatewayAPICRD(newCRD)
}

// isGatewayAPICRDDelete determines if a delete event should trigger reconciliation
func isGatewayAPICRDDelete(e event.DeleteEvent) bool {
	crd, ok := e.Object.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return false
	}
	return isGatewayAPICRD(crd)
}

// isGatewayAPICRD checks if a CRD belongs to the Gateway API group
func isGatewayAPICRD(crd *apiextensionsv1.CustomResourceDefinition) bool {
	return crd.Spec.Group == GatewayAPIGroup
}

// createBootstrapChannel creates a channel that simulates a bootstrap event
func (r *GatewayCRDReconciler) createBootstrapChannel() chan event.GenericEvent {
	bootstrapping := make(chan event.GenericEvent, 1)
	bootstrapping <- event.GenericEvent{
		Object: &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: BootstrapObjectName,
			},
		},
	}
	return bootstrapping
}

func (r *GatewayCRDReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(GatewayCRDControllerName).
		For(&apiextensionsv1.CustomResourceDefinition{}, builder.WithPredicates(r.resourceFilter())).
		WatchesRawSource(
			source.Channel(r.createBootstrapChannel(), &handler.EnqueueRequestForObject{}),
		).
		Complete(r)
}
