/*
 * Copyright 2026 The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package features

import (
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/component-base/featuregate"
	logsapi "k8s.io/component-base/logs/api/v1"
)

// NewFeatureGate returns the component's feature-gate registry. It contains
// gates owned by the component and its dependencies. A new registry is
// returned for each application instance to keep flag parsing isolated in
// tests.
func NewFeatureGate() featuregate.MutableVersionedFeatureGate {
	featureGate := featuregate.NewFeatureGate()
	utilruntime.Must(logsapi.AddFeatureGates(featureGate))
	utilruntime.Must(featureGate.SetFromMap(map[string]bool{string(logsapi.ContextualLogging): true}))
	return featureGate
}
