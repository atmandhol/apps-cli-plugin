/*
Copyright 2021 VMware, Inc.

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
package commands_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	diemetav1 "dies.dev/apis/meta/v1"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vmware-tanzu/apps-cli-plugin/pkg/apis"
	cartov1alpha1 "github.com/vmware-tanzu/apps-cli-plugin/pkg/apis/cartographer/v1alpha1"
	cli "github.com/vmware-tanzu/apps-cli-plugin/pkg/cli-runtime"
	"github.com/vmware-tanzu/apps-cli-plugin/pkg/cli-runtime/logs"
	clitesting "github.com/vmware-tanzu/apps-cli-plugin/pkg/cli-runtime/testing"
	"github.com/vmware-tanzu/apps-cli-plugin/pkg/cli-runtime/validation"
	watchhelper "github.com/vmware-tanzu/apps-cli-plugin/pkg/cli-runtime/watch"
	watchfakes "github.com/vmware-tanzu/apps-cli-plugin/pkg/cli-runtime/watch/fake"
	"github.com/vmware-tanzu/apps-cli-plugin/pkg/commands"
	diecartov1alpha1 "github.com/vmware-tanzu/apps-cli-plugin/pkg/dies/cartographer/v1alpha1"
	"github.com/vmware-tanzu/apps-cli-plugin/pkg/flags"
)

func TestWorkloadApplyOptionsValidate(t *testing.T) {
	table := clitesting.ValidatableTestSuite{
		{
			Name: "valid options",
			Validatable: &commands.WorkloadApplyOptions{
				WorkloadOptions: commands.WorkloadOptions{
					Namespace: "default",
					Name:      "my-resource",
					Env:       []string{"FOO=bar"},
				},
			},
			ShouldValidate: true,
		},
		{
			Name: "invalid options",
			Validatable: &commands.WorkloadApplyOptions{
				WorkloadOptions: commands.WorkloadOptions{
					Namespace: "default",
					Name:      "my-resource",
					Env:       []string{"FOO"},
				},
			},
			ExpectFieldErrors: validation.ErrInvalidArrayValue("FOO", flags.EnvFlagName, 0),
		},
	}

	table.Run(t)
}

func TestWorkloadApplyCommand(t *testing.T) {
	defaultNamespace := "default"
	workloadName := "my-workload"
	file := "testdata/workload.yaml"
	gitRepo := "https://example.com/repo.git"
	gitBranch := "main"
	serviceAccountName := "my-service-account"
	serviceAccountNameUpdated := "my-service-account-updated"

	scheme := runtime.NewScheme()
	_ = cartov1alpha1.AddToScheme(scheme)

	var cmd *cobra.Command

	parent := diecartov1alpha1.WorkloadBlank.
		MetadataDie(func(d *diemetav1.ObjectMetaDie) {
			d.Name(workloadName)
			d.Namespace(defaultNamespace)
		})

	table := clitesting.CommandTestSuite{
		{
			Name:        "invalid args",
			Args:        []string{},
			ShouldError: true,
		},
		{
			Name: "get failed",
			Args: []string{flags.FilePathFlagName, file},
			WithReactors: []clitesting.ReactionFunc{
				clitesting.InduceFailure("get", "Workload"),
			},
			ShouldError: true,
		},
		{
			Name: "dry run",
			Args: []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.DryRunFlagName, flags.YesFlagName},
			ExpectOutput: `
---
apiVersion: carto.run/v1alpha1
kind: Workload
metadata:
  creationTimestamp: null
  name: my-workload
  namespace: default
spec:
  source:
    git:
      ref:
        branch: main
      url: https://example.com/repo.git
status:
  supplyChainRef: {}
`,
		},
		{
			Name: "git source with subPath",
			Args: []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.SubPathFlagName, "./app", flags.YesFlagName},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: gitRepo,
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
							Subpath: "./app",
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  name: my-workload
      6 + |  namespace: default
      7 + |spec:
      8 + |  source:
      9 + |    git:
     10 + |      ref:
     11 + |        branch: main
     12 + |      url: https://example.com/repo.git
     13 + |    subPath: ./app

Created workload "my-workload"
`,
		},
		{
			Name: "Update git source with subPath from file",
			Args: []string{workloadName, flags.FilePathFlagName, "./testdata/workload-subPath.yaml", flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Source(&cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/spring-projects/spring-petclinic.git",
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
						})
					}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/spring-projects/spring-petclinic.git",
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
							Subpath: "./app",
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  9,  9   |    git:
 10, 10   |      ref:
 11, 11   |        branch: main
 12, 12   |      url: https://github.com/spring-projects/spring-petclinic.git
     13 + |    subPath: ./app

Updated workload "my-workload"
`,
		},
		{
			Name: "Create git source with subPath from file",
			Args: []string{workloadName, flags.FilePathFlagName, "./testdata/workload-subPath.yaml", flags.YesFlagName},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/spring-projects/spring-petclinic.git",
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
							Subpath: "./app",
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  name: my-workload
      6 + |  namespace: default
      7 + |spec:
      8 + |  source:
      9 + |    git:
     10 + |      ref:
     11 + |        branch: main
     12 + |      url: https://github.com/spring-projects/spring-petclinic.git
     13 + |    subPath: ./app

Created workload "my-workload"
`,
		},
		{
			Name:        "subPath with no source",
			Args:        []string{workloadName, flags.SubPathFlagName, "./app", flags.YesFlagName},
			ShouldError: true,
		},
		{
			Name: "wait with timeout error",
			Args: []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.YesFlagName, flags.WaitFlagName, flags.WaitTimeoutFlagName, "1ns"},
			Prepare: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) (context.Context, error) {
				workload := &cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Status: cartov1alpha1.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:   cartov1alpha1.WorkloadConditionReady,
								Status: metav1.ConditionTrue,
							},
						},
					},
				}
				fakeWatcher := watchfakes.NewFakeWithWatch(false, config.Client, []watch.Event{
					{Type: watch.Modified, Object: workload},
				})
				ctx = watchhelper.WithWatcher(ctx, fakeWatcher)
				return ctx, nil
			},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: gitRepo,
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
						},
					},
				},
			},
			ShouldError: true,
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  name: my-workload
      6 + |  namespace: default
      7 + |spec:
      8 + |  source:
      9 + |    git:
     10 + |      ref:
     11 + |        branch: main
     12 + |      url: https://example.com/repo.git

Created workload "my-workload"
Waiting for workload "my-workload" to become ready...
Error: timeout after 1ns waiting for "my-workload" to become ready
To view status run: tanzu apps workload get my-workload --namespace default
`,
		},
		{
			Name: "successful wait for ready cond",
			Args: []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.YesFlagName, flags.WaitFlagName},
			Prepare: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) (context.Context, error) {
				workload := &cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Status: cartov1alpha1.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:   cartov1alpha1.WorkloadConditionReady,
								Status: metav1.ConditionTrue,
							},
						},
					},
				}
				fakeWatcher := watchfakes.NewFakeWithWatch(false, config.Client, []watch.Event{
					{Type: watch.Modified, Object: workload},
				})
				ctx = watchhelper.WithWatcher(ctx, fakeWatcher)
				return ctx, nil
			},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: gitRepo,
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  name: my-workload
      6 + |  namespace: default
      7 + |spec:
      8 + |  source:
      9 + |    git:
     10 + |      ref:
     11 + |        branch: main
     12 + |      url: https://example.com/repo.git

Created workload "my-workload"
Waiting for workload "my-workload" to become ready...
Workload "my-workload" is ready
`,
		},
		{
			Name: "tail while waiting for ready cond",
			Args: []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.YesFlagName, flags.TailFlagName},
			Prepare: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) (context.Context, error) {
				workload := &cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Status: cartov1alpha1.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:   cartov1alpha1.WorkloadConditionReady,
								Status: metav1.ConditionTrue,
							},
						},
					},
				}
				fakeWatcher := watchfakes.NewFakeWithWatch(false, config.Client, []watch.Event{
					{Type: watch.Modified, Object: workload},
				})
				ctx = watchhelper.WithWatcher(ctx, fakeWatcher)

				tailer := &logs.FakeTailer{}
				selector, _ := labels.Parse(fmt.Sprintf("%s=%s", cartov1alpha1.WorkloadLabelName, workloadName))
				tailer.On("Tail", mock.Anything, "default", selector, []string{}, time.Second, false).Return(nil).Once()
				ctx = logs.StashTailer(ctx, tailer)

				return ctx, nil
			},
			CleanUp: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) error {
				tailer := logs.RetrieveTailer(ctx).(*logs.FakeTailer)
				tailer.AssertExpectations(t)
				return nil
			},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: gitRepo,
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  name: my-workload
      6 + |  namespace: default
      7 + |spec:
      8 + |  source:
      9 + |    git:
     10 + |      ref:
     11 + |        branch: main
     12 + |      url: https://example.com/repo.git

Created workload "my-workload"
Waiting for workload "my-workload" to become ready...
...tail output...
Workload "my-workload" is ready
`,
		},
		{
			Name: "error during create",
			Args: []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.YesFlagName},
			WithReactors: []clitesting.ReactionFunc{
				clitesting.InduceFailure("create", "Workload"),
			},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: gitRepo,
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
						},
					},
				},
			},
			ShouldError: true,
		},
		{
			Name: "watcher error",
			Args: []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.YesFlagName, flags.WaitFlagName},
			Prepare: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) (context.Context, error) {
				fakewatch := watchfakes.NewFakeWithWatch(true, config.Client, []watch.Event{})
				ctx = watchhelper.WithWatcher(ctx, fakewatch)
				return ctx, nil
			},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: gitRepo,
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
						},
					},
				},
			},
			ShouldError: true,
		},
		{
			Name: "create - wait error for false condition",
			Args: []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.LabelFlagName, "apps.tanzu.vmware.com/workload-type=web", flags.LabelFlagName, "apps.tanzu.vmware.com/workload-type-", flags.YesFlagName, flags.WaitFlagName},
			Prepare: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) (context.Context, error) {
				workload := &cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Status: cartov1alpha1.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:    cartov1alpha1.WorkloadConditionReady,
								Status:  metav1.ConditionFalse,
								Reason:  "OopsieDoodle",
								Message: "a hopefully informative message about what went wrong",
							},
						},
					},
				}
				fakeWatcher := watchfakes.NewFakeWithWatch(false, config.Client, []watch.Event{
					{Type: watch.Modified, Object: workload},
				})
				ctx = watchhelper.WithWatcher(ctx, fakeWatcher)
				return ctx, nil
			},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: gitRepo,
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
						},
					},
				},
			},
			ShouldError: true,
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  name: my-workload
      6 + |  namespace: default
      7 + |spec:
      8 + |  source:
      9 + |    git:
     10 + |      ref:
     11 + |        branch: main
     12 + |      url: https://example.com/repo.git

Created workload "my-workload"
Waiting for workload "my-workload" to become ready...
Error: Failed to become ready: a hopefully informative message about what went wrong
`,
		},
		{
			Name: "filepath",
			Args: []string{flags.FilePathFlagName, "testdata/workload.yaml", flags.YesFlagName},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							apis.AppPartOfLabelName:               "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/spring-projects/spring-petclinic.git",
								Ref: cartov1alpha1.GitRef{
									Branch: "main",
								},
							},
						},
						Env: []corev1.EnvVar{
							{
								Name:  "SPRING_PROFILES_ACTIVE",
								Value: "mysql",
							},
						},
						Resources: &corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
      8 + |  name: spring-petclinic
      9 + |  namespace: default
     10 + |spec:
     11 + |  env:
     12 + |  - name: SPRING_PROFILES_ACTIVE
     13 + |    value: mysql
     14 + |  resources:
     15 + |    limits:
     16 + |      cpu: 500m
     17 + |      memory: 1Gi
     18 + |    requests:
     19 + |      cpu: 100m
     20 + |      memory: 1Gi
     21 + |  source:
     22 + |    git:
     23 + |      ref:
     24 + |        branch: main
     25 + |      url: https://github.com/spring-projects/spring-petclinic.git

Created workload "spring-petclinic"
`,
		},
		{
			Name: "create - accept yaml file through stdin - using --yes flag",
			Args: []string{flags.FilePathFlagName, "-", flags.YesFlagName},
			Stdin: []byte(`
apiVersion: carto.run/v1alpha1
kind: Workload
metadata:
  name: spring-petclinic
  labels:
    app.kubernetes.io/part-of: spring-petclinic
    apps.tanzu.vmware.com/workload-type: web
spec:
  env:
  - name: SPRING_PROFILES_ACTIVE
    value: mysql
  resources:
    requests:
      memory: 1Gi
      cpu: 100m
    limits:
      memory: 1Gi
      cpu: 500m
  source:
    git:
      url: https://github.com/spring-projects/spring-petclinic.git
      ref:
        branch: main
`),
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							apis.AppPartOfLabelName:               "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/spring-projects/spring-petclinic.git",
								Ref: cartov1alpha1.GitRef{
									Branch: "main",
								},
							},
						},
						Env: []corev1.EnvVar{
							{
								Name:  "SPRING_PROFILES_ACTIVE",
								Value: "mysql",
							},
						},
						Resources: &corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
      8 + |  name: spring-petclinic
      9 + |  namespace: default
     10 + |spec:
     11 + |  env:
     12 + |  - name: SPRING_PROFILES_ACTIVE
     13 + |    value: mysql
     14 + |  resources:
     15 + |    limits:
     16 + |      cpu: 500m
     17 + |      memory: 1Gi
     18 + |    requests:
     19 + |      cpu: 100m
     20 + |      memory: 1Gi
     21 + |  source:
     22 + |    git:
     23 + |      ref:
     24 + |        branch: main
     25 + |      url: https://github.com/spring-projects/spring-petclinic.git

Created workload "spring-petclinic"
`,
		},
		{
			Name: "update - accept yaml file through stdin - using --yes flag",
			Args: []string{flags.FilePathFlagName, "-", flags.YesFlagName},
			Stdin: []byte(`
apiVersion: carto.run/v1alpha1
kind: Workload
metadata:
  name: spring-petclinic
  labels:
    app.kubernetes.io/part-of: spring-petclinic
    apps.tanzu.vmware.com/workload-type: web
spec:
  env:
  - name: SPRING_PROFILES_ACTIVE
    value: mysql
  resources:
    requests:
      memory: 1Gi
      cpu: 100m
    limits:
      memory: 1Gi
      cpu: 500m
  source:
    git:
      url: https://github.com/spring-projects/spring-petclinic.git
      ref:
        branch: main
`),
			GivenObjects: []client.Object{
				diecartov1alpha1.WorkloadBlank.
					MetadataDie(func(d *diemetav1.ObjectMetaDie) {
						d.Name("spring-petclinic")
						d.Namespace(defaultNamespace)
						d.AddLabel("preserve-me", "should-exist")
					}).
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
						d.Env(
							corev1.EnvVar{
								Name:  "OVERRIDE_VAR",
								Value: "doesnt matter",
							},
						)
					}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							"preserve-me":                         "should-exist",
							"app.kubernetes.io/part-of":           "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/spring-projects/spring-petclinic.git",
								Ref: cartov1alpha1.GitRef{
									Branch: "main",
								},
							},
						},
						Env: []corev1.EnvVar{
							{
								Name:  "OVERRIDE_VAR",
								Value: "doesnt matter",
							},
							{
								Name:  "SPRING_PROFILES_ACTIVE",
								Value: "mysql",
							},
						},
						Resources: &corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  2,  2   |apiVersion: carto.run/v1alpha1
  3,  3   |kind: Workload
  4,  4   |metadata:
  5,  5   |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
  6,  8   |    preserve-me: should-exist
  7,  9   |  name: spring-petclinic
  8, 10   |  namespace: default
  9, 11   |spec:
 10, 12   |  env:
 11, 13   |  - name: OVERRIDE_VAR
 12, 14   |    value: doesnt matter
 13     - |  image: ubuntu:bionic
     15 + |  - name: SPRING_PROFILES_ACTIVE
     16 + |    value: mysql
     17 + |  resources:
     18 + |    limits:
     19 + |      cpu: 500m
     20 + |      memory: 1Gi
     21 + |    requests:
     22 + |      cpu: 100m
     23 + |      memory: 1Gi
     24 + |  source:
     25 + |    git:
     26 + |      ref:
     27 + |        branch: main
     28 + |      url: https://github.com/spring-projects/spring-petclinic.git

Updated workload "spring-petclinic"
`,
		},
		{
			Name: "update - accept yaml file through stdin - using --dry-run flag",
			Args: []string{flags.FilePathFlagName, "-", flags.DryRunFlagName},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
					}),
			},
			Stdin: []byte(`
---
apiVersion: carto.run/v1alpha1
kind: Workload
metadata:
  creationTimestamp: null
  name: my-workload
  namespace: default
  resourceVersion: "999"
spec:
  image: ubuntu:bionic
status:
  supplyChainRef: {}
`),
		},
		{
			Name: "create - accept yaml file through stdin - using --dry-run flag",
			Args: []string{flags.FilePathFlagName, "-", flags.DryRunFlagName},
			Stdin: []byte(`
apiVersion: carto.run/v1alpha1
kind: Workload
metadata:
  creationTimestamp: null
  name: my-workload
  namespace: default
spec:
  source:
    git:
      ref:
        branch: main
      url: https://example.com/repo.git
status:
  supplyChainRef: {}
`),
		},
		{
			Name: "filepath - service account build-env",
			Args: []string{flags.FilePathFlagName, "testdata/workload-build-env.yaml", flags.YesFlagName},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							apis.AppPartOfLabelName:               "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Build: &cartov1alpha1.WorkloadBuild{Env: []corev1.EnvVar{
							{Name: "BP_MAVEN_POM_FILE", Value: "skip-pom.xml"},
						}},
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/spring-projects/spring-petclinic.git",
								Ref: cartov1alpha1.GitRef{
									Branch: "main",
								},
							},
						},
						Env: []corev1.EnvVar{
							{
								Name:  "SPRING_PROFILES_ACTIVE",
								Value: "mysql",
							},
						},
						Resources: &corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
      8 + |  name: spring-petclinic
      9 + |  namespace: default
     10 + |spec:
     11 + |  build:
     12 + |    env:
     13 + |    - name: BP_MAVEN_POM_FILE
     14 + |      value: skip-pom.xml
     15 + |  env:
     16 + |  - name: SPRING_PROFILES_ACTIVE
     17 + |    value: mysql
     18 + |  resources:
     19 + |    limits:
     20 + |      cpu: 500m
     21 + |      memory: 1Gi
     22 + |    requests:
     23 + |      cpu: 100m
     24 + |      memory: 1Gi
     25 + |  source:
     26 + |    git:
     27 + |      ref:
     28 + |        branch: main
     29 + |      url: https://github.com/spring-projects/spring-petclinic.git

Created workload "spring-petclinic"
`,
		},
		{
			Name:        "fail to accept yaml file through stdin missing --yes flag",
			Args:        []string{flags.FilePathFlagName, "-"},
			ShouldError: true,
		},
		{
			Name:        "filepath - missing",
			Args:        []string{workloadName, flags.FilePathFlagName, "testdata/missing.yaml", flags.YesFlagName},
			ShouldError: true,
		},
		{
			Name: "noop",
			Args: []string{workloadName},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
					}),
			},
			ExpectOutput: `
Workload is unchanged, skipping update
`,
		},
		{
			Name: "invalid resource",
			Args: []string{workloadName},
			GivenObjects: []client.Object{
				parent,
			},
			ShouldError: true,
			CleanUp: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) error {
				if expected, actual := false, cmd.SilenceUsage; expected != actual {
					t.Errorf("expected cmd.SilenceUsage to be %t, actually %t", expected, actual)
				}
				return nil
			},
		},
		{
			Name: "get failed",
			Args: []string{workloadName},
			WithReactors: []clitesting.ReactionFunc{
				clitesting.InduceFailure("get", "Workload"),
			},
			ShouldError: true,
		},
		{
			Name: "update - dry run",
			Args: []string{workloadName, flags.DebugFlagName, flags.DryRunFlagName, flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
					}),
			},
			ExpectOutput: `
---
apiVersion: carto.run/v1alpha1
kind: Workload
metadata:
  creationTimestamp: null
  name: my-workload
  namespace: default
  resourceVersion: "999"
spec:
  image: ubuntu:bionic
  params:
  - name: debug
    value: "true"
status:
  supplyChainRef: {}
`,
		},
		{
			Name: "error during update",
			Args: []string{workloadName, flags.DebugFlagName, flags.YesFlagName},
			WithReactors: []clitesting.ReactionFunc{
				clitesting.InduceFailure("update", "Workload"),
			},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
					}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Image: "ubuntu:bionic",
						Params: []cartov1alpha1.Param{
							{
								Name:  "debug",
								Value: apiextensionsv1.JSON{Raw: []byte(`"true"`)},
							},
						},
					},
				},
			},
			ShouldError: true,
		},
		{
			Name: "conflict during update",
			Args: []string{workloadName, flags.DebugFlagName, flags.YesFlagName},
			WithReactors: []clitesting.ReactionFunc{
				clitesting.InduceFailure("update", "Workload", clitesting.InduceFailureOpts{
					Error: apierrs.NewConflict(schema.GroupResource{Group: "carto.run", Resource: "workloads"}, workloadName, fmt.Errorf("induced conflict")),
				}),
			},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
					}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Image: "ubuntu:bionic",
						Params: []cartov1alpha1.Param{
							{
								Name:  "debug",
								Value: apiextensionsv1.JSON{Raw: []byte(`"true"`)},
							},
						},
					},
				},
			},
			ShouldError: true,
			ExpectOutput: `
Update workload:
...
  5,  5   |  name: my-workload
  6,  6   |  namespace: default
  7,  7   |spec:
  8,  8   |  image: ubuntu:bionic
      9 + |  params:
     10 + |  - name: debug
     11 + |    value: "true"

Error: conflict updating workload, the object was modified by another user; please run the update command again
`,
		},
		{
			Name: "update - wait error with timeout",
			Args: []string{workloadName, flags.ServiceRefFlagName, "database=services.tanzu.vmware.com/v1alpha1:PostgreSQL:my-prod-db", flags.WaitFlagName, flags.YesFlagName, flags.WaitTimeoutFlagName, "1ns"},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
					}),
			},
			Prepare: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) (context.Context, error) {
				workload := &cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Status: cartov1alpha1.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:   cartov1alpha1.WorkloadConditionReady,
								Status: metav1.ConditionTrue,
							},
						},
					},
				}
				fakeWatcher := watchfakes.NewFakeWithWatch(false, config.Client, []watch.Event{
					{Type: watch.Modified, Object: workload},
				})
				ctx = watchhelper.WithWatcher(ctx, fakeWatcher)
				return ctx, nil
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Image: "ubuntu:bionic",
						ServiceClaims: []cartov1alpha1.WorkloadServiceClaim{
							{
								Name: "database",
								Ref: &cartov1alpha1.WorkloadServiceClaimReference{
									APIVersion: "services.tanzu.vmware.com/v1alpha1",
									Kind:       "PostgreSQL",
									Name:       "my-prod-db",
								},
							},
						},
					},
				},
			},
			ShouldError: true,
			ExpectOutput: `
Update workload:
...
  5,  5   |  name: my-workload
  6,  6   |  namespace: default
  7,  7   |spec:
  8,  8   |  image: ubuntu:bionic
      9 + |  serviceClaims:
     10 + |  - name: database
     11 + |    ref:
     12 + |      apiVersion: services.tanzu.vmware.com/v1alpha1
     13 + |      kind: PostgreSQL
     14 + |      name: my-prod-db

Updated workload "my-workload"
Waiting for workload "my-workload" to become ready...
Error: timeout after 1ns waiting for "my-workload" to become ready
To view status run: tanzu apps workload get my-workload --namespace default
`,
		},
		{
			Name: "update - wait error for false condition",
			Args: []string{workloadName, flags.ServiceRefFlagName, "database=services.tanzu.vmware.com/v1alpha1:PostgreSQL:my-prod-db", flags.WaitFlagName, flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
					}),
			},

			Prepare: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) (context.Context, error) {
				workload := &cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Status: cartov1alpha1.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:    cartov1alpha1.WorkloadConditionReady,
								Status:  metav1.ConditionFalse,
								Reason:  "OopsieDoodle",
								Message: "a hopefully informative message about what went wrong",
							},
						},
					},
				}
				fakeWatcher := watchfakes.NewFakeWithWatch(false, config.Client, []watch.Event{
					{Type: watch.Modified, Object: workload},
				})
				ctx = watchhelper.WithWatcher(ctx, fakeWatcher)
				return ctx, nil
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Image: "ubuntu:bionic",
						ServiceClaims: []cartov1alpha1.WorkloadServiceClaim{
							{
								Name: "database",
								Ref: &cartov1alpha1.WorkloadServiceClaimReference{
									APIVersion: "services.tanzu.vmware.com/v1alpha1",
									Kind:       "PostgreSQL",
									Name:       "my-prod-db",
								},
							},
						},
					},
				},
			},
			ShouldError: true,
			ExpectOutput: `
Update workload:
...
  5,  5   |  name: my-workload
  6,  6   |  namespace: default
  7,  7   |spec:
  8,  8   |  image: ubuntu:bionic
      9 + |  serviceClaims:
     10 + |  - name: database
     11 + |    ref:
     12 + |      apiVersion: services.tanzu.vmware.com/v1alpha1
     13 + |      kind: PostgreSQL
     14 + |      name: my-prod-db

Updated workload "my-workload"
Waiting for workload "my-workload" to become ready...
Error: Failed to become ready: a hopefully informative message about what went wrong
`,
		},
		{
			Name: "update - successful wait for ready condition",
			Args: []string{workloadName, flags.ServiceRefFlagName, "database=services.tanzu.vmware.com/v1alpha1:PostgreSQL:my-prod-db", flags.WaitFlagName, flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
					}),
			},
			Prepare: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) (context.Context, error) {
				workload := &cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Status: cartov1alpha1.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:   cartov1alpha1.WorkloadConditionReady,
								Status: metav1.ConditionTrue,
							},
						},
					},
				}
				fakeWatcher := watchfakes.NewFakeWithWatch(false, config.Client, []watch.Event{
					{Type: watch.Modified, Object: workload},
				})
				ctx = watchhelper.WithWatcher(ctx, fakeWatcher)
				return ctx, nil
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Image: "ubuntu:bionic",
						ServiceClaims: []cartov1alpha1.WorkloadServiceClaim{
							{
								Name: "database",
								Ref: &cartov1alpha1.WorkloadServiceClaimReference{
									APIVersion: "services.tanzu.vmware.com/v1alpha1",
									Kind:       "PostgreSQL",
									Name:       "my-prod-db",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  5,  5   |  name: my-workload
  6,  6   |  namespace: default
  7,  7   |spec:
  8,  8   |  image: ubuntu:bionic
      9 + |  serviceClaims:
     10 + |  - name: database
     11 + |    ref:
     12 + |      apiVersion: services.tanzu.vmware.com/v1alpha1
     13 + |      kind: PostgreSQL
     14 + |      name: my-prod-db

Updated workload "my-workload"
Waiting for workload "my-workload" to become ready...
Workload "my-workload" is ready
`,
		},
		{
			Name: "update - tail with timestamp while waiting for ready condition",
			Args: []string{workloadName, flags.ServiceRefFlagName, "database=services.tanzu.vmware.com/v1alpha1:PostgreSQL:my-prod-db", flags.YesFlagName, flags.TailTimestampFlagName},
			Prepare: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) (context.Context, error) {
				workload := &cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Status: cartov1alpha1.WorkloadStatus{
						Conditions: []metav1.Condition{
							{
								Type:   cartov1alpha1.WorkloadConditionReady,
								Status: metav1.ConditionTrue,
							},
						},
					},
				}
				fakeWatcher := watchfakes.NewFakeWithWatch(false, config.Client, []watch.Event{
					{Type: watch.Modified, Object: workload},
				})
				ctx = watchhelper.WithWatcher(ctx, fakeWatcher)

				tailer := &logs.FakeTailer{}
				selector, _ := labels.Parse(fmt.Sprintf("%s=%s", cartov1alpha1.WorkloadLabelName, workloadName))
				tailer.On("Tail", mock.Anything, "default", selector, []string{}, time.Second, true).Return(nil).Once()
				ctx = logs.StashTailer(ctx, tailer)

				return ctx, nil
			},
			CleanUp: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) error {
				tailer := logs.RetrieveTailer(ctx).(*logs.FakeTailer)
				tailer.AssertExpectations(t)
				return nil
			},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
					}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Image: "ubuntu:bionic",
						ServiceClaims: []cartov1alpha1.WorkloadServiceClaim{
							{
								Name: "database",
								Ref: &cartov1alpha1.WorkloadServiceClaimReference{
									APIVersion: "services.tanzu.vmware.com/v1alpha1",
									Kind:       "PostgreSQL",
									Name:       "my-prod-db",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  5,  5   |  name: my-workload
  6,  6   |  namespace: default
  7,  7   |spec:
  8,  8   |  image: ubuntu:bionic
      9 + |  serviceClaims:
     10 + |  - name: database
     11 + |    ref:
     12 + |      apiVersion: services.tanzu.vmware.com/v1alpha1
     13 + |      kind: PostgreSQL
     14 + |      name: my-prod-db

Updated workload "my-workload"
Waiting for workload "my-workload" to become ready...
...tail output...
Workload "my-workload" is ready
`,
		},
		{
			Name: "update - filepath",
			Args: []string{flags.FilePathFlagName, "testdata/workload.yaml", flags.SubPathFlagName, "./cmd", flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					MetadataDie(func(d *diemetav1.ObjectMetaDie) {
						d.Name("spring-petclinic")
						d.AddLabel("preserve-me", "should-exist")
					}).
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
						d.Env(
							corev1.EnvVar{
								Name:  "OVERRIDE_VAR",
								Value: "doesnt matter",
							},
						)
					}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							"preserve-me":                         "should-exist",
							"app.kubernetes.io/part-of":           "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/spring-projects/spring-petclinic.git",
								Ref: cartov1alpha1.GitRef{
									Branch: "main",
								},
							},
							Subpath: "./cmd",
						},
						Env: []corev1.EnvVar{
							{
								Name:  "OVERRIDE_VAR",
								Value: "doesnt matter",
							},
							{
								Name:  "SPRING_PROFILES_ACTIVE",
								Value: "mysql",
							},
						},
						Resources: &corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  2,  2   |apiVersion: carto.run/v1alpha1
  3,  3   |kind: Workload
  4,  4   |metadata:
  5,  5   |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
  6,  8   |    preserve-me: should-exist
  7,  9   |  name: spring-petclinic
  8, 10   |  namespace: default
  9, 11   |spec:
 10, 12   |  env:
 11, 13   |  - name: OVERRIDE_VAR
 12, 14   |    value: doesnt matter
 13     - |  image: ubuntu:bionic
     15 + |  - name: SPRING_PROFILES_ACTIVE
     16 + |    value: mysql
     17 + |  resources:
     18 + |    limits:
     19 + |      cpu: 500m
     20 + |      memory: 1Gi
     21 + |    requests:
     22 + |      cpu: 100m
     23 + |      memory: 1Gi
     24 + |  source:
     25 + |    git:
     26 + |      ref:
     27 + |        branch: main
     28 + |      url: https://github.com/spring-projects/spring-petclinic.git
     29 + |    subPath: ./cmd

Updated workload "spring-petclinic"
`,
		},
		{
			Name: "update - filepath - custom namespace and name",
			Args: []string{workloadName, flags.NamespaceFlagName, "test-namespace", flags.FilePathFlagName, "testdata/workload.yaml", flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					MetadataDie(func(d *diemetav1.ObjectMetaDie) {
						d.Namespace("test-namespace")
						d.AddLabel("preserve-me", "should-exist")
					}).
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
						d.Env(
							corev1.EnvVar{
								Name:  "OVERRIDE_VAR",
								Value: "doesnt matter",
							},
						)
					}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "test-namespace",
						Name:      workloadName,
						Labels: map[string]string{
							"preserve-me":                         "should-exist",
							"app.kubernetes.io/part-of":           "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/spring-projects/spring-petclinic.git",
								Ref: cartov1alpha1.GitRef{
									Branch: "main",
								},
							},
						},
						Env: []corev1.EnvVar{
							{
								Name:  "OVERRIDE_VAR",
								Value: "doesnt matter",
							},
							{
								Name:  "SPRING_PROFILES_ACTIVE",
								Value: "mysql",
							},
						},
						Resources: &corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  2,  2   |apiVersion: carto.run/v1alpha1
  3,  3   |kind: Workload
  4,  4   |metadata:
  5,  5   |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
  6,  8   |    preserve-me: should-exist
  7,  9   |  name: my-workload
  8, 10   |  namespace: test-namespace
  9, 11   |spec:
 10, 12   |  env:
 11, 13   |  - name: OVERRIDE_VAR
 12, 14   |    value: doesnt matter
 13     - |  image: ubuntu:bionic
     15 + |  - name: SPRING_PROFILES_ACTIVE
     16 + |    value: mysql
     17 + |  resources:
     18 + |    limits:
     19 + |      cpu: 500m
     20 + |      memory: 1Gi
     21 + |    requests:
     22 + |      cpu: 100m
     23 + |      memory: 1Gi
     24 + |  source:
     25 + |    git:
     26 + |      ref:
     27 + |        branch: main
     28 + |      url: https://github.com/spring-projects/spring-petclinic.git

Updated workload "my-workload"
`,
		},
		{
			Name:        "local path - missing fields",
			Args:        []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.LocalPathFlagName, "testdata/local-source", flags.YesFlagName},
			ShouldError: true,
			CleanUp: func(t *testing.T, ctx context.Context, config *cli.Config, tc *clitesting.CommandTestCase) error {
				if expected, actual := false, cmd.SilenceUsage; expected != actual {
					t.Errorf("expected cmd.SilenceUsage to be %t, actually %t", expected, actual)
				}

				return nil
			},
		},
		{
			Name:        "filepath invalid name",
			Args:        []string{flags.FilePathFlagName, "testdata/workload-invalid-name.yaml", flags.YesFlagName},
			ShouldError: true,
		},
		{
			Name: "update - serviceclaim with deprecation warning",
			Args: []string{workloadName, flags.ServiceRefFlagName, "database=services.tanzu.vmware.com/v1alpha1:PostgreSQL:my-prod-ns:my-prod-db", flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
						d.Image("ubuntu:bionic")
					}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Annotations: map[string]string{
							apis.ServiceClaimAnnotationName: `{"kind":"ServiceClaimsExtension","apiVersion":"supplychain.apps.x-tanzu.vmware.com/v1alpha1","spec":{"serviceClaims":{"database":{"namespace":"my-prod-ns"}}}}`,
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Image: "ubuntu:bionic",
						ServiceClaims: []cartov1alpha1.WorkloadServiceClaim{
							{
								Name: "database",
								Ref: &cartov1alpha1.WorkloadServiceClaimReference{
									APIVersion: "services.tanzu.vmware.com/v1alpha1",
									Kind:       "PostgreSQL",
									Name:       "my-prod-db",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
WARNING: Cross namespace service claims are deprecated. Please use ` + "`tanzu service claim create`" + ` instead.
Update workload:
  1,  1   |---
  2,  2   |apiVersion: carto.run/v1alpha1
  3,  3   |kind: Workload
  4,  4   |metadata:
      5 + |  annotations:
      6 + |    serviceclaims.supplychain.apps.x-tanzu.vmware.com/extensions: '{"kind":"ServiceClaimsExtension","apiVersion":"supplychain.apps.x-tanzu.vmware.com/v1alpha1","spec":{"serviceClaims":{"database":{"namespace":"my-prod-ns"}}}}'
  5,  7   |  name: my-workload
  6,  8   |  namespace: default
  7,  9   |spec:
  8, 10   |  image: ubuntu:bionic
     11 + |  serviceClaims:
     12 + |  - name: database
     13 + |    ref:
     14 + |      apiVersion: services.tanzu.vmware.com/v1alpha1
     15 + |      kind: PostgreSQL
     16 + |      name: my-prod-db

Updated workload "my-workload"
`,
		},
		{
			Name: "create - serviceclaim with deprecation warning",
			Args: []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.ServiceRefFlagName, "database=services.tanzu.vmware.com/v1alpha1:PostgreSQL:my-prod-ns:my-prod-db", flags.YesFlagName},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Annotations: map[string]string{
							apis.ServiceClaimAnnotationName: `{"kind":"ServiceClaimsExtension","apiVersion":"supplychain.apps.x-tanzu.vmware.com/v1alpha1","spec":{"serviceClaims":{"database":{"namespace":"my-prod-ns"}}}}`,
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{URL: "https://example.com/repo.git", Ref: cartov1alpha1.GitRef{Branch: "main"}},
						},
						ServiceClaims: []cartov1alpha1.WorkloadServiceClaim{
							{
								Name: "database",
								Ref: &cartov1alpha1.WorkloadServiceClaimReference{
									APIVersion: "services.tanzu.vmware.com/v1alpha1",
									Kind:       "PostgreSQL",
									Name:       "my-prod-db",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
WARNING: Cross namespace service claims are deprecated. Please use ` + "`tanzu service claim create`" + ` instead.
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  annotations:
      6 + |    serviceclaims.supplychain.apps.x-tanzu.vmware.com/extensions: '{"kind":"ServiceClaimsExtension","apiVersion":"supplychain.apps.x-tanzu.vmware.com/v1alpha1","spec":{"serviceClaims":{"database":{"namespace":"my-prod-ns"}}}}'
      7 + |  name: my-workload
      8 + |  namespace: default
      9 + |spec:
     10 + |  serviceClaims:
     11 + |  - name: database
     12 + |    ref:
     13 + |      apiVersion: services.tanzu.vmware.com/v1alpha1
     14 + |      kind: PostgreSQL
     15 + |      name: my-prod-db
     16 + |  source:
     17 + |    git:
     18 + |      ref:
     19 + |        branch: main
     20 + |      url: https://example.com/repo.git

Created workload "my-workload"
`,
		},
		{
			Name: "update serviceAccountName via file",
			Args: []string{flags.FilePathFlagName, "testdata/service-account-name.yaml", flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					MetadataDie(func(d *diemetav1.ObjectMetaDie) {
						d.Name("spring-petclinic")
						d.AddLabel("preserve-me", "should-exist")
					}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							"preserve-me":                         "should-exist",
							"app.kubernetes.io/part-of":           "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						ServiceAccountName: &serviceAccountName,
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/sample-accelerators/spring-petclinic",
								Ref: cartov1alpha1.GitRef{
									Tag: "tap-1.1",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  2,  2   |apiVersion: carto.run/v1alpha1
  3,  3   |kind: Workload
  4,  4   |metadata:
  5,  5   |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
  6,  8   |    preserve-me: should-exist
  7,  9   |  name: spring-petclinic
  8, 10   |  namespace: default
  9     - |spec: {}
     11 + |spec:
     12 + |  serviceAccountName: my-service-account
     13 + |  source:
     14 + |    git:
     15 + |      ref:
     16 + |        tag: tap-1.1
     17 + |      url: https://github.com/sample-accelerators/spring-petclinic

Updated workload "spring-petclinic"
`,
		},
		{
			Name: "delete serviceAccountName by setting to empty in file",
			Args: []string{flags.FilePathFlagName, "testdata/no-service-account-name.yaml", flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					MetadataDie(func(d *diemetav1.ObjectMetaDie) {
						d.Name("spring-petclinic")
						d.AddLabel("preserve-me", "should-exist")
					}).SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
					d.ServiceAccountName(&serviceAccountName)
				}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							"preserve-me":                         "should-exist",
							"app.kubernetes.io/part-of":           "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						ServiceAccountName: nil,
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/sample-accelerators/spring-petclinic",
								Ref: cartov1alpha1.GitRef{
									Tag: "tap-1.1",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  2,  2   |apiVersion: carto.run/v1alpha1
  3,  3   |kind: Workload
  4,  4   |metadata:
  5,  5   |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
  6,  8   |    preserve-me: should-exist
  7,  9   |  name: spring-petclinic
  8, 10   |  namespace: default
  9, 11   |spec:
 10     - |  serviceAccountName: my-service-account
     12 + |  source:
     13 + |    git:
     14 + |      ref:
     15 + |        tag: tap-1.1
     16 + |      url: https://github.com/sample-accelerators/spring-petclinic

Updated workload "spring-petclinic"
`,
		},
		{
			Name: "updated serviceAccountName taking priority from flag",
			Args: []string{flags.FilePathFlagName, "testdata/no-service-account-name.yaml", flags.ServiceAccountFlagName, serviceAccountNameUpdated, flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					MetadataDie(func(d *diemetav1.ObjectMetaDie) {
						d.Name("spring-petclinic")
						d.AddLabel("preserve-me", "should-exist")
					}).SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
					d.ServiceAccountName(&serviceAccountName)
				}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							"preserve-me":                         "should-exist",
							"app.kubernetes.io/part-of":           "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						ServiceAccountName: &serviceAccountNameUpdated,
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/sample-accelerators/spring-petclinic",
								Ref: cartov1alpha1.GitRef{
									Tag: "tap-1.1",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  2,  2   |apiVersion: carto.run/v1alpha1
  3,  3   |kind: Workload
  4,  4   |metadata:
  5,  5   |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
  6,  8   |    preserve-me: should-exist
  7,  9   |  name: spring-petclinic
  8, 10   |  namespace: default
  9, 11   |spec:
 10     - |  serviceAccountName: my-service-account
     12 + |  serviceAccountName: my-service-account-updated
     13 + |  source:
     14 + |    git:
     15 + |      ref:
     16 + |        tag: tap-1.1
     17 + |      url: https://github.com/sample-accelerators/spring-petclinic

Updated workload "spring-petclinic"
`,
		},
		{
			Name: "delete serviceAccountName field",
			Args: []string{flags.FilePathFlagName, "testdata/no-service-account-name.yaml", flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					MetadataDie(func(d *diemetav1.ObjectMetaDie) {
						d.Name("spring-petclinic")
						d.AddLabel("preserve-me", "should-exist")
					}).SpecDie(func(d *diecartov1alpha1.WorkloadSpecDie) {
					d.ServiceAccountName(&serviceAccountName)
				}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							"preserve-me":                         "should-exist",
							"app.kubernetes.io/part-of":           "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/sample-accelerators/spring-petclinic",
								Ref: cartov1alpha1.GitRef{
									Tag: "tap-1.1",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  2,  2   |apiVersion: carto.run/v1alpha1
  3,  3   |kind: Workload
  4,  4   |metadata:
  5,  5   |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
  6,  8   |    preserve-me: should-exist
  7,  9   |  name: spring-petclinic
  8, 10   |  namespace: default
  9, 11   |spec:
 10     - |  serviceAccountName: my-service-account
     12 + |  source:
     13 + |    git:
     14 + |      ref:
     15 + |        tag: tap-1.1
     16 + |      url: https://github.com/sample-accelerators/spring-petclinic

Updated workload "spring-petclinic"
`,
		},
		{
			Name: "do not delete serviceAccountName when updating another field",
			Args: []string{workloadName, flags.GitTagFlagName, "tap-1.1", flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					SpecDie(
						func(d *diecartov1alpha1.WorkloadSpecDie) {
							d.ServiceAccountName(&serviceAccountName)
							d.Source(&cartov1alpha1.Source{
								Git: &cartov1alpha1.GitSource{
									URL: "https://github.com/sample-accelerators/spring-petclinic",
									Ref: cartov1alpha1.GitRef{
										Branch: "main",
									},
								},
							})
						}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						ServiceAccountName: &serviceAccountName,
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/sample-accelerators/spring-petclinic",
								Ref: cartov1alpha1.GitRef{
									Branch: "main",
									Tag:    "tap-1.1",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  9,  9   |  source:
 10, 10   |    git:
 11, 11   |      ref:
 12, 12   |        branch: main
     13 + |        tag: tap-1.1
 13, 14   |      url: https://github.com/sample-accelerators/spring-petclinic

Updated workload "my-workload"
`,
		},
		{
			Name: "update serviceAccountName field via flag",
			Args: []string{workloadName, flags.ServiceAccountFlagName, serviceAccountNameUpdated, flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					SpecDie(
						func(d *diecartov1alpha1.WorkloadSpecDie) {
							d.ServiceAccountName(&serviceAccountName)
							d.Source(&cartov1alpha1.Source{
								Git: &cartov1alpha1.GitSource{
									URL: "https://github.com/sample-accelerators/spring-petclinic",
									Ref: cartov1alpha1.GitRef{
										Branch: "main",
									},
								},
							})
						}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						ServiceAccountName: &serviceAccountNameUpdated,
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/sample-accelerators/spring-petclinic",
								Ref: cartov1alpha1.GitRef{
									Branch: "main",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  4,  4   |metadata:
  5,  5   |  name: my-workload
  6,  6   |  namespace: default
  7,  7   |spec:
  8     - |  serviceAccountName: my-service-account
      8 + |  serviceAccountName: my-service-account-updated
  9,  9   |  source:
 10, 10   |    git:
 11, 11   |      ref:
 12, 12   |        branch: main
...

Updated workload "my-workload"
`,
		},
		{
			Name: "delete serviceAccountName via flag",
			Args: []string{workloadName, flags.ServiceAccountFlagName, "", flags.YesFlagName},
			GivenObjects: []client.Object{
				parent.
					SpecDie(
						func(d *diecartov1alpha1.WorkloadSpecDie) {
							d.ServiceAccountName(&serviceAccountName)
							d.Source(&cartov1alpha1.Source{
								Git: &cartov1alpha1.GitSource{
									URL: "https://github.com/sample-accelerators/spring-petclinic",
									Ref: cartov1alpha1.GitRef{
										Branch: "main",
									},
								},
							})
						}),
			},
			ExpectUpdates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						ServiceAccountName: nil,
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/sample-accelerators/spring-petclinic",
								Ref: cartov1alpha1.GitRef{
									Branch: "main",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Update workload:
...
  4,  4   |metadata:
  5,  5   |  name: my-workload
  6,  6   |  namespace: default
  7,  7   |spec:
  8     - |  serviceAccountName: my-service-account
  9,  8   |  source:
 10,  9   |    git:
 11, 10   |      ref:
 12, 11   |        branch: main
...

Updated workload "my-workload"
`,
		},
		{
			Name: "create with serviceAccountName",
			Args: []string{flags.FilePathFlagName, "testdata/service-account-name.yaml", flags.YesFlagName},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							apis.AppPartOfLabelName:               "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						ServiceAccountName: &serviceAccountName,
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/sample-accelerators/spring-petclinic",
								Ref: cartov1alpha1.GitRef{
									Tag: "tap-1.1",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
      8 + |  name: spring-petclinic
      9 + |  namespace: default
     10 + |spec:
     11 + |  serviceAccountName: my-service-account
     12 + |  source:
     13 + |    git:
     14 + |      ref:
     15 + |        tag: tap-1.1
     16 + |      url: https://github.com/sample-accelerators/spring-petclinic

Created workload "spring-petclinic"
`,
		},
		{
			Name: "create with serviceAccountName via flag",
			Args: []string{workloadName, flags.GitRepoFlagName, gitRepo, flags.GitBranchFlagName, gitBranch, flags.ServiceAccountFlagName, serviceAccountName, flags.YesFlagName},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      workloadName,
						Labels:    map[string]string{},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						ServiceAccountName: &serviceAccountName,
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: gitRepo,
								Ref: cartov1alpha1.GitRef{
									Branch: gitBranch,
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  name: my-workload
      6 + |  namespace: default
      7 + |spec:
      8 + |  serviceAccountName: my-service-account
      9 + |  source:
     10 + |    git:
     11 + |      ref:
     12 + |        branch: main
     13 + |      url: https://example.com/repo.git

Created workload "my-workload"
`,
		},
		{
			Name: "create with serviceAccountName from file and flag",
			Args: []string{flags.FilePathFlagName, "testdata/service-account-name.yaml", flags.ServiceAccountFlagName, serviceAccountNameUpdated, flags.YesFlagName},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							apis.AppPartOfLabelName:               "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						ServiceAccountName: &serviceAccountNameUpdated,
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/sample-accelerators/spring-petclinic",
								Ref: cartov1alpha1.GitRef{
									Tag: "tap-1.1",
								},
							},
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
      8 + |  name: spring-petclinic
      9 + |  namespace: default
     10 + |spec:
     11 + |  serviceAccountName: my-service-account-updated
     12 + |  source:
     13 + |    git:
     14 + |      ref:
     15 + |        tag: tap-1.1
     16 + |      url: https://github.com/sample-accelerators/spring-petclinic

Created workload "spring-petclinic"
`,
		},
		{
			Name: "create with multiple param-yaml using valid json and yaml",
			Args: []string{flags.FilePathFlagName, "testdata/param-yaml.yaml",
				flags.ParamYamlFlagName, `ports_json={"name": "smtp", "port": 1026}`,
				flags.ParamYamlFlagName, "ports_nesting_yaml=- deployment:\n    name: smtp\n    port: 1026",
				flags.YesFlagName},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							apis.AppPartOfLabelName:               "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Image: "my-reponame/my-image:my-tag",
						Params: []cartov1alpha1.Param{
							{
								Name:  "ports_json",
								Value: apiextensionsv1.JSON{Raw: []byte(`{"name":"smtp","port":1026}`)},
							}, {
								Name:  "ports_nesting_yaml",
								Value: apiextensionsv1.JSON{Raw: []byte(`[{"deployment":{"name":"smtp","port":1026}}]`)},
							},
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
      8 + |  name: spring-petclinic
      9 + |  namespace: default
     10 + |spec:
     11 + |  image: my-reponame/my-image:my-tag
     12 + |  params:
     13 + |  - name: ports_json
     14 + |    value:
     15 + |      name: smtp
     16 + |      port: 1026
     17 + |  - name: ports_nesting_yaml
     18 + |    value:
     19 + |    - deployment:
     20 + |        name: smtp
     21 + |        port: 1026

Created workload "spring-petclinic"
`,
		},
		{
			ShouldError: true,
			Name:        "failse create with multiple param-yaml using invalid json",
			Args: []string{flags.FilePathFlagName, "testdata/param-yaml.yaml",
				flags.ParamYamlFlagName, `ports_json={"name": "smtp", "port": 1026`,
				flags.ParamYamlFlagName, `ports_nesting_yaml=- deployment:\n    name: smtp\n    port: 1026`,
				flags.YesFlagName},
			ExpectOutput: "",
		},
		{
			Name: "create with multiple param-yaml using valid json and yaml from file",
			Args: []string{flags.FilePathFlagName, "testdata/workload-param-yaml.yaml",
				flags.YesFlagName},
			ExpectCreates: []client.Object{
				&cartov1alpha1.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: defaultNamespace,
						Name:      "spring-petclinic",
						Labels: map[string]string{
							apis.AppPartOfLabelName:               "spring-petclinic",
							"apps.tanzu.vmware.com/workload-type": "web",
						},
					},
					Spec: cartov1alpha1.WorkloadSpec{
						Source: &cartov1alpha1.Source{
							Git: &cartov1alpha1.GitSource{
								URL: "https://github.com/spring-projects/spring-petclinic.git",
								Ref: cartov1alpha1.GitRef{
									Branch: "main",
								},
							},
						},
						Params: []cartov1alpha1.Param{
							{
								Name:  "ports",
								Value: apiextensionsv1.JSON{Raw: []byte(`{"ports":[{"name":"http","port":8080,"protocol":"TCP","targetPort":8080},{"name":"https","port":8443,"protocol":"TCP","targetPort":8443}]}`)},
							}, {
								Name:  "services",
								Value: apiextensionsv1.JSON{Raw: []byte(`[{"image":"mysql:5.7","name":"mysql"},{"image":"postgres:9.6","name":"postgres"}]`)},
							},
						},
					},
				},
			},
			ExpectOutput: `
Create workload:
      1 + |---
      2 + |apiVersion: carto.run/v1alpha1
      3 + |kind: Workload
      4 + |metadata:
      5 + |  labels:
      6 + |    app.kubernetes.io/part-of: spring-petclinic
      7 + |    apps.tanzu.vmware.com/workload-type: web
      8 + |  name: spring-petclinic
      9 + |  namespace: default
     10 + |spec:
     11 + |  params:
     12 + |  - name: ports
     13 + |    value:
     14 + |      ports:
     15 + |      - name: http
     16 + |        port: 8080
     17 + |        protocol: TCP
     18 + |        targetPort: 8080
     19 + |      - name: https
     20 + |        port: 8443
     21 + |        protocol: TCP
     22 + |        targetPort: 8443
     23 + |  - name: services
     24 + |    value:
     25 + |    - image: mysql:5.7
     26 + |      name: mysql
     27 + |    - image: postgres:9.6
     28 + |      name: postgres
     29 + |  source:
     30 + |    git:
     31 + |      ref:
     32 + |        branch: main
     33 + |      url: https://github.com/spring-projects/spring-petclinic.git

Created workload "spring-petclinic"
`,
		},
	}

	table.Run(t, scheme, func(ctx context.Context, c *cli.Config) *cobra.Command {
		// capture the cobra command so we can make assertions on cleanup, this will fail if tests are run parallel.
		cmd = commands.NewWorkloadApplyCommand(ctx, c)
		return cmd
	})
}
