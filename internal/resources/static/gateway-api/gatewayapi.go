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

package gatewayapi

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/go-logr/logr"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	ChannelStandard     Channel = "standard"
	ChannelExperimental Channel = "experimental"
	ControllerName              = "gateway-crd-controller"
	GatewayAPIGroup             = "gateway.networking.k8s.io"
	BootstrapObjectName         = "gateway-api-bootstrap"
)

// Channel represents the Gateway API channel
type Channel string

var (
	//go:embed manifests/standard/gateway-api-crds.yaml
	standardGatewayAPICRDs []byte

	//go:embed manifests/experimental/gateway-api-crds.yaml
	experimentalGatewayAPICRDs []byte
)

type Reconciler struct {
	client.Client
	Log     logr.Logger
	Channel string
}

// Add adds the Gateway API controller to the manager
func Add(mgr manager.Manager, log logr.Logger, channel string) error {
	reconciler, err := NewReconciler(mgr.GetClient(), log, channel)
	if err != nil {
		return fmt.Errorf("failed to create reconciler: %w", err)
	}

	return setupController(mgr, reconciler)
}

// setupController configures the controller with appropriate predicates and sources
func setupController(mgr manager.Manager, reconciler *Reconciler) error {
	bootstrapping := createBootstrapChannel()

	_, err := builder.ControllerManagedBy(mgr).
		Named(ControllerName).
		For(&apiextensionsv1.CustomResourceDefinition{},
			builder.WithPredicates(createPredicates())).
		WatchesRawSource(source.Channel(bootstrapping, &handler.EnqueueRequestForObject{})).
		Build(reconciler)

	return err
}

// createBootstrapChannel creates a channel for bootstrap events
func createBootstrapChannel() chan event.GenericEvent {
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

// createPredicates creates the predicate functions for the controller
func createPredicates() predicate.Predicate {
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

func NewReconciler(client client.Client, log logr.Logger, channel string) (*Reconciler, error) {
	// Validate channel
	if channel != string(ChannelStandard) && channel != string(ChannelExperimental) {
		return nil, fmt.Errorf("invalid Gateway API channel: %s, must be either %s or %s",
			channel, ChannelStandard, ChannelExperimental)
	}

	reconciler := &Reconciler{
		Client:  client,
		Log:     log.WithName(ControllerName),
		Channel: channel,
	}

	return reconciler, nil
}

// Reconcile implements the reconcile.Reconciler interface
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := r.Log.WithValues("name", req.NamespacedName)
	log.Info("Reconciling Gateway API CRDs")

	if r.Channel == "" {
		return reconcile.Result{}, fmt.Errorf("gateway API channel is not set")
	}

	// Install Gateway API CRDs if enabled
	channel := Channel(r.Channel)
	log.Info("Installing Gateway API CRDs", "channel", channel)

	if err := installGatewayAPICRDs(ctx, log, r.Client, channel); err != nil {
		log.Error(err, "failed to install Gateway API CRDs")
		// Requeue after 30 seconds for retry
		return reconcile.Result{RequeueAfter: time.Second * 30}, fmt.Errorf("failed to install Gateway API CRDs: %w", err)
	}
	log.Info("Successfully installed Gateway API CRDs")

	return reconcile.Result{}, nil
}

// GetGatewayAPICRDs returns the embedded Gateway API CRDs for the specified channel
func getGatewayAPICRDs(channel Channel) ([]byte, error) {
	switch channel {
	case ChannelStandard:
		return standardGatewayAPICRDs, nil
	case ChannelExperimental:
		return experimentalGatewayAPICRDs, nil
	default:
		return nil, fmt.Errorf("unsupported Gateway API channel: %s", channel)
	}
}

// installGatewayAPICRDs installs the Gateway API CRDs for the specified channel
func installGatewayAPICRDs(ctx context.Context, log logr.Logger, client client.Client, channel Channel) error {
	log.Info("Installing Gateway API CRDs", "channel", channel)

	crdData, err := getGatewayAPICRDs(channel)
	if err != nil {
		return fmt.Errorf("failed to get Gateway API CRDs: %w", err)
	}

	// Split the YAML document into individual CRDs
	decoder := yaml.NewYAMLToJSONDecoder(bytes.NewReader(crdData))
	docIndex := 0

	for {
		var rawObj map[string]any
		err := decoder.Decode(&rawObj)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to decode CRD document %d: %w", docIndex, err)
		}
		docIndex++

		if rawObj == nil {
			continue
		}

		// Convert to unstructured object
		obj := &unstructured.Unstructured{Object: rawObj}

		// Skip if it's not a CRD
		if obj.GetKind() != "CustomResourceDefinition" {
			continue
		}

		log.Info("Processing CRD", "name", obj.GetName(), "kind", obj.GetKind())

		// Try to create the CRD, if it already exists, update it
		err = client.Create(ctx, obj)
		if err != nil {
			if apierrors.IsAlreadyExists(err) {
				// CRD already exists, try to update it
				existing := &unstructured.Unstructured{}
				existing.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())
				err = client.Get(ctx, types.NamespacedName{Name: obj.GetName()}, existing)
				if err != nil {
					return fmt.Errorf("failed to get existing CRD %s: %w", obj.GetName(), err)
				}

				// Update the spec
				obj.SetResourceVersion(existing.GetResourceVersion())
				err = client.Update(ctx, obj)
				if err != nil {
					return fmt.Errorf("failed to update CRD %s: %w", obj.GetName(), err)
				}
				log.Info("Updated existing CRD", "name", obj.GetName())
			} else {
				return fmt.Errorf("failed to create CRD %s: %w", obj.GetName(), err)
			}
		} else {
			log.Info("Created CRD", "name", obj.GetName())
		}
	}

	log.Info("Successfully installed Gateway API CRDs", "channel", channel)
	return nil
}
