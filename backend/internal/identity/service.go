// Package identity owns shared credentials, never website sessions or balances.
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"math/big"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

type Error struct {
	Code, Message string
	Status        int
}

func (e *Error) Error() string                    { return e.Message }
func fail(code, message string, status int) error { return &Error{code, message, status} }

var Invalid = &Error{"INVALID_CREDENTIALS", "邮箱或密码错误，请核对后重试", 401}
var CodeInvalid = &Error{"CODE_INVALID", "验证码错误、已使用或已过期，请重新获取", 400}
var Conflict = &Error{"ACCOUNT_CONFLICT", "账号或邮箱已关联，请登录已有账号或联系平台客服", 409}

type Account struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Version    int64     `json:"version"`
	VerifiedAt time.Time `json:"verifiedAt"`
	Notice     string    `json:"notice,omitempty"`
}

type Input struct {
	Email          string `json:"email"`
	Password       string `json:"password"`
	NewPassword    string `json:"newPassword"`
	ChallengeID    string `json:"challengeId"`
	Code           string `json:"code"`
	RequestID      string `json:"requestId"`
	AccountID      string `json:"accountId"`
	LocalID        string `json:"localId"`
	Purpose        string `json:"purpose"`
	Version        int64  `json:"version"`
	SessionVersion int64  `json:"-"`
	OldChallengeID string `json:"oldChallengeId"`
	OldCode        string `json:"oldCode"`
	Site           string `json:"-"`
}

type Challenge struct {
	ID         string `json:"challengeId"`
	RetryAfter int    `json:"retryAfter"`
}
type Service struct {
	CheckEmail func(context.Context, string, string) error
	Notify     func(string, string, string) error
	DB         *sql.DB
	Key        string
	Send       func(context.Context, string, string, string) error
	Alias      func(string) string
	Legacy     func(context.Context, string) (bool, error)
}

func Email(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	a, err := mail.ParseAddress(value)
	if err != nil || a.Address != value || len(value) > 120 {
		return "", fail("EMAIL_INVALID", "请输入有效邮箱，最多 120 位", 400)
	}
	return value, nil
}

func Password(value string) error {
	// bcrypt has a 72-byte limit; reject explicitly instead of truncating.
	if len([]rune(value)) < 12 || len([]byte(value)) > 72 {
		return fail("PASSWORD_INVALID", "新密码至少 12 个字符，最多 72 字节", 400)
	}
	return nil
}

func (s *Service) digest(value string) string {
	h := hmac.New(sha256.New, []byte(s.Key))
	_, _ = h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Service) Rate(ctx context.Context, key string, limit int, duration time.Duration) error {
	var n int
	err := s.DB.QueryRowContext(ctx, `INSERT INTO unified_attempts(key, expires_at) VALUES($1, NOW()+$2*INTERVAL '1 second')
ON CONFLICT(key) DO UPDATE SET attempts=CASE WHEN unified_attempts.expires_at<NOW() THEN 1 ELSE unified_attempts.attempts+1 END,
expires_at=CASE WHEN unified_attempts.expires_at<NOW() THEN EXCLUDED.expires_at ELSE unified_attempts.expires_at END RETURNING attempts`, s.digest(key), int(duration.Seconds())).Scan(&n)
	if err != nil {
		return err
	}
	if n > limit {
		return fail("RATE_LIMITED", "操作过于频繁，请稍后重试", 429)
	}
	return nil
}

func (s *Service) SendCode(ctx context.Context, in Input) (*Challenge, error) {
	email, err := Email(in.Email)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{"register": "注册账号", "link": "关联已有账号", "recover": "找回通用密码", "email-old": "确认原邮箱", "email-new": "验证新邮箱"}
	label, ok := labels[in.Purpose]
	if !ok {
		return nil, fail("PURPOSE_INVALID", "验证码用途无效", 400)
	}
	if err = s.Rate(ctx, "email-hour:"+email, 5, time.Hour); err != nil {
		return nil, err
	}
	if err = s.Rate(ctx, "email-day:"+email, 10, 24*time.Hour); err != nil {
		return nil, err
	}
	if err = s.Rate(ctx, "email-cooldown:"+email+":"+in.Purpose, 1, time.Minute); err != nil {
		return nil, err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	code := fmt.Sprintf("%06d", n.Int64())
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, email+":"+in.Purpose); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE unified_codes SET used_at=NOW() WHERE email=$1 AND purpose=$2 AND used_at IS NULL`, email, in.Purpose); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO unified_codes(id,email,purpose,code_hash,expires_at) VALUES($1,$2,$3,$4,NOW()+INTERVAL '5 minutes')`, id, email, in.Purpose, s.digest(id+":"+code)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	body := fmt.Sprintf("<p>GPTPLUSCH %s验证码：</p><p style=\"font-size:28px\">%s</p><p>5 分钟内有效，仅用于%s。请勿向任何人提供验证码。非本人操作请忽略。</p>", html.EscapeString(label), code, html.EscapeString(label))
	if s.Send == nil {
		err = errors.New("mail not configured")
	} else {
		err = s.Send(ctx, email, "GPTPLUSCH 邮箱验证码", body)
	}
	if err != nil {
		_, _ = s.DB.ExecContext(ctx, `UPDATE unified_codes SET used_at=NOW() WHERE id=$1`, id)
		return nil, fail("MAIL_UNAVAILABLE", "验证码发送失败，请稍后重试或修改邮箱", 503)
	}
	return &Challenge{id, 60}, nil
}

