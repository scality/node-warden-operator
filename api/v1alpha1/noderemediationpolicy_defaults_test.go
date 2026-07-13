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

package v1alpha1

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

var _ = Describe("NodeRemediationPolicy spec defaults", func() {
	It("defaults the condition status when unset, else returns the set value", func() {
		Expect(ConditionMatch{}.StatusOrDefault()).To(Equal(DefaultConditionStatus))
		Expect(ConditionMatch{Status: "False"}.StatusOrDefault()).To(Equal("False"))
	})

	It("defaults the debounce windows when unset, else returns the set values", func() {
		Expect(Debounce{}.EnterOrDefault()).To(Equal(DefaultDebounceEnter))
		Expect(Debounce{}.ExitOrDefault()).To(Equal(DefaultDebounceExit))

		set := Debounce{
			Enter: &metav1.Duration{Duration: 5 * time.Second},
			Exit:  &metav1.Duration{Duration: 2 * time.Second},
		}
		Expect(set.EnterOrDefault()).To(Equal(5 * time.Second))
		Expect(set.ExitOrDefault()).To(Equal(2 * time.Second))
	})

	It("defaults the guard to 100 (disabled) when unset, else returns the set value", func() {
		Expect(Guard{}.MaxAffectedPercentOrDefault()).To(Equal(DefaultMaxAffectedPercent))
		Expect(Guard{MaxAffectedPercent: ptr.To(int32(50))}.MaxAffectedPercentOrDefault()).To(Equal(int32(50)))
	})
})
