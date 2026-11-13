//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnifiedStoreRegistrationEmailPolicy(t *testing.T) {
	ctx := context.Background()
	repo := &settingRepoStub{values: map[string]string{
		SettingKeyRegistrationEmailSuffixWhitelist: `["@gmail.com","@qq.com","@163.com","@126.com","@mail.com","@outlook.com"]`,
	}}
	s := &AuthService{settingService: &SettingService{settingRepo: repo}}
	for _, email := range []string{"a@gmail.com", "a@qq.com", "a@163.com", "a@126.com", "a@mail.com", "a@outlook.com", " User@QQ.COM "} {
		require.NoError(t, s.validateStoreRegistrationEmail(ctx, email), email)
	}
	for _, email := range []string{"a@example.com", "a@sub.qq.com", "a@qq.com.evil.test", "a@notqq.com", "not-an-email"} {
		require.ErrorIs(t, s.validateStoreRegistrationEmail(ctx, email), ErrEmailSuffixNotAllowed, email)
	}
	// Read the authoritative setting each time, without a stale copied whitelist.
	repo.values[SettingKeyRegistrationEmailSuffixWhitelist] = `["*.edu.cn"]`
	require.NoError(t, s.validateStoreRegistrationEmail(ctx, "a@dept.school.edu.cn"))
	require.NoError(t, s.validateStoreRegistrationEmail(ctx, "a@edu.cn"))
	require.ErrorIs(t, s.validateStoreRegistrationEmail(ctx, "a@qq.com"), ErrEmailSuffixNotAllowed)
	require.ErrorIs(t, s.validateStoreRegistrationEmail(ctx, "a@eviledu.cn"), ErrEmailSuffixNotAllowed)
	repo.values[SettingKeyRegistrationEmailSuffixWhitelist] = "[]"
	require.NoError(t, s.validateStoreRegistrationEmail(ctx, "a@custom.test"))
	suffixes, err := s.StoreRegistrationEmailSuffixes(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{}, suffixes)
}

func TestUnifiedStoreRegistrationEmailPolicyReadFailure(t *testing.T) {
	ctx := context.Background()
	repo := &settingRepoStub{err: errors.New("settings unavailable")}
	s := &AuthService{settingService: &SettingService{settingRepo: repo}}
	require.ErrorIs(t, s.validateStoreRegistrationEmail(ctx, "a@qq.com"), ErrServiceUnavailable)
	repo.err = nil
	for _, raw := range []string{`not-json`, `["invalid_domain"]`, `{}`} {
		repo.values = map[string]string{SettingKeyRegistrationEmailSuffixWhitelist: raw}
		require.ErrorIs(t, s.validateStoreRegistrationEmail(ctx, "a@qq.com"), ErrServiceUnavailable, raw)
	}
	s.settingService = nil
	require.ErrorIs(t, s.validateStoreRegistrationEmail(ctx, "a@qq.com"), ErrServiceUnavailable)
}

func TestUnifiedStoreRegistrationEmailPolicyConfigured(t *testing.T) {
	t.Setenv("UNIFIED_ACCOUNT_ENABLED", "true")
	t.Setenv("UNIFIED_ACCOUNT_KEY", "local-only-test-key-at-least-32-characters")
	s := &AuthService{emailService: &EmailService{}, settingService: &SettingService{settingRepo: &settingRepoStub{
		values: map[string]string{SettingKeyRegistrationEmailSuffixWhitelist: `["@qq.com"]`},
	}}}
	s.ConfigureUnified(nil)
	require.NotNil(t, s.Unified.CheckRegistrationEmail)
	require.NoError(t, s.Unified.CheckRegistrationEmail(context.Background(), "a@qq.com"))
	require.ErrorIs(t, s.Unified.CheckRegistrationEmail(context.Background(), "a@custom.test"), ErrEmailSuffixNotAllowed)
}