type row interface{ Scan(...any) error }

func scanAccount(r row) (*Account, string, error) {
	a := &Account{}
	var hash string
	err := r.Scan(&a.ID, &a.Email, &a.Version, &a.VerifiedAt, &hash)
	return a, hash, err
}

const accountColumns = "a.id,a.email,a.version,a.verified_at,a.password_hash"

func (s *Service) Get(ctx context.Context, id string) (*Account, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, Conflict
	}
	a, _, err := scanAccount(s.DB.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM unified_accounts a WHERE a.id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Conflict
	}
	return a, err
}

func (s *Service) Linked(ctx context.Context, site, localID string) (*Account, error) {
	a, _, err := scanAccount(s.DB.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM unified_links l JOIN unified_accounts a ON a.id=l.account_id WHERE l.site=$1 AND l.local_id=$2`, site, localID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (s *Service) LocalID(ctx context.Context, site, accountID string) (string, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT local_id FROM unified_links WHERE site=$1 AND account_id=$2`, site, accountID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Service) Verify(ctx context.Context, email, password string) (*Account, error) {
	email, err := Email(email)
	if err != nil {
		return nil, Invalid
	}
	if len(password) > 72 {
		return nil, Invalid
	}
	if err = s.Rate(ctx, "password:"+email, 30, 5*time.Minute); err != nil {
		return nil, err
	}
	a, hash, err := scanAccount(s.DB.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM unified_accounts a WHERE email=$1`, email))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Invalid
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, Invalid
	}
	return a, nil
}

// checkCode locks the challenge until the caller commits the business operation.
func (s *Service) checkCode(ctx context.Context, tx *sql.Tx, in Input, purpose, email string) (string, error) {
	if _, err := uuid.Parse(in.ChallengeID); err != nil {
		return "", CodeInvalid
	}
	var hash, storedEmail, storedPurpose string
	var attempts int
	var expires time.Time
	var used sql.NullTime
	var operation, result sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT code_hash,email,purpose,attempts,expires_at,used_at,operation_key,result_id FROM unified_codes WHERE id=$1 FOR UPDATE`, in.ChallengeID).Scan(&hash, &storedEmail, &storedPurpose, &attempts, &expires, &used, &operation, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return "", CodeInvalid
	}
	if err != nil {
		return "", err
	}
	if storedEmail != email || storedPurpose != purpose || attempts >= 5 {
		return "", CodeInvalid
	}
	if used.Valid {
		if purpose == "register" && len(in.RequestID) >= 16 && operation.String == in.RequestID && result.Valid {
			return result.String, nil
		}
		return "", CodeInvalid
	}
	if !expires.After(time.Now()) {
		return "", CodeInvalid
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(s.digest(in.ChallengeID+":"+in.Code))) != 1 {
		if _, err = tx.ExecContext(ctx, `UPDATE unified_codes SET attempts=attempts+1 WHERE id=$1`, in.ChallengeID); err != nil {
			return "", err
		}
		// Persist failed attempts even though the requested operation is rejected.
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return "", CodeInvalid
	}
	return "", nil
}

func (s *Service) operationKey(in Input) (string, error) {
	if len(in.RequestID) < 16 || len(in.RequestID) > 100 || (in.Site != "store" && in.Site != "ai") {
		return "", fail("REQUEST_INVALID", "操作请求无效，请刷新后重试", 400)
	}
	return s.digest(in.Site + ":" + in.RequestID), nil
}

func (s *Service) replayOperation(ctx context.Context, tx *sql.Tx, in Input, action, payload string) (*Account, bool, error) {
	key, err := s.operationKey(in)
	if err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return nil, false, err
	}
	var storedAction, storedHash, accountID string
	err = tx.QueryRowContext(ctx, `SELECT action,payload_hash,account_id::text FROM unified_operations WHERE request_key=$1 AND expires_at>NOW()`, key).Scan(&storedAction, &storedHash, &accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if storedAction != action || subtle.ConstantTimeCompare([]byte(storedHash), []byte(s.digest(payload))) != 1 {
		return nil, false, fail("REQUEST_CONFLICT", "操作请求内容已变化，请重新提交", 409)
	}
	_ = tx.Rollback()
	account, err := s.Get(ctx, accountID)
	return account, true, err
}

func (s *Service) saveOperation(ctx context.Context, tx *sql.Tx, in Input, action, payload, accountID string) error {
	key, err := s.operationKey(in)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM unified_operations WHERE expires_at<NOW()`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO unified_operations(request_key,site,action,payload_hash,account_id,expires_at) VALUES($1,$2,$3,$4,$5,NOW()+INTERVAL '7 days')`, key, in.Site, action, s.digest(payload), accountID)
	return err
}

