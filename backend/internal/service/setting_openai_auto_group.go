package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
)

const SettingKeyOpenAIAutoGroup = "openai_auto_group"
const OpenAIAutoGroupRateBoundary = 2.0

type OpenAIAutoGroupSettings struct {
	DefaultHighRate bool    `json:"default_high_rate"`
	Enabled         bool    `json:"enabled"`
	SkipGroupIDs    []int64 `json:"skip_group_ids"`
	// WindowMinutes is accepted only for compatibility with older clients.
	WindowMinutes         int    `json:"window_minutes,omitempty"`
	FailureWindow         int    `json:"failure_window"`
	FailureThreshold      int    `json:"failure_threshold"`
	LatencyEnabled        bool   `json:"latency_enabled"`
	LatencyWindow         int    `json:"latency_window"`
	SlowThreshold         int    `json:"slow_threshold"`
	FirstTokenSeconds     int    `json:"first_token_seconds"`
	RecoveryEnabled       bool   `json:"recovery_enabled"`
	RecoverySamples       int    `json:"recovery_samples"`
	RecoveryFastPercent   int    `json:"recovery_fast_percent"`
	RecoveryMedianSeconds int    `json:"recovery_median_seconds"`
	RecoveryFastStreak    int    `json:"recovery_fast_streak"`
	ProtectionMinutes     int    `json:"protection_minutes"`
	ObserveOnly           bool   `json:"observe_only"`
	Revision              string `json:"revision,omitempty"`
}

func (s *OpenAIAutoGroupSettings) Active() bool {
	return s != nil && (s.Enabled || s.LatencyEnabled || s.RecoveryEnabled)
}

// SkipsGroups exempts the whole account because automatic changes operate on
// all of its bindings, even when the triggering request used a different group.
func (s *OpenAIAutoGroupSettings) SkipsGroups(ids []int64) bool {
	if s == nil {
		return false
	}
	for _, id := range ids {
		if slices.Contains(s.SkipGroupIDs, id) {
			return true
		}
	}
	return false
}

func cloneOpenAIAutoGroupSettings(s OpenAIAutoGroupSettings) OpenAIAutoGroupSettings {
	s.SkipGroupIDs = append([]int64{}, s.SkipGroupIDs...)
	return s
}

type cachedOpenAIAutoGroupSettings struct {
	settings  OpenAIAutoGroupSettings
	expiresAt time.Time
}

func DefaultOpenAIAutoGroupSettings() *OpenAIAutoGroupSettings {
	return &OpenAIAutoGroupSettings{
		SkipGroupIDs: []int64{}, FailureWindow: 20, FailureThreshold: 3, LatencyWindow: 10, SlowThreshold: 3,
		FirstTokenSeconds: 10, RecoverySamples: 30, RecoveryFastPercent: 90,
		RecoveryMedianSeconds: 8, RecoveryFastStreak: 10, ProtectionMinutes: 30,
	}
}

func (s *SettingService) GetOpenAIAutoGroupSettings(ctx context.Context) (*OpenAIAutoGroupSettings, error) {
	settings := DefaultOpenAIAutoGroupSettings()
	if s == nil || s.settingRepo == nil {
		return settings, nil
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIAutoGroup)
	if errors.Is(err, ErrSettingNotFound) {
		return settings, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load GPT auto-group settings: %w", err)
	}
	if value == "" {
		return settings, nil
	}
	if err := json.Unmarshal([]byte(value), settings); err != nil {
		return nil, fmt.Errorf("decode GPT auto-group settings: %w", err)
	}
	// A legacy threshold above the new default window must remain usable.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &fields); err != nil {
		return nil, err
	}
	if _, exists := fields["failure_window"]; !exists {
		settings.FailureWindow = max(settings.FailureWindow, settings.FailureThreshold)
	}
	if err := validateOpenAIAutoGroupSettings(settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func validateOpenAIAutoGroupSettings(settings *OpenAIAutoGroupSettings) error {
	if settings == nil {
		return fmt.Errorf("GPT auto-group settings are required")
	}
	if len(settings.SkipGroupIDs) > 1000 {
		return fmt.Errorf("skip_group_ids must contain at most 1000 groups")
	}
	seen := make(map[int64]bool, len(settings.SkipGroupIDs))
	for _, id := range settings.SkipGroupIDs {
		if id <= 0 || seen[id] {
			return fmt.Errorf("skip_group_ids must contain unique positive group IDs")
		}
		seen[id] = true
	}
	if settings.WindowMinutes < 0 || settings.WindowMinutes > 60 {
		return fmt.Errorf("legacy window_minutes must be between 0 and 60")
	}
	for name, value := range map[string]int{
		"failure_window": settings.FailureWindow, "latency_window": settings.LatencyWindow,
		"recovery_samples": settings.RecoverySamples,
	} {
		if value < 1 || value > 1000 {
			return fmt.Errorf("%s must be between 1 and 1000", name)
		}
	}
	if settings.FailureThreshold < 1 || settings.FailureThreshold > settings.FailureWindow {
		return fmt.Errorf("failure_threshold must be between 1 and failure_window")
	}
	if settings.SlowThreshold < 1 || settings.SlowThreshold > settings.LatencyWindow {
		return fmt.Errorf("slow_threshold must be between 1 and latency_window")
	}
	if settings.FirstTokenSeconds < 1 || settings.FirstTokenSeconds > 300 || settings.RecoveryMedianSeconds < 1 || settings.RecoveryMedianSeconds > settings.FirstTokenSeconds {
		return fmt.Errorf("first_token_seconds must be 1–300; recovery_median_seconds must be positive and no greater than it")
	}
	if settings.RecoveryFastPercent < 1 || settings.RecoveryFastPercent > 100 || settings.RecoveryFastStreak < 1 || settings.RecoveryFastStreak > settings.RecoverySamples || settings.RecoverySamples < settings.LatencyWindow {
		return fmt.Errorf("recovery requires 1–100 percent, a positive streak no larger than recovery_samples, and recovery_samples >= latency_window")
	}
	if settings.ProtectionMinutes < 0 || settings.ProtectionMinutes > 1440 {
		return fmt.Errorf("protection_minutes must be between 0 and 1440")
	}
	return nil
}

func (s *SettingService) SetOpenAIAutoGroupSettings(ctx context.Context, settings *OpenAIAutoGroupSettings) error {
	if err := validateOpenAIAutoGroupSettings(settings); err != nil {
		return err
	}
	s.openAIAutoGroupMu.Lock()
	defer s.openAIAutoGroupMu.Unlock()
	saved := cloneOpenAIAutoGroupSettings(*settings)
	saved.Revision = uuid.NewString()
	value, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAIAutoGroup, string(value)); err != nil {
		return err
	}
	s.openAIAutoGroupCache.Store(&cachedOpenAIAutoGroupSettings{settings: cloneOpenAIAutoGroupSettings(saved), expiresAt: time.Now().Add(5 * time.Second)})
	*settings = saved
	return nil
}

