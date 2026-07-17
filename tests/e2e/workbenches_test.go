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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
)

// Single Ordered Describe ensures deterministic execution order across all
// test areas. Each registerXxxTests function adds its Contexts to this
// container and can live in its own file.
var _ = Describe("Workbenches E2E", Ordered, func() {
	registerLifecycleTests()
	registerWebhookTests()

	AfterAll(func() {
		wb := &componentsv1alpha1.Workbenches{}

		err := k8sClient.Get(ctx, types.NamespacedName{
			Name: componentsv1alpha1.WorkbenchesInstanceName,
		}, wb)
		if errors.IsNotFound(err) {
			return
		}

		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(ctx, wb)).To(Succeed())

		Eventually(func(g Gomega) {
			getErr := k8sClient.Get(ctx, types.NamespacedName{
				Name: componentsv1alpha1.WorkbenchesInstanceName,
			}, &componentsv1alpha1.Workbenches{})
			g.Expect(errors.IsNotFound(getErr)).To(BeTrue(), "expected NotFound, got: %v", getErr)
		}, timeout, interval).Should(Succeed(), "Workbenches CR should be deleted")
	})
})
