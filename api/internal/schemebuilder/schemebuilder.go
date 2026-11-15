/*
Copyright 2026.

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

// Package schemebuilder replaces the deprecated controller-runtime
// pkg/scheme.Builder with a minimal equivalent built on apimachinery only.
package schemebuilder

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Builder collects the Go types of one group-version and registers them in a
// runtime.Scheme. Its Register and AddToScheme methods keep the call shape the
// kubebuilder-scaffolded *_types.go files already use.
type Builder struct {
	GroupVersion schema.GroupVersion
	objects      []runtime.Object
}

// Register records the given objects for registration. It returns the builder
// so calls can be chained, like the controller-runtime version.
func (b *Builder) Register(objs ...runtime.Object) *Builder {
	b.objects = append(b.objects, objs...)
	return b
}

// AddToScheme registers every recorded object, and the group-version's list
// and watch option types, in the given scheme.
func (b *Builder) AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(b.GroupVersion, b.objects...)
	metav1.AddToGroupVersion(s, b.GroupVersion)
	return nil
}
