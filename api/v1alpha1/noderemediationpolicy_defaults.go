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

import "time"

// Defaults mirror the +kubebuilder:default markers on the spec fields. The API server applies
// those markers; these constants back the getters below so a client that bypasses defaulting
// (or a zero-valued object built in code) still resolves the same values.
const (
	DefaultConditionStatus          = "True"
	DefaultDebounceEnter            = 60 * time.Second
	DefaultDebounceExit             = 30 * time.Second
	DefaultMaxAffectedPercent int32 = 100
)

// StatusOrDefault returns the condition status to match, defaulting when unset.
func (c ConditionMatch) StatusOrDefault() string {
	if c.Status != "" {
		return c.Status
	}
	return DefaultConditionStatus
}

// EnterOrDefault returns how long the condition must hold before remediation is applied.
func (d Debounce) EnterOrDefault() time.Duration {
	if d.Enter != nil {
		return d.Enter.Duration
	}
	return DefaultDebounceEnter
}

// ExitOrDefault returns how long the condition must be clear before remediation is removed.
func (d Debounce) ExitOrDefault() time.Duration {
	if d.Exit != nil {
		return d.Exit.Duration
	}
	return DefaultDebounceExit
}

// MaxAffectedPercentOrDefault returns the guard threshold, defaulting to 100 (guard disabled).
func (g Guard) MaxAffectedPercentOrDefault() int32 {
	if g.MaxAffectedPercent != nil {
		return *g.MaxAffectedPercent
	}
	return DefaultMaxAffectedPercent
}
