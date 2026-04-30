//go:build e2e
// +build e2e

/*
Copyright 2025.

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
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/harmonycloud/opensaola/test/utils"
)

// namespace where the project is deployed in
const namespace = "opensaola-system"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		if err != nil {
			Expect(err.Error()).To(ContainSubstring("AlreadyExists"), "Failed to create namespace")
		}

		By("labeling the namespace to enforce the baseline security policy")
		// NOTE: This project is scaffolded for the 'restricted' Pod Security Standard, but several
		// upstream images used in this e2e suite (e.g. curlimages/curl) may not satisfy restricted
		// requirements consistently across versions. Baseline keeps the e2e suite stable while still
		// exercising the deployment in a policy-enforced namespace.
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=baseline")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with baseline policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy-e2e", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("undeploying the controller-manager")
		cmd := exec.Command("make", "undeploy-e2e")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			var cmd *exec.Cmd

			if controllerPodName != "" {
				By("Fetching controller manager pod logs")
				cmd = exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				controllerLogs, err := utils.Run(cmd)
				if err == nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
				} else {
					_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
				}
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			if controllerPodName != "" {
				By("Fetching controller manager pod description")
				cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
				podDescription, err := utils.Run(cmd)
				if err == nil {
					fmt.Println("Pod description:\n", podDescription)
				} else {
					fmt.Println("Failed to describe controller pod")
				}
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should report the controller deployment as available and ready", func() {
			By("waiting for the controller-manager deployment to become Available")
			cmd := exec.Command("kubectl", "wait", "deployment/opensaola-controller-manager",
				"--for=condition=Available", "--timeout=120s", "-n", namespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "controller-manager deployment should become Available")

			By("waiting for the controller-manager pod to become Ready")
			cmd = exec.Command("kubectl", "wait", "pod/"+controllerPodName,
				"--for=condition=Ready", "--timeout=120s", "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "controller-manager pod should become Ready")

			By("checking the controller-manager container readiness state")
			cmd = exec.Command("kubectl", "get", "pod", controllerPodName,
				"-o", "jsonpath={range .status.containerStatuses[*]}{.ready}{\"\\n\"}{end}",
				"-n", namespace)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(utils.GetNonEmptyLines(output)).To(ContainElement("true"))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// TODO: Customize the e2e test suite with scenarios specific to your project.
		// Consider applying sample/CR(s), checking their status, and verifying
		// their reconciliation results through Kubernetes resources and conditions.
	})

	Context("CRD Lifecycle", func() {
		It("should set Available state for valid MiddlewareOperatorBaseline", func() {
			mobName := "e2e-mob-valid"
			mobYAML := fmt.Sprintf(`
apiVersion: middleware.cn/v1
kind: MiddlewareOperatorBaseline
metadata:
  name: %s
spec:
  gvks:
    - name: v1
      group: apps
      version: v1
      kind: Deployment
`, mobName)

			By("creating a valid MOB")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(mobYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "mob", mobName, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

			By("waiting for Available state")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "mob", mobName,
					"-o", "jsonpath={.status.state}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(output)).To(Equal("Available"))
			}, 60*time.Second, 2*time.Second).Should(Succeed())

			By("verifying Checked condition is True")
			cmd = exec.Command("kubectl", "get", "mob", mobName,
				"-o", `jsonpath={.status.conditions[?(@.type=="Checked")].status}`)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(Equal("True"))
		})

		It("should set Checked=False for MOB with empty GVKs", func() {
			mobName := "e2e-mob-invalid"
			mobYAML := fmt.Sprintf(`
apiVersion: middleware.cn/v1
kind: MiddlewareOperatorBaseline
metadata:
  name: %s
spec:
  gvks: []
`, mobName)

			By("creating MOB with empty GVKs")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(mobYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "mob", mobName, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

			By("waiting for Checked condition to be False")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "mob", mobName,
					"-o", `jsonpath={.status.conditions[?(@.type=="Checked")].status}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(output)).To(Equal("False"))
			}, 60*time.Second, 2*time.Second).Should(Succeed())

			By("verifying error message mentions empty GVKs")
			cmd = exec.Command("kubectl", "get", "mob", mobName,
				"-o", `jsonpath={.status.conditions[?(@.type=="Checked")].message}`)
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("gvks"))
		})

		It("should add finalizer and clean up on Middleware deletion", func() {
			midName := "e2e-mid-delete"
			midYAML := fmt.Sprintf(`
apiVersion: middleware.cn/v1
kind: Middleware
metadata:
  name: %s
  namespace: %s
  labels:
    middleware.cn/component: TestDelete
spec:
  baseline: nonexistent-baseline
`, midName, namespace)

			By("creating Middleware with nonexistent baseline")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(midYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for finalizer to be added")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "mid", midName, "-n", namespace,
					"-o", `jsonpath={.metadata.finalizers}`)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(output)).To(ContainSubstring("middleware.cn/middleware-cleanup"))
			}, 60*time.Second, 2*time.Second).Should(Succeed())

			By("verifying Middleware enters error state due to nonexistent baseline")
			// Wait for controller to set a status (non-empty state)
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "mid", midName, "-n", namespace,
					"-o", "jsonpath={.status.state}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(output)).NotTo(BeEmpty())
			}, 60*time.Second, 2*time.Second).Should(Succeed())

			// Verify the state is Unavailable (not just any non-empty value)
			cmd = exec.Command("kubectl", "get", "mid", midName, "-n", namespace,
				"-o", "jsonpath={.status.state}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(Equal("Unavailable"))

			By("deleting the Middleware")
			cmd = exec.Command("kubectl", "delete", "mid", midName, "-n", namespace, "--timeout=60s")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying Middleware is fully deleted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "mid", midName, "-n", namespace)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred()) // should fail with NotFound
			}, 120*time.Second, 2*time.Second).Should(Succeed())
		})

		It("should converge observedGeneration after spec update", func() {
			mobName := "e2e-mob-converge"
			mobYAML := fmt.Sprintf(`
apiVersion: middleware.cn/v1
kind: MiddlewareOperatorBaseline
metadata:
  name: %s
spec:
  gvks:
    - name: v1
      group: apps
      version: v1
      kind: Deployment
`, mobName)

			By("creating initial MOB")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(mobYAML)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "mob", mobName, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

			By("waiting for initial Available")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "mob", mobName,
					"-o", "jsonpath={.status.state}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(string(output)).To(Equal("Available"))
			}, 60*time.Second, 2*time.Second).Should(Succeed())

			By("patching spec to trigger re-reconcile")
			patchJSON := `{"spec":{"gvks":[{"name":"v1","group":"apps","version":"v1","kind":"Deployment"},{"name":"v2","group":"batch","version":"v1","kind":"Job"}]}}`
			cmd = exec.Command("kubectl", "patch", "mob", mobName,
				"--type=merge", "-p", patchJSON)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for observedGeneration to catch up")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "mob", mobName,
					"-o", "jsonpath={.metadata.generation}:{.status.observedGeneration}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				parts := strings.Split(string(output), ":")
				g.Expect(parts).To(HaveLen(2))
				g.Expect(parts[0]).To(Equal(parts[1]))
			}, 60*time.Second, 2*time.Second).Should(Succeed())

			By("verifying state is still Available")
			cmd = exec.Command("kubectl", "get", "mob", mobName,
				"-o", "jsonpath={.status.state}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(Equal("Available"))
		})
	}) // end Context("CRD Lifecycle")
})
