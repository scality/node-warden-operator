//go:build e2e
// +build e2e

/*
Copyright 2026 Scality.

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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/scality/node-warden-operator/test/utils"
)

// namespace where the project is deployed in
const namespace = "node-warden-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "node-warden-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "node-warden-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "node-warden-operator-metrics-binding"

// Fixtures for the remediation lifecycle specs: a policy that taints on a synthetic node condition.
const (
	e2ePolicyName    = "e2e-remediation"
	e2eConditionType = "WardenE2E"
	e2eTaintKey      = "warden.scality.com/e2e"
)

// Fixtures for the guard specs: a separate policy with a 50% guard, on its own condition/taint so
// it does not interfere with the lifecycle specs above.
const (
	e2eGuardPolicyName    = "e2e-guard"
	e2eGuardConditionType = "WardenGuardE2E"
	e2eGuardTaintKey      = "warden.scality.com/guard-e2e"
	e2eGuardMaxPercent    = 50
)

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string
	// workerNode is chosen by the first remediation spec and reused by the ones that follow.
	var workerNode string
	// guardNodes are the three workers the guard specs drive conditions onto.
	var guardNodes []string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("deleting the e2e policies while the operator is still up so their finalizer can run")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "noderemediationpolicy",
			e2ePolicyName, e2eGuardPolicyName, "--ignore-not-found", "--timeout=60s"))

		By("removing the synthetic conditions the specs added, restoring the nodes to their pre-test state")
		if workerNode != "" {
			_ = deleteNodeCondition(workerNode, e2eConditionType)
		}
		for _, n := range guardNodes {
			_ = deleteNodeCondition(n, e2eGuardConditionType)
		}

		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
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
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
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

				By("validating the pod's status")
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

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=node-warden-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		It("taints a node when its watched condition trips", func() {
			By("selecting a worker node to drive the condition onto")
			cmd := exec.Command("kubectl", "get", "nodes",
				"-l", "!node-role.kubernetes.io/control-plane",
				"-o", "jsonpath={.items[0].metadata.name}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to list worker nodes")
			workerNode = strings.TrimSpace(out)
			Expect(workerNode).NotTo(BeEmpty(), "expected a worker node in the Kind cluster")

			By("creating a NodeRemediationPolicy that taints on the condition")
			Expect(applyPolicy(e2ePolicyName, e2eConditionType, e2eTaintKey, 100)).
				To(Succeed(), "Failed to apply the NodeRemediationPolicy")

			By("setting the watched condition to True on the worker node")
			Expect(setNodeCondition(workerNode, e2eConditionType, "True")).To(Succeed(), "Failed to set the node condition")

			By("observing the remediation taint appear on the node")
			Eventually(func(g Gomega) {
				has, err := nodeHasTaint(workerNode, e2eTaintKey)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(has).To(BeTrue(), "node should carry the remediation taint")
			}).Should(Succeed())
		})

		It("removes the taint once the condition clears", func() {
			By("clearing the watched condition on the worker node")
			Expect(setNodeCondition(workerNode, e2eConditionType, "False")).To(Succeed(), "Failed to clear the node condition")

			By("observing the remediation taint be removed")
			Eventually(func(g Gomega) {
				has, err := nodeHasTaint(workerNode, e2eTaintKey)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(has).To(BeFalse(), "taint should be removed once the condition clears")
			}).Should(Succeed())
		})

		It("removes the taint via the finalizer when the policy is deleted", func() {
			By("re-tripping the condition and waiting for the taint to be re-applied")
			Expect(setNodeCondition(workerNode, e2eConditionType, "True")).To(Succeed(), "Failed to re-set the node condition")
			Eventually(func(g Gomega) {
				has, err := nodeHasTaint(workerNode, e2eTaintKey)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(has).To(BeTrue(), "node should be tainted again")
			}).Should(Succeed())

			By("deleting the policy while the node is still tainted")
			cmd := exec.Command("kubectl", "delete", "noderemediationpolicy", e2ePolicyName, "--wait=false")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete the NodeRemediationPolicy")

			By("observing the finalizer remove the taint and let the policy go")
			Eventually(func(g Gomega) {
				has, err := nodeHasTaint(workerNode, e2eTaintKey)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(has).To(BeFalse(), "finalizer must remove the taint on deletion")
				out, err := utils.Run(exec.Command("kubectl", "get", "noderemediationpolicy",
					e2ePolicyName, "--ignore-not-found", "-o", "name"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(BeEmpty(), "policy should be gone once the finalizer completes")
			}).Should(Succeed())
		})

		It("holds all remediation when the guard trips", func() {
			By("selecting three worker nodes")
			cmd := exec.Command("kubectl", "get", "nodes",
				"-l", "!node-role.kubernetes.io/control-plane",
				"-o", "jsonpath={.items[*].metadata.name}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to list worker nodes")
			guardNodes = strings.Fields(out)
			Expect(len(guardNodes)).To(BeNumerically(">=", 3), "expected at least three worker nodes")
			guardNodes = guardNodes[:3]

			By("creating a policy with a 50% guard")
			Expect(applyPolicy(e2eGuardPolicyName, e2eGuardConditionType, e2eGuardTaintKey, e2eGuardMaxPercent)).
				To(Succeed(), "Failed to apply the guarded policy")

			By("matching two of three workers (the third reports a determinate, non-matching condition)")
			Expect(setNodeCondition(guardNodes[0], e2eGuardConditionType, "True")).To(Succeed())
			Expect(setNodeCondition(guardNodes[1], e2eGuardConditionType, "True")).To(Succeed())
			Expect(setNodeCondition(guardNodes[2], e2eGuardConditionType, "False")).To(Succeed())

			By("observing the guard trip in the policy status (2 of 3 = 67% > 50%)")
			Eventually(func(g Gomega) {
				g.Expect(policyConditionReason(e2eGuardPolicyName)).To(Equal("GuardTripped"))
			}).Should(Succeed())

			By("confirming no worker is tainted while the guard holds")
			Consistently(func(g Gomega) {
				for _, n := range guardNodes {
					has, err := nodeHasTaint(n, e2eGuardTaintKey)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(has).To(BeFalse(), "guard must block every taint while tripped")
				}
			}, 10*time.Second, 2*time.Second).Should(Succeed())
		})

		It("remediates once the matches fall back within the guard budget", func() {
			By("clearing the condition on one of the two matching workers (1 of 3 = 33% <= 50%)")
			Expect(setNodeCondition(guardNodes[1], e2eGuardConditionType, "False")).To(Succeed())

			By("observing only the still-matching worker get tainted")
			Eventually(func(g Gomega) {
				has0, err := nodeHasTaint(guardNodes[0], e2eGuardTaintKey)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(has0).To(BeTrue(), "the single remaining match is within budget")
				has1, err := nodeHasTaint(guardNodes[1], e2eGuardTaintKey)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(has1).To(BeFalse())
				has2, err := nodeHasTaint(guardNodes[2], e2eGuardTaintKey)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(has2).To(BeFalse())
			}).Should(Succeed())

			By("cleaning up the guarded policy")
			_, err := utils.Run(exec.Command("kubectl", "delete", "noderemediationpolicy", e2eGuardPolicyName, "--wait=false"))
			Expect(err).NotTo(HaveOccurred(), "Failed to delete the guarded policy")
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}

// applyPolicy creates a NodeRemediationPolicy the specs drive. It uses a short debounce so the
// specs do not wait on the sample's 60s/30s windows, and no nodeSelector -- only nodes the specs
// set the condition on will match. maxAffectedPercent sets the guard (100 disables it).
func applyPolicy(name, conditionType, taintKey string, maxAffectedPercent int) error {
	manifest := fmt.Sprintf(`apiVersion: warden.scality.com/v1alpha1
kind: NodeRemediationPolicy
metadata:
  name: %s
spec:
  condition:
    type: %s
    status: "True"
  remediations:
    taint:
      key: %s
      effect: NoExecute
  debounce:
    enter: 1s
    exit: 1s
  guard:
    maxAffectedPercent: %d
`, name, conditionType, taintKey, maxAffectedPercent)
	path := filepath.Join(GinkgoT().TempDir(), "e2e-"+name+".yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		return err
	}
	_, err := utils.Run(exec.Command("kubectl", "apply", "-f", path))
	return err
}

// setNodeCondition upserts the given condition on the node's status subresource. lastTransitionTime
// is set in the past so the debounce window is already satisfied and the specs do not have to wait.
// The strategic merge patch keys on the condition type, so it adds or updates only this condition
// and leaves the node's real conditions (Ready, ...) untouched.
func setNodeCondition(node, conditionType, status string) error {
	transition := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(
		`{"status":{"conditions":[{"type":%q,"status":%q,"reason":"E2E","message":"e2e","lastHeartbeatTime":%q,"lastTransitionTime":%q}]}}`,
		conditionType, status, transition, transition)
	_, err := utils.Run(exec.Command("kubectl", "patch", "node", node,
		"--subresource=status", "--type=strategic", "-p", patch))
	return err
}

// deleteNodeCondition removes the given condition from the node's status, restoring it to the state
// before the spec added it. The strategic merge $patch:delete keys on the condition type, so it
// drops only this condition and leaves the node's real conditions (Ready, ...) untouched.
func deleteNodeCondition(node, conditionType string) error {
	patch := fmt.Sprintf(`{"status":{"conditions":[{"type":%q,"$patch":"delete"}]}}`, conditionType)
	_, err := utils.Run(exec.Command("kubectl", "patch", "node", node,
		"--subresource=status", "--type=strategic", "-p", patch))
	return err
}

// nodeHasTaint reports whether the node currently carries a taint with the given key. It returns
// the kubectl error too: callers pass the pair straight to g.Expect(...), so a transient failure
// surfaces (and Eventually retries) instead of being swallowed as "taint absent" and passing a
// BeFalse() check.
func nodeHasTaint(node, taintKey string) (bool, error) {
	out, err := utils.Run(exec.Command("kubectl", "get", "node", node,
		"-o", "jsonpath={.spec.taints[*].key}"))
	if err != nil {
		return false, err
	}
	for _, key := range strings.Fields(out) {
		if key == taintKey {
			return true, nil
		}
	}
	return false, nil
}

// policyConditionReason returns the reason of the policy's Remediating status condition.
func policyConditionReason(name string) string {
	out, err := utils.Run(exec.Command("kubectl", "get", "noderemediationpolicy", name,
		"-o", `jsonpath={.status.conditions[?(@.type=="Remediating")].reason}`))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
