package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Only request metadata is retained. No prompts, tokens or credentials are stored.
type OpenAIAutoGroupSample struct {
	ID           string `json:"id"`
	Cohort       string `json:"cohort,omitempty"`
	FirstTokenMS int    `json:"first_token_ms"`
	Success      bool   `json:"success"`
	Failed       bool   `json:"failed"`
	Overload     bool   `json:"overload"`
	StatusCode   int    `json:"status_code,omitempty"`
}

type OpenAIAutoGroupHistory struct {
	Version  string                             `json:"version"`
	Requests []OpenAIAutoGroupSample            `json:"requests"`
	Cohorts  map[string][]OpenAIAutoGroupSample `json:"cohorts"`
	Overflow bool                               `json:"overflow,omitempty"`
	Seen     []string                           `json:"seen"`
}

type OpenAIAutoGroupBinding struct {
	ID       int64 `json:"id"`
	Priority int   `json:"priority"`
}

type OpenAIAutoGroupDecision struct {
	State             OpenAIAutoGroupState
	Action            string
	ProtectionMinutes int
	SkipGroupIDs      []int64
}

func ReadOpenAIAutoGroupState(extra map[string]any) OpenAIAutoGroupState {
	var state OpenAIAutoGroupState
	if extra != nil {
		data, err := json.Marshal(extra[OpenAIAutoGroupStateExtraKey])
		if err == nil {
			_ = json.Unmarshal(data, &state)
		}
	}
	return state
}

// Updates run under the account's database row lock. Keeping a fixed number of
// requests (without an expiry) also preserves evidence for low-traffic accounts.
func UpdateOpenAIAutoGroupHistory(h *OpenAIAutoGroupHistory, sample OpenAIAutoGroupSample, p *OpenAIAutoGroupSettings, recovering bool) bool {
	seen := false
	for _, id := range h.Seen {
		if id == sample.ID {
			seen = true
			break
		}
	}
	if !seen {
		for _, old := range h.Requests {
			if old.ID == sample.ID {
				seen = true
				break
			}
		}
	}
	newFailure := sample.Failed || sample.Overload
	if seen {
		for i, old := range h.Requests {
			if old.ID != sample.ID {
				continue
			}
			newFailure = (sample.Failed && !old.Failed) || (sample.Overload && !old.Overload)
			if !newFailure && (!sample.Success || old.Success) {
				return false
			}
			sample.Failed = sample.Failed || old.Failed
			sample.Overload = sample.Overload || old.Overload
			if sample.StatusCode == 0 {
				sample.StatusCode = old.StatusCode
			}
			h.Requests[i] = sample
			break
		}
		// Replace the latency observation for this request; never count retries twice.
		for key, items := range h.Cohorts {
			filtered := items[:0]
			for _, item := range items {
				if item.ID != sample.ID {
					filtered = append(filtered, item)
				}
			}
			h.Cohorts[key] = filtered
		}
	} else {
		h.Seen = append(h.Seen, sample.ID)
		if len(h.Seen) > 4096 {
			h.Seen = h.Seen[len(h.Seen)-4096:]
		}
		h.Requests = append(h.Requests, sample)
		if len(h.Requests) > p.FailureWindow {
			h.Requests = h.Requests[len(h.Requests)-p.FailureWindow:]
		}
	}
	if h.Cohorts == nil || (recovering && newFailure) {
		h.Cohorts = make(map[string][]OpenAIAutoGroupSample)
	}
	// A successful retry still contributes its cumulative latency when high.
	// Recovery needs clean requests, so earlier failures continue to disqualify it.
	if !sample.Success || sample.Cohort == "" || sample.FirstTokenMS < 0 || (recovering && (sample.Failed || sample.Overload)) {
		return true
	}
	if _, exists := h.Cohorts[sample.Cohort]; !exists && len(h.Cohorts) >= 32 {
		h.Overflow = true
		return true
	}
	items := append(h.Cohorts[sample.Cohort], sample)
	limit := max(p.LatencyWindow, p.RecoverySamples)
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	h.Cohorts[sample.Cohort] = items
	return true
}

func openAIAutoGroupCohort(result *OpenAIForwardResult) string {
	if result == nil {
		return ""
	}
	model := strings.TrimSpace(result.UpstreamModel)
	if model == "" {
		model = strings.TrimSpace(result.Model)
	}
	if model == "" {
		return ""
	}
	effort, tier := "unknown", "unknown"
	if result.ReasoningEffort != nil && *result.ReasoningEffort != "" {
		effort = *result.ReasoningEffort
	}
	if result.ServiceTier != nil && *result.ServiceTier != "" {
		tier = *result.ServiceTier
	}
	if result.UpstreamResponseServiceTier != "" {
		tier = result.UpstreamResponseServiceTier
	}
	// OpenAI's raw input_tokens includes cached tokens (usage logs split them later).
	input := result.Usage.InputTokens
	size := "unknown"
	switch {
	case input > 128000:
		size = ">128k"
	case input > 32000:
		size = "32k-128k"
	case input > 0:
		size = "<=32k"
	}
	encoded, _ := json.Marshal([]string{model, effort, size, tier})
	return string(encoded)
}

func openAIAutoGroupSlow(items []OpenAIAutoGroupSample, p *OpenAIAutoGroupSettings) int {
	if len(items) > p.LatencyWindow {
		items = items[len(items)-p.LatencyWindow:]
	}
	count := 0
	for _, item := range items {
		if item.FirstTokenMS > p.FirstTokenSeconds*1000 {
			count++
		}
	}
	return count
}

