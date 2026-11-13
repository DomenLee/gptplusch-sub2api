package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/identity"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

var ErrUnifiedSettings = infraerrors.BadRequest("USE_UNIFIED_SETTINGS", "该账号已升级，请在通用账号设置中修改邮箱或密码；忘记密码可通过邮箱验证码找回")

type unifiedRegistrationKey struct{}

func UnifiedRegistration(ctx context.Context) string {
	id, _ := ctx.Value(unifiedRegistrationKey{}).(string)
	return id
}
func LocalSessionVersion(u *User) int64 { return resolvedTokenVersion(u) }
func UnifiedError(err error) error {
	if err == nil {
		return nil
	}
	var e *identity.Error
	if errors.As(err, &e) {
		return infraerrors.New(e.Status, e.Code, e.Message)
	}
	return err
}
func (s *AuthService) ConfigureUnified(db *sql.DB) {
	if os.Getenv("UNIFIED_ACCOUNT_ENABLED") != "true" {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM unified_links WHERE site='ai')`).Scan(&exists); err == nil && exists {
			panic("AI accounts have been migrated; UNIFIED_ACCOUNT_ENABLED must remain true")
		}
		return
	}
	key := os.Getenv("UNIFIED_ACCOUNT_KEY")
	if len(key) < 32 {
		panic("UNIFIED_ACCOUNT_KEY must contain at least 32 characters")
	}
	s.Unified = &identity.Service{DB: db, Key: key, Alias: NormalizeEmailForAliasDedup, Send: s.emailService.SendEmail, CheckEmail: s.checkStoreEmail}
	if s.emailQueueService != nil {
		s.Unified.Notify = s.emailQueueService.EnqueueAccountNotice
	}
	s.Unified.Legacy = func(ctx context.Context, email string) (bool, error) {
		// An already linked exact account is safe for idempotent registration retries.
		u, err := s.userRepo.GetByEmail(ctx, email)
		if err == nil && u.UnifiedID != "" && u.Email == email {
			return false, nil
		}
		if err != nil && !errors.Is(err, ErrUserNotFound) {
			return false, err
		}
		return s.existsByEmailOrAlias(ctx, email)
	}
	if repo, ok := s.userRepo.(interface{ SetUnified(*identity.Service) }); ok {
		repo.SetUnified(s.Unified)
	}
}
func (s *AuthService) RegisterUnified(ctx context.Context, in identity.Input, promo, invite, affiliate string) (*User, error) {
	if s.Unified == nil {
		return nil, ErrServiceUnavailable
	}
	replay, err := s.Unified.RegistrationReplay(ctx, in)
	if err != nil {
		return nil, err
	}
	if !replay {
		if s.settingService == nil || !s.settingService.IsRegistrationEnabled(ctx) {
			return nil, ErrRegDisabled
		}
		if isReservedEmail(in.Email) {
			return nil, ErrEmailReserved
		}
		if err := s.validateRegistrationEmailQuota(ctx, in.Email); err != nil {
			return nil, err
		}
	}
	a, err := s.Unified.Register(ctx, in)
	if err != nil {
		return nil, UnifiedError(err)
	}
	u, err := s.provisionUnified(ctx, a, in.Password, promo, invite, affiliate)
	if err != nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "UNIFIED_PROFILE_PENDING", "通用账号已注册，AI 站资料暂未开通。请用同一邮箱和密码重试；如 AI 站已有历史账号，请登录旧账号后在账号安全中关联，原注册验证码无需重复申请")
	}
	return u, nil
}
func (s *AuthService) provisionUnified(ctx context.Context, a *identity.Account, password, promo, invite, affiliate string) (*User, error) {
	id, err := s.Unified.LocalID(ctx, "ai", a.ID)
	if err != nil {
		return nil, err
	}
	if id != "" {
		n, e := strconv.ParseInt(id, 10, 64)
		if e != nil {
			return nil, e
		}
		u, e := s.userRepo.GetByID(ctx, n)
		if e != nil {
			return nil, e
		}
		if !u.IsActive() {
			return nil, ErrUserNotActive
		}
		return u, nil
	}
	// A verified common email must never claim an unrelated pre-existing business account.
	if exists, e := s.existsByEmailOrAlias(ctx, a.Email); e != nil {
		return nil, e
	} else if exists {
		return nil, infraerrors.Conflict("LEGACY_ACCOUNT", "AI 站已有历史账号，请登录后在通用账号设置中关联")
	}
	ctx = context.WithValue(ctx, unifiedRegistrationKey{}, a.ID)
	_, u, err := s.RegisterWithVerification(ctx, a.Email, uuid.NewString()+uuid.NewString(), "", promo, invite, affiliate)
	if err != nil {
		return nil, err
	}
	return s.userRepo.GetByID(ctx, u.ID)
}
func (s *AuthService) loginUnified(ctx context.Context, email, password string, invitation ...string) (string, *User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	u, err := s.userRepo.GetByEmail(ctx, email)
	if err == nil && u.UnifiedID == "" {
		if !u.CheckPassword(password) {
			return "", nil, ErrInvalidCredentials
		}
	} else {
		if err != nil && !errors.Is(err, ErrUserNotFound) {
			return "", nil, err
		}
		a, e := s.Unified.Verify(ctx, email, password)
		if e != nil {
			return "", nil, UnifiedError(e)
		}
		invite := ""
		if len(invitation) > 0 {
			invite = invitation[0]
		}
		u, err = s.provisionUnified(ctx, a, password, "", invite, "")
		if err != nil {
			return "", nil, err
		}
	}
	if !u.IsActive() {
		return "", nil, ErrUserNotActive
	}
	token, err := s.GenerateToken(ctx, u)
	return token, u, err
}
func (s *AuthService) LinkUnified(ctx context.Context, userID int64, oldPassword string, in identity.Input) (*identity.Account, error) {
	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.UnifiedID != "" {
		return nil, ErrUnifiedSettings
	}
	if !u.CheckPassword(oldPassword) {
		return nil, ErrPasswordIncorrect
	}
	in.Site = "ai"
	in.LocalID = fmt.Sprint(userID)
	in.SessionVersion = resolvedTokenVersion(u)
	a, err := s.Unified.Link(ctx, in)
	return a, UnifiedError(err)
}

// Both entry points must protect the store's historical account namespace.
func (s *AuthService) checkStoreEmail(ctx context.Context, accountID, email string) error {
	unavailable := &identity.Error{Code: "STORE_CHECK_UNAVAILABLE", Message: "商城邮箱占用检查暂不可用，请稍后重试；原邮箱保持不变", Status: 503}
	base, err := url.Parse(os.Getenv("UNIFIED_STORE_URL"))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return unavailable
	}
	localHTTP := base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost" || base.Hostname() == "::1")
	if base.Scheme != "https" && !localHTTP {
		return unavailable
	}
	body, _ := json.Marshal(map[string]string{"accountId": accountID, "email": email})
	// #nosec G704 -- The destination is a validated operator-only environment value with a fixed path; user input is confined to the JSON body.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.ResolveReference(&url.URL{Path: "/api/auth/unified/email-check"}).String(), bytes.NewReader(body))
	if err != nil {
		return unavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Unified-Key", s.Unified.Key)
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// #nosec G704 -- Only the configured store receives this request, and redirects are disabled to prevent forwarding the shared key.
	response, err := client.Do(req)
	if err != nil {
		return unavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusConflict {
		return &identity.Error{Code: "ACCOUNT_CONFLICT", Message: "该邮箱已用于商城其他账号，请更换邮箱或联系平台客服处理", Status: 409}
	}
	if response.StatusCode != http.StatusOK {
		return unavailable
	}
	var result struct {
		Available bool `json:"available"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&result) != nil || !result.Available {
		return unavailable
	}
	return nil
}
