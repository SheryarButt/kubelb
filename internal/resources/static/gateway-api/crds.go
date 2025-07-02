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

	"github.com/go-logr/logr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Channel represents the Gateway API channel
type Channel string

const (
	ChannelStandard     Channel = "standard"
	ChannelExperimental Channel = "experimental"
)

var (
	//go:embed manifests/standard/gateway-api-crds.yaml
	standardGatewayAPICRDs []byte

	//go:embed manifests/experimental/gateway-api-crds.yaml
	experimentalGatewayAPICRDs []byte
)

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

// InstallGatewayAPICRDs installs the Gateway API CRDs for the specified channel
func InstallGatewayAPICRDs(ctx context.Context, log logr.Logger, client client.Client, channel Channel) error {
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