func openAIAutoGroupRecovered(items []OpenAIAutoGroupSample, p *OpenAIAutoGroupSettings) bool {
	if len(items) < p.RecoverySamples {
		return false
	}
	items = items[len(items)-p.RecoverySamples:]
	fast := 0
	values := make([]int, len(items))
	for i, item := range items {
		values[i] = item.FirstTokenMS
		if item.FirstTokenMS < p.FirstTokenSeconds*1000 {
			fast++
		}
		if i >= len(items)-p.RecoveryFastStreak && item.FirstTokenMS >= p.FirstTokenSeconds*1000 {
			return false
		}
	}
	if fast*100 < len(items)*p.RecoveryFastPercent {
		return false
	}
	sort.Ints(values)
	middle := len(values) / 2
	median := float64(values[middle])
	if len(values)%2 == 0 {
		median = float64(values[middle-1]+values[middle]) / 2
	}
	return median <= float64(p.RecoveryMedianSeconds*1000) && openAIAutoGroupSlow(items, p) < p.SlowThreshold
}

func EvaluateOpenAIAutoGroup(state OpenAIAutoGroupState, h *OpenAIAutoGroupHistory, sample OpenAIAutoGroupSample, p *OpenAIAutoGroupSettings, healthy bool, now time.Time) *OpenAIAutoGroupDecision {
	// Decisions may be discarded during protection or observation. Never mutate
	// the caller's recovery slice through an append into shared backing storage.
	state.RecoveryCohorts = append([]string(nil), state.RecoveryCohorts...)
	d := &OpenAIAutoGroupDecision{State: state, ProtectionMinutes: p.ProtectionMinutes, SkipGroupIDs: p.SkipGroupIDs}
	s := &d.State
	s.At = now.UTC().Format(time.RFC3339)
	s.RequestCount, s.FailureCount, s.SlowCount, s.SampleCount, s.RecoveryCount = len(h.Requests), 0, 0, 0, 0
	s.StatusCode, s.WindowMinutes = sample.StatusCode, 0
	s.FailureWindow, s.LatencyWindow, s.RecoverySamples = p.FailureWindow, p.LatencyWindow, p.RecoverySamples
	s.Message = ""
	for _, item := range h.Requests {
		if item.Overload {
			s.FailureCount++
		}
	}
	s.Cohort = sample.Cohort
	items := h.Cohorts[sample.Cohort]
	s.SampleCount = min(len(items), p.LatencyWindow)
	s.SlowCount = openAIAutoGroupSlow(items, p)
	slow := make([]string, 0)
	for key, values := range h.Cohorts {
		if openAIAutoGroupSlow(values, p) >= p.SlowThreshold {
			slow = append(slow, key)
		}
	}
	sort.Strings(slow)
	low := len(s.RestoreBindings) > 0
	if !low {
		s.Status = "tracking"
		if p.Enabled && s.FailureCount >= int64(p.FailureThreshold) {
			d.Action, s.Reason = "down", "overload"
		} else if p.LatencyEnabled && len(slow) > 0 {
			d.Action, s.Reason = "down", "latency"
			s.Cohort = slow[0]
			s.SlowCount = openAIAutoGroupSlow(h.Cohorts[s.Cohort], p)
			s.SampleCount = min(len(h.Cohorts[s.Cohort]), p.LatencyWindow)
		}
		if d.Action == "down" {
			s.RecoveryCohorts = append([]string(nil), slow...)
			if s.Reason == "overload" {
				s.RecoveryCohorts = nil
				for key := range h.Cohorts {
					s.RecoveryCohorts = append(s.RecoveryCohorts, key)
				}
				sort.Strings(s.RecoveryCohorts)
			}
		}
	} else {
		s.Status = "downgraded"
		if len(s.RecoveryCohorts) == 0 && len(items) > 0 {
			s.RecoveryCohorts = []string{sample.Cohort}
		}
		for _, key := range slow {
			found := false
			for _, required := range s.RecoveryCohorts {
				found = found || required == key
			}
			if !found {
				s.RecoveryCohorts = append(s.RecoveryCohorts, key)
			}
		}
		ready := len(s.RecoveryCohorts) > 0 && !h.Overflow
		s.RecoveryCount = p.RecoverySamples
		for _, key := range s.RecoveryCohorts {
			s.RecoveryCount = min(s.RecoveryCount, len(h.Cohorts[key]))
			ready = ready && openAIAutoGroupRecovered(h.Cohorts[key], p)
		}
		if len(s.RecoveryCohorts) == 0 {
			s.RecoveryCount = 0
		}
		if p.RecoveryEnabled && ready && healthy && sample.Success && sample.FirstTokenMS >= 0 && !sample.Failed && !sample.Overload {
			d.Action, s.Reason = "up", "recovery"
		}
	}
	if h.Overflow && d.Action != "down" {
		d.Action = ""
		s.Status, s.Message = "failed", "Too many request categories (maximum 32); review model/context settings and save grouping settings to restart sampling"
	}
	until, _ := time.Parse(time.RFC3339Nano, s.ProtectedUntil)
	if now.Before(until) {
		d.Action = ""
		if s.Status != "failed" {
			s.Message = "Group protection is active; collecting new requests"
		}
		return d
	}
	if d.Action != "" && p.ObserveOnly {
		s.Status, s.Message = "observing", fmt.Sprintf("Suggested %s: %s; observation mode keeps current groups", d.Action, s.Reason)
		d.Action = ""
	}
	return d
}