func (s *Service) Operation(ctx context.Context, site, requestID string) (*Account, error) {
	in := Input{Site: site, RequestID: requestID}
	key, err := s.operationKey(in)
	if err != nil {
		return nil, err
	}
	var accountID string
	err = s.DB.QueryRowContext(ctx, `SELECT account_id::text FROM unified_operations WHERE request_key=$1 AND site=$2 AND expires_at>NOW()`, key, site).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, accountID)
}

func sqlError(err error) error {
	var pg *pq.Error
	if errors.As(err, &pg) && pg.Code == "23505" {
		return Conflict
	}
	return err
}

func (s *Service) Register(ctx context.Context, in Input) (*Account, error) {
	email, err := Email(in.Email)
	if err != nil {
		return nil, err
	}
	if err = Password(in.Password); err != nil {
		return nil, err
	}
	if len(in.RequestID) < 16 || len(in.RequestID) > 100 {
		return nil, fail("REQUEST_INVALID", "注册请求无效，请刷新后重试", 400)
	}
	if s.Legacy != nil {
		exists, e := s.Legacy(ctx, email)
		if e != nil {
			return nil, e
		}
		if exists {
			return nil, fail("LEGACY_ACCOUNT", "AI 站已有历史账号，请先登录 AI 站完成账号升级", 409)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	replay, err := s.checkCode(ctx, tx, in, "register", email)
	if err != nil {
		return nil, err
	}
	if replay != "" {
		_ = tx.Rollback()
		return s.Verify(ctx, email, in.Password)
	}
	id := uuid.NewString()
	if _, err = tx.ExecContext(ctx, `INSERT INTO unified_accounts(id,email,alias_key,password_hash) VALUES($1,$2,$3,$4)`, id, email, s.Alias(email), string(hash)); err != nil {
		return nil, sqlError(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE unified_codes SET used_at=NOW(),operation_key=$2,result_id=$3 WHERE id=$1`, in.ChallengeID, in.RequestID, id); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO unified_events(account_id,site,action) VALUES($1,$2,'register')`, id, in.Site); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Service) RegistrationReplay(ctx context.Context, in Input) (bool, error) {
	email, err := Email(in.Email)
	if err != nil {
		return false, err
	}
	if _, err = uuid.Parse(in.ChallengeID); err != nil || len(in.RequestID) < 16 || len(in.RequestID) > 100 {
		return false, nil
	}
	var exists bool
	err = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM unified_codes WHERE id=$1 AND email=$2 AND purpose='register' AND used_at IS NOT NULL AND operation_key=$3 AND result_id IS NOT NULL)`, in.ChallengeID, email, in.RequestID).Scan(&exists)
	return exists, err
}

func (s *Service) Attach(ctx context.Context, in Input) (*Account, error) {
	a, err := s.Verify(ctx, in.Email, in.Password)
	if err != nil {
		return nil, err
	}
	if in.LocalID == "" || len(in.LocalID) > 120 || (in.Site != "store" && in.Site != "ai") {
		return nil, Conflict
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO unified_links(site,local_id,account_id,session_version) VALUES($1,$2,$3,$4) ON CONFLICT(site,local_id) DO UPDATE SET account_id=unified_links.account_id WHERE unified_links.account_id=EXCLUDED.account_id`, in.Site, in.LocalID, a.ID, in.SessionVersion)
	if err != nil {
		return nil, sqlError(err)
	}
	linked, err := s.Linked(ctx, in.Site, in.LocalID)
	if err != nil {
		return nil, err
	}
	if linked == nil || linked.ID != a.ID {
		return nil, Conflict
	}
	return a, nil
}

// Link is called only after the owning site verifies its legacy password/MFA.
func (s *Service) Link(ctx context.Context, in Input) (*Account, error) {
	if len(in.Password) > 72 {
		return nil, Invalid
	}
	email, err := Email(in.Email)
	if err != nil {
		return nil, err
	}
	if in.LocalID == "" || len(in.LocalID) > 120 || (in.Site != "ai" && in.Site != "store") {
		return nil, Conflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, s.Alias(email)); err != nil {
		return nil, err
	}
	if _, err = s.checkCode(ctx, tx, in, "link", email); err != nil {
		return nil, err
	}
	a, hash, err := scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM unified_accounts a WHERE email=$1 FOR UPDATE`, email))
	if errors.Is(err, sql.ErrNoRows) {
		if in.Site == "store" && s.Legacy != nil {
			exists, e := s.Legacy(ctx, email)
			if e != nil {
				return nil, e
			}
			if exists {
				return nil, fail("LEGACY_ACCOUNT", "请先在 AI 站登录并升级旧账号，再返回商城关联", 409)
			}
		}
		if err = Password(in.Password); err != nil {
			return nil, err
		}
		bytes, e := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		if e != nil {
			return nil, e
		}
		a = &Account{ID: uuid.NewString(), Email: email}
		if _, err = tx.ExecContext(ctx, `INSERT INTO unified_accounts(id,email,alias_key,password_hash) VALUES($1,$2,$3,$4)`, a.ID, email, s.Alias(email), string(bytes)); err != nil {
			return nil, sqlError(err)
		}
	} else if err != nil {
		return nil, err
	} else if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		return nil, Invalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO unified_links(site,local_id,account_id,session_version) VALUES($1,$2,$3,$4) ON CONFLICT(site,local_id) DO NOTHING`, in.Site, in.LocalID, a.ID, in.SessionVersion); err != nil {
		return nil, sqlError(err)
	}
	var bound string
	if err = tx.QueryRowContext(ctx, `SELECT account_id FROM unified_links WHERE site=$1 AND local_id=$2`, in.Site, in.LocalID).Scan(&bound); err != nil {
		return nil, err
	}
	if bound != a.ID {
		return nil, Conflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE unified_codes SET used_at=NOW(),result_id=$2 WHERE id=$1`, in.ChallengeID, a.ID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO unified_events(account_id,site,action) VALUES($1,$2,'link')`, a.ID, in.Site); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(ctx, a.ID)
}

