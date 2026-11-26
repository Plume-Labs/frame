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

package updater

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// ClusterActivity counts what the Updates screen counts (src/lib/updates.ts):
// jobs running, installs not finished, OSD replacements not finished. The two
// have to agree, or the console would offer "Update" for a request the updater
// then refuses, or the other way round.
func ClusterActivity(c client.Reader) func(ctx context.Context) (Activity, error) {
	return func(ctx context.Context) (Activity, error) {
		var a Activity

		var jobs framev1beta1.FrameJobList
		if err := c.List(ctx, &jobs); err != nil {
			return a, err
		}
		for i := range jobs.Items {
			if jobRunning(&jobs.Items[i]) {
				a.RunningJobs++
			}
		}

		var installs framev1beta1.FrameInstallList
		if err := c.List(ctx, &installs); err != nil {
			return a, err
		}
		for i := range installs.Items {
			if p := installs.Items[i].Status.Phase; p != "Ready" && p != "Failed" {
				a.InstallsInProgress++
			}
		}

		var repl framev1beta1.FrameOSDReplacementList
		if err := c.List(ctx, &repl); err != nil {
			return a, err
		}
		for i := range repl.Items {
			switch repl.Items[i].Status.Phase {
			case "Succeeded", "Cancelled", "Failed":
			default:
				a.OSDReplacementsActive++
			}
		}
		return a, nil
	}
}

// jobRunning is a FrameJob whose Ready condition says it is running (the console counts the same).
func jobRunning(j *framev1beta1.FrameJob) bool {
	c := meta.FindStatusCondition(j.Status.Conditions, "Ready")
	return c != nil && c.Reason == "Running"
}
