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

package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
)

func registerLifecycleTests() {
	registerOperatorDeploymentTests()
	registerCELValidationTests()
	registerComponentLifecycleTests()
	registerStatusConditionTests()
	registerCELImmutabilityTests()
	registerDriftRecoveryTests()
	registerManagementStateTests()
	registerOperandHealthTests()
}

func registerOperatorDeploymentTests() {
	Context("Operator deployment", Label("lifecycle"), func() {
		It("Should have the operator deployment running and ready", func() {
			Eventually(func(g Gomega) {
				deploy := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      "workbenches-operator",
					Namespace: operatorNS,
				}, deploy)).To(Succeed(),
					"operator deployment not found in namespace %q — "+
						"deploy the operator first or set OPERATOR_NAMESPACE", operatorNS)

				g.Expect(deploy.Status.ReadyReplicas).To(BeNumerically(">=", 1),
					"operator deployment in namespace %q has no ready replicas", operatorNS)
			}, timeout, interval).Should(Succeed())
		})
	})
}

func registerCELValidationTests() {
	Context("CEL validation", Label("validation"), func() {
		It("Should reject a Workbenches CR with a name other than 'default'", func() {
			wb := &componentsv1alpha1.Workbenches{
				ObjectMeta: metav1.ObjectMeta{
					Name: "not-default",
				},
				Spec: componentsv1alpha1.WorkbenchesSpec{
					ManagementState:    "Managed",
					WorkbenchNamespace: defaultTestWorkbenchNamespace,
					Platform:           "OpenDataHub",
				},
			}

			err := k8sClient.Create(ctx, wb)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsInvalid(err) || errors.IsForbidden(err)).To(BeTrue(),
				"expected validation error, got: %v", err)
		})
	})
}

func registerComponentLifecycleTests() {
	Context("Component lifecycle", Label("lifecycle"), func() {
		It("Should create a Workbenches CR and reach ProvisioningSucceeded", func() {
			wb := workbenchesCR()

			err := k8sClient.Create(ctx, wb)
			if errors.IsAlreadyExists(err) {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wb.Name}, wb)).To(Succeed())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}

			// Read back the actual workbench namespace from the CR (it may differ
			// from the default if the CR already existed on the cluster).
			workbenchNamespace = wb.Spec.WorkbenchNamespace

			waitForCondition("ProvisioningSucceeded", metav1.ConditionTrue)
		})

		It("Should create the configured workbench namespace with ownership label", func() {
			Expect(workbenchNamespace).NotTo(BeEmpty(), "workbenchNamespace must be set by a prior test")

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: workbenchNamespace}, ns)).To(Succeed())

			Expect(ns.Labels).To(HaveKeyWithValue("opendatahub.io/generated-namespace", "true"))
		})
	})
}

