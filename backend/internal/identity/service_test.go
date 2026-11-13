package identity

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"golang.org/x/crypto/bcrypt"
)

func TestRegisterCodeReplaySurvivesChallengeExpiry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	requestID := "0123456789abcdef0123456789abcdef"
	challengeID := "111e4567-e89b-12d3-a456-426614174000"
	accountID := "123e4567-e89b-12d3-a456-426614174000"
	mock.ExpectQuery("SELECT code_hash,email,purpose,attempts,expires_at,used_at,operation_key,result_id FROM unified_codes").
		WithArgs(challengeID).
		WillReturnRows(sqlmock.NewRows([]string{"code_hash", "email", "purpose", "attempts", "expires_at", "used_at", "operation_key", "result_id"}).
			AddRow("unused-hash", "user@example.test", "register", 0, now.Add(-time.Hour), sql.NullTime{Time: now, Valid: true}, sql.NullString{String: requestID, Valid: true}, sql.NullString{String: accountID, Valid: true}))

	service := &Service{}
	result, err := service.checkCode(context.Background(), tx, Input{ChallengeID: challengeID, RequestID: requestID}, "register", "user@example.test")
	if err != nil {
		t.Fatalf("expected consumed registration replay, got %v", err)
	}
	if result != accountID {
		t.Fatalf("expected account %s, got %s", accountID, result)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUnifiedOriginalResetUsesEmailProofAndOnlyRevokesAISessions(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "public-still-needs-code", true: "verified-reset-link"}[trusted], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			s := &Service{DB: db, Key: "isolated-test-key"}
			id := "123e4567-e89b-12d3-a456-426614174000"
			email := "user@example.test"
			hash, _ := bcrypt.GenerateFromPassword([]byte("OriginalPassword2026"), bcrypt.MinCost)
			rows := func(version int) *sqlmock.Rows {
				return sqlmock.NewRows([]string{"id", "email", "version", "verified_at", "password_hash"}).AddRow(id, email, version, time.Now(), string(hash))
			}
			mock.ExpectBegin()
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("SELECT action,payload_hash,account_id").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"action", "payload_hash", "account_id"}))
			mock.ExpectQuery("SELECT .* FROM unified_accounts a WHERE email=.*FOR UPDATE").WithArgs(email).WillReturnRows(rows(1))
			if !trusted {
				mock.ExpectRollback()
				_, err = s.ChangePassword(context.Background(), Input{Email: email, NewPassword: "ChangedPassword2026", RequestID: "request-without-email-code", Site: "ai"}, true)
				if err != CodeInvalid {
					t.Fatalf("public recovery without code: got %v", err)
				}
			} else {
				mock.ExpectExec("UPDATE unified_accounts SET password_hash").WithArgs(id, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("INSERT INTO unified_events").WithArgs(id, "ai", "recover").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE unified_links SET session_version=.*WHERE site='ai' AND account_id").WithArgs(id).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("DELETE FROM unified_operations").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("INSERT INTO unified_operations").WithArgs(sqlmock.AnyArg(), "ai", "recover", sqlmock.AnyArg(), id).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
				mock.ExpectQuery("SELECT .* FROM unified_accounts a WHERE a.id=").WithArgs(id).WillReturnRows(rows(2))
				a, resetErr := s.ResetVerifiedPassword(context.Background(), email, "ChangedPassword2026")
				if resetErr != nil || a.Version != 2 {
					t.Fatalf("verified reset failed: %v / %v", a, resetErr)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnifiedPasswordMinimumRemains12Characters(t *testing.T) {
	for _, password := range []string{"123456", "12345678901", string(make([]byte, 73))} {
		if err := Password(password); err == nil {
			t.Fatalf("accepted invalid password length %d", len(password))
		}
	}
	if err := Password("123456789012"); err != nil {
		t.Fatal(err)
	}
	if err := Password("这是一段十二字中文密码呀"); err != nil {
		t.Fatal(err)
	}
}
