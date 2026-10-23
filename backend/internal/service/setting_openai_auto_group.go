package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

const SettingKeyOpenAIAutoGroup = "openai_auto_group"
const OpenAIAutoGroupRateBoundary = 2.0

type OpenAIAutoGroupSettings struct {
	DefaultHighRate  bool   `json:"default_high_rate"`
	Enabled          bool   `json:"enabled"`
	WindowMinutes    int    `json:"window_minutes"`
	FailureThreshold int    `json:"failure_threshold"`
	Revision         string `json:"revision,omitempty"`
}

type cachedOpenAIAutoGroupSettings struct {
	settings  OpenAIAutoGroupSettings
	expiresAt time.Time
}

func DefaultOpenAIAutoGroupSettings() *OpenAIAutoGroupSettings {
	return &OpenAIAutoGroupSettings{WindowMinutes: 5, FailureThreshold: 3}
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
	if err := validateOpenAIAutoGroupSettings(settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func validateOpenAIAutoGroupSettings(settings *OpenAIAutoGroupSettings) error {
	if settings == nil {
		return fmt.Errorf("GPT auto-group settings are required")
	}
	if settings.WindowMinutes < 1 || settings.WindowMinutes > 60 {
		return fmt.Errorf("window_minutes must be an integer between 1 and 60")
	}
	if settings.FailureThreshold < 1 || settings.FailureThreshold > 1000 {
		return fmt.Errorf("failure_threshold must be an integer between 1 and 1000")
	}
	return nil
}

func (s *SettingService) SetOpenAIAutoGroupSettings(ctx context.Context, settings *OpenAIAutoGroupSettings) error {
	if err := validateOpenAIAutoGroupSettings(settings); err != nil {
		return err
	}
	s.openAIAutoGroupMu.Lock()
	defer s.openAIAutoGroupMu.Unlock()
	saved := *settings
	saved.Revision = uuid.NewString()
	value, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAIAutoGroup, string(value)); err != nil {
		return err
	}
	s.openAIAutoGroupCache.Store(&cachedOpenAIAutoGroupSettings{settings: saved, expiresAt: time.Now().Add(5 * time.Second)})
	*settings = saved
	return nil
}

func (s *SettingService) openAIAutoGroupRuntimeSettings(ctx context.Context) (*OpenAIAutoGroupSettings, error) {
	if s == nil {
		return DefaultOpenAIAutoGroupSettings(), nil
	}
	if cached, ok := s.openAIAutoGroupCache.Load().(*cachedOpenAIAutoGroupSettings); ok && time.Now().Before(cached.expiresAt) {
		settings := cached.settings
		return &settings, nil
	}
	result, err, _ := s.openAIAutoGroupSF.Do("settings", func() (any, error) {
		s.openAIAutoGroupMu.Lock()
		defer s.openAIAutoGroupMu.Unlock()
		if cached, ok := s.openAIAutoGroupCache.Load().(*cachedOpenAIAutoGroupSettings); ok && time.Now().Before(cached.expiresAt) {
			return cached.settings, nil
		}
		settings, err := s.GetOpenAIAutoGroupSettings(ctx)
		if err != nil {
			return nil, err
		}
		s.openAIAutoGroupCache.Store(&cachedOpenAIAutoGroupSettings{settings: *settings, expiresAt: time.Now().Add(5 * time.Second)})
		return *settings, nil
	})
	if err != nil {
		return nil, err
	}
	settings, ok := result.(OpenAIAutoGroupSettings)
	if !ok {
		return nil, fmt.Errorf("invalid GPT auto-group settings result")
	}
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
		if openAIAutoGroupEligible(group, accountType) && group.RateMultiplier > OpenAIAutoGroupRateBoundary {
			ids = append(ids, group.ID)
		}
	}
	if len(ids) == 0 {
		return nil, true, fmt.Errorf("GPT account: no compatible active groups above 2x; select groups manually or disable default high-rate grouping")
	}
	return ids, true, nil
}