func (s *Service) ChangePassword(ctx context.Context, in Input, recovery bool) (*Account, error) {
	if !recovery && len(in.Password) > 72 {
		return nil, Invalid
	}
	if err := Password(in.NewPassword); err != nil {
		return nil, err
	}
	email, err := Email(in.Email)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	action := "password"
	if recovery {
		action = "recover"
	}
	payload := strings.Join([]string{action, in.Email, in.AccountID, in.Password, in.NewPassword, fmt.Sprint(in.Version), in.ChallengeID, in.Code}, "\x00")
	if replay, ok, replayErr := s.replayOperation(ctx, tx, in, action, payload); ok || replayErr != nil {
		return replay, replayErr
	}
	a, old, err := scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM unified_accounts a WHERE email=$1 FOR UPDATE`, email))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Invalid
	}
	if err != nil {
		return nil, err
	}
	if recovery {
		if _, err = s.checkCode(ctx, tx, in, "recover", email); err != nil {
			return nil, err
		}
	} else {
		if a.ID != in.AccountID || bcrypt.CompareHashAndPassword([]byte(old), []byte(in.Password)) != nil {
			return nil, Invalid
		}
		if a.Version != in.Version {
			return nil, fail("CREDENTIAL_CHANGED", "密码已被其他操作更新，请重新登录后重试", 409)
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE unified_accounts SET password_hash=$2,version=version+1,updated_at=NOW() WHERE id=$1`, a.ID, string(hash)); err != nil {
		return nil, err
	}
	if recovery {
		if _, err = tx.ExecContext(ctx, `UPDATE unified_codes SET used_at=NOW() WHERE id=$1`, in.ChallengeID); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO unified_events(account_id,site,action) VALUES($1,$2,$3)`, a.ID, in.Site, map[bool]string{true: "recover", false: "password"}[recovery]); err != nil {
		return nil, err
	}
	if in.Site == "ai" {
		if _, err = tx.ExecContext(ctx, `UPDATE unified_links SET session_version=CASE WHEN session_version=9223372036854775807 THEN 0 ELSE session_version+1 END WHERE site='ai' AND account_id=$1`, a.ID); err != nil {
			return nil, err
		}
	}
	if err = s.saveOperation(ctx, tx, in, action, payload, a.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	updated, err := s.Get(ctx, a.ID)
	if err == nil {
		s.notify(updated, []string{a.Email}, "通用密码已修改")
	}
	return updated, err
}

func (s *Service) ChangeEmail(ctx context.Context, in Input) (*Account, error) {
	if len(in.Password) > 72 {
		return nil, Invalid
	}
	email, err := Email(in.Email)
	if err != nil {
		return nil, err
	}
	if _, err = uuid.Parse(in.AccountID); err != nil {
		return nil, Conflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	payload := strings.Join([]string{"email", in.AccountID, in.Email, in.Password, fmt.Sprint(in.Version), in.ChallengeID, in.Code, in.OldChallengeID, in.OldCode}, "\x00")
	if replay, ok, replayErr := s.replayOperation(ctx, tx, in, "email", payload); ok || replayErr != nil {
		return replay, replayErr
	}
	a, hash, err := scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM unified_accounts a WHERE id=$1 FOR UPDATE`, in.AccountID))
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		return nil, Invalid
	}
	if a.Version != in.Version {
		return nil, Conflict
	}
	old := in
	old.ChallengeID = in.OldChallengeID
	old.Code = in.OldCode
	if _, err = s.checkCode(ctx, tx, old, "email-old", a.Email); err != nil {
		return nil, err
	}
	if _, err = s.checkCode(ctx, tx, in, "email-new", email); err != nil {
		return nil, err
	}
	if s.Legacy != nil && a.Email != email {
		exists, e := s.Legacy(ctx, email)
		if e != nil {
			return nil, e
		}
		if exists {
			return nil, Conflict
		}
	}
	if s.CheckEmail != nil && a.Email != email {
		if err = s.CheckEmail(ctx, a.ID, email); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE unified_accounts SET email=$2,alias_key=$3,version=version+1,verified_at=NOW(),updated_at=NOW() WHERE id=$1`, a.ID, email, s.Alias(email)); err != nil {
		return nil, sqlError(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE unified_codes SET used_at=NOW() WHERE id IN ($1,$2)`, in.ChallengeID, in.OldChallengeID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO unified_events(account_id,site,action) VALUES($1,$2,'email')`, a.ID, in.Site); err != nil {
		return nil, err
	}
	if in.Site == "ai" {
		if _, err = tx.ExecContext(ctx, `UPDATE unified_links SET session_version=CASE WHEN session_version=9223372036854775807 THEN 0 ELSE session_version+1 END WHERE site='ai' AND account_id=$1`, a.ID); err != nil {
			return nil, err
		}
	}
	if err = s.saveOperation(ctx, tx, in, "email", payload, a.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	updated, err := s.Get(ctx, a.ID)
	if err == nil {
		s.notify(updated, []string{a.Email, email}, "通用账号邮箱已修改")
	}
	return updated, err
}

// Revoke affects only sessions belonging to the specified site.
func (s *Service) Revoke(ctx context.Context, site, localID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE unified_links SET session_version=CASE WHEN session_version=9223372036854775807 THEN 0 ELSE session_version+1 END WHERE site=$1 AND local_id=$2`, site, localID)
	return err
}

func (s *Service) SessionVersion(ctx context.Context, site, localID string) (int64, error) {
	var v int64
	err := s.DB.QueryRowContext(ctx, `SELECT session_version FROM unified_links WHERE site=$1 AND local_id=$2`, site, localID).Scan(&v)
	return v, err
}

func (s *Service) notify(a *Account, emails []string, title string) {
	for _, email := range emails {
		if s.Notify == nil || s.Notify(email, "GPTPLUSCH 账号安全提醒", "<p>"+html.EscapeString(title)+"。</p><p>两站下次密码登录将使用最新邮箱和密码；另一站已有登录状态保持不变。非本人操作请立即通过平台找回密码并联系平台客服。</p>") != nil {
			a.Notice = "账号修改成功，安全通知暂未进入发送队列，请确认本次为本人操作"
		}
	}
}

// Lookup is internal metadata only. Callers must still verify the password before login/provisioning.
func (s *Service) Lookup(ctx context.Context, email string) (*Account, error) {
	normalized, err := Email(email)
	if err != nil {
		return nil, err
	}
	a, _, err := scanAccount(s.DB.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM unified_accounts a WHERE email=$1`, normalized))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}
