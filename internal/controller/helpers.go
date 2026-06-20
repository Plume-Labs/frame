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

package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func setCondition(conditions *[]metav1.Condition, c metav1.Condition) {
	c.LastTransitionTime = metav1.Now()
	for i, existing := range *conditions {
		if existing.Type == c.Type {
			if existing.Status != c.Status {
				(*conditions)[i] = c
			}
			return
		}
	}
	*conditions = append(*conditions, c)
}

func conditionStatus(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func corev1GVK(version, kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "", Version: version, Kind: kind}
}