func (s *SettingService) openAIAutoGroupRuntimeSettings(ctx context.Context) (*OpenAIAutoGroupSettings, error) {
	if s == nil {
		return DefaultOpenAIAutoGroupSettings(), nil
	}
	if cached, ok := s.openAIAutoGroupCache.Load().(*cachedOpenAIAutoGroupSettings); ok && time.Now().Before(cached.expiresAt) {
		settings := cloneOpenAIAutoGroupSettings(cached.settings)
		return &settings, nil
	}
	result, err, _ := s.openAIAutoGroupSF.Do("settings", func() (any, error) {
		s.openAIAutoGroupMu.Lock()
		defer s.openAIAutoGroupMu.Unlock()
		if cached, ok := s.openAIAutoGroupCache.Load().(*cachedOpenAIAutoGroupSettings); ok && time.Now().Before(cached.expiresAt) {
			return cloneOpenAIAutoGroupSettings(cached.settings), nil
		}
		settings, err := s.GetOpenAIAutoGroupSettings(ctx)
		if err != nil {
			return nil, err
		}
		s.openAIAutoGroupCache.Store(&cachedOpenAIAutoGroupSettings{settings: cloneOpenAIAutoGroupSettings(*settings), expiresAt: time.Now().Add(5 * time.Second)})
		return cloneOpenAIAutoGroupSettings(*settings), nil
	})
	if err != nil {
		return nil, err
	}
	settings, ok := result.(OpenAIAutoGroupSettings)
	if !ok {
		return nil, fmt.Errorf("invalid GPT auto-group settings result")
	}
	settings = cloneOpenAIAutoGroupSettings(settings)
	return &settings, nil
}

func openAIAutoGroupEligible(group *Group, accountType string) bool {
	return group != nil && group.ID > 0 && group.Platform == PlatformOpenAI && group.IsActive() &&
		!group.AutoRouteEnabled && !math.IsNaN(group.RateMultiplier) && !math.IsInf(group.RateMultiplier, 0) &&
		group.RateMultiplier >= 0 && (accountType != AccountTypeAPIKey || !group.RequireOAuthOnly)
}

func openAIDefaultHighRateGroups(ctx context.Context, settings *SettingService, repo GroupRepository, platform, accountType string) ([]int64, bool, error) {
	if platform != PlatformOpenAI || settings == nil {
		return nil, false, nil
	}
	policy, err := settings.openAIAutoGroupRuntimeSettings(ctx)
	if err != nil {
		return nil, false, err
	}
	if !policy.DefaultHighRate {
		return nil, false, nil
	}
	groups, err := repo.ListActiveByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, true, err
	}
	ids := make([]int64, 0, len(groups))
	for index := range groups {
		group := &groups[index]
		if openAIAutoGroupEligible(group, accountType) && group.RateMultiplier > OpenAIAutoGroupRateBoundary && !policy.SkipsGroups([]int64{group.ID}) {
			ids = append(ids, group.ID)
		}
	}
	if len(ids) == 0 {
		return nil, true, fmt.Errorf("GPT account: no compatible active groups above 2x; select groups manually or disable default high-rate grouping")
	}
	return ids, true, nil
}