func registerStatusConditionTests() {
	Context("Status conditions completeness", Label("lifecycle"), func() {
		It("Should have all expected status conditions set", func() {
			Eventually(func() int {
				wb := getWorkbenches()

				return len(wb.Status.Conditions)
			}, timeout, interval).Should(BeNumerically(">=", 3))

			wb := getWorkbenches()

			provCond := meta.FindStatusCondition(wb.Status.Conditions, "ProvisioningSucceeded")
			Expect(provCond).NotTo(BeNil())
			Expect(provCond.Status).To(Equal(metav1.ConditionTrue))

			deployCond := meta.FindStatusCondition(wb.Status.Conditions, "DeploymentsAvailable")
			Expect(deployCond).NotTo(BeNil())

			readyCond := meta.FindStatusCondition(wb.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
		})

		It("Should populate status.workbenchNamespace", func() {
			wb := getWorkbenches()
			Expect(wb.Status.WorkbenchNamespace).To(Equal(workbenchNamespace))
		})

		It("Should set observedGeneration to match metadata.generation", func() {
			wb := getWorkbenches()
			Expect(wb.Status.ObservedGeneration).To(Equal(wb.Generation))
		})
	})
}

func registerCELImmutabilityTests() {
	Context("CEL immutability", Label("validation"), func() {
		It("Should reject updates to workbenchNamespace", func() {
			wb := getWorkbenches()

			wb.Spec.WorkbenchNamespace = "different-namespace"
			err := k8sClient.Update(ctx, wb)
			Expect(err).To(HaveOccurred(), "updating workbenchNamespace should be rejected")
		})
	})
}

func registerDriftRecoveryTests() {
	Context("Drift recovery", Label("lifecycle"), func() {
		It("Should recreate a deleted Deployment on next reconcile", func() {
			list := &appsv1.DeploymentList{}
			expectDriftRecovery("deployment", list,
				func() client.Object {
					if len(list.Items) == 0 {
						return nil
					}

					return list.Items[0].DeepCopy()
				},
				func() client.Object { return &appsv1.Deployment{} },
			)
		})

		It("Should recreate a deleted ConfigMap on next reconcile", func() {
			list := &corev1.ConfigMapList{}
			expectDriftRecovery("ConfigMap", list,
				func() client.Object {
					if len(list.Items) == 0 {
						return nil
					}

					return list.Items[0].DeepCopy()
				},
				func() client.Object { return &corev1.ConfigMap{} },
			)
		})

		It("Should recreate a deleted Service on next reconcile", func() {
			list := &corev1.ServiceList{}
			expectDriftRecovery("Service", list,
				func() client.Object {
					if len(list.Items) == 0 {
						return nil
					}

					return list.Items[0].DeepCopy()
				},
				func() client.Object { return &corev1.Service{} },
			)
		})
	})
}

func registerManagementStateTests() {
	Context("Managed to Removed transition", Label("lifecycle"), func() {
		It("Should transition to Failed phase when management state is Removed", func() {
			updateWorkbenchesSpec(func(wb *componentsv1alpha1.Workbenches) {
				wb.Spec.ManagementState = "Removed"
			})

			waitForPhase("Failed")
			waitForCondition("Ready", metav1.ConditionFalse)
			waitForCondition("ProvisioningSucceeded", metav1.ConditionFalse)
		})
	})

	Context("Removed to Managed round-trip", Label("lifecycle"), func() {
		It("Should recover to a healthy state when switching back to Managed", func() {
			updateWorkbenchesSpec(func(wb *componentsv1alpha1.Workbenches) {
				wb.Spec.ManagementState = "Managed"
			})

			waitForCondition("ProvisioningSucceeded", metav1.ConditionTrue)
			waitForPhase("Ready")
		})
	})
}

func registerOperandHealthTests() {
	Context("Operand health", Label("lifecycle"), func() {
		It("Should have the odh-notebook-controller-manager deployment ready", func() {
			Expect(workbenchNamespace).NotTo(BeEmpty(),
				"workbenchNamespace must be set by a prior test")

			Eventually(func(g Gomega) {
				deploy := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      "odh-notebook-controller-manager",
					Namespace: workbenchNamespace,
				}, deploy)).To(Succeed(),
					"odh-notebook-controller-manager deployment not found in namespace %q", workbenchNamespace)

				g.Expect(deploy.Status.ReadyReplicas).To(BeNumerically(">=", 1),
					"odh-notebook-controller-manager in %q has no ready replicas", workbenchNamespace)
			}, timeout, interval).Should(Succeed())
		})

		It("Should have the notebook-controller-deployment ready", func() {
			Expect(workbenchNamespace).NotTo(BeEmpty(),
				"workbenchNamespace must be set by a prior test")

			Eventually(func(g Gomega) {
				deploy := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      "notebook-controller-deployment",
					Namespace: workbenchNamespace,
				}, deploy)).To(Succeed(),
					"notebook-controller-deployment not found in namespace %q", workbenchNamespace)

				g.Expect(deploy.Status.ReadyReplicas).To(BeNumerically(">=", 1),
					"notebook-controller-deployment in %q has no ready replicas", workbenchNamespace)
			}, timeout, interval).Should(Succeed())
		})
	})
}
