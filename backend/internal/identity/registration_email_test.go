package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestUnifiedStoreRegistrationEmailBeforeSending(t *testing.T) {
	blocked := errors.New("email type rejected")
	s := &Service{CheckRegistrationEmail: func(_ context.Context, email string) error {
		require.Equal(t, "user@blocked.test", email)
		return blocked
	}}
	// No database or mail sender is configured: rejection must precede both.
	_, err := s.SendCode(context.Background(), Input{Email: " User@Blocked.test ", Site: "store", Purpose: "register"})
	require.ErrorIs(t, err, blocked)
}

func TestUnifiedStoreRegistrationEmailDoesNotAffectOtherCodes(t *testing.T) {
	for _, in := range []Input{
		{Site: "store", Purpose: "recover"},
		{Site: "store", Purpose: "link"},
		{Site: "store", Purpose: "email-old"},
		{Site: "store", Purpose: "email-new"},
		{Site: "ai", Purpose: "register"},
	} {
		t.Run(in.Site+"/"+in.Purpose, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			reachedRate := errors.New("original send path reached")
			mock.ExpectQuery("INSERT INTO unified_attempts").WillReturnError(reachedRate)
			s := &Service{DB: db, CheckRegistrationEmail: func(context.Context, string) error {
				t.Fatal("registration-only policy applied to another operation")
				return nil
			}}
			in.Email = "user@blocked.test"
			_, err = s.SendCode(context.Background(), in)
			require.ErrorIs(t, err, reachedRate)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUnifiedStoreRegistrationEmailRecheckedBeforeCreate(t *testing.T) {
	for _, state := range []string{"allowed", "policy-changed", "completed-retry"} {
		t.Run(state, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			blocked := errors.New("email type rejected after code was issued")
			calls := 0
			s := &Service{DB: db, Key: "local-only-test-key", Alias: func(email string) string { return email },
				CheckRegistrationEmail: func(_ context.Context, email string) error {
					calls++
					require.Equal(t, "user@example.test", email)
					if state != "allowed" {
						return blocked
					}
					return nil
				}}
			in := Input{Email: "User@Example.test", Password: "Password2026", Site: "store", Code: "123456",
				ChallengeID: "111e4567-e89b-12d3-a456-426614174000", RequestID: "test-registration-request"}
			id := "123e4567-e89b-12d3-a456-426614174000"
			hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.MinCost)
			require.NoError(t, err)
			accountRows := func() *sqlmock.Rows {
				return sqlmock.NewRows([]string{"id", "email", "version", "verified_at", "password_hash"}).
					AddRow(id, "user@example.test", 1, time.Now(), string(hash))
			}
			mock.ExpectBegin()
			codeRows := sqlmock.NewRows([]string{"code_hash", "email", "purpose", "attempts", "expires_at", "used_at", "operation_key", "result_id"})
			if state == "completed-retry" {
				codeRows.AddRow(s.digest(in.ChallengeID+":"+in.Code), "user@example.test", "register", 0, time.Now().Add(-time.Hour), time.Now(), in.RequestID, id)
			} else {
				codeRows.AddRow(s.digest(in.ChallengeID+":"+in.Code), "user@example.test", "register", 0, time.Now().Add(time.Minute), nil, nil, nil)
			}
			mock.ExpectQuery("SELECT code_hash,email,purpose").WithArgs(in.ChallengeID).WillReturnRows(codeRows)
			switch state {
			case "allowed":
				mock.ExpectExec("INSERT INTO unified_accounts").WithArgs(sqlmock.AnyArg(), "user@example.test", "user@example.test", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE unified_codes SET used_at").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("INSERT INTO unified_events").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
				mock.ExpectQuery("SELECT .* FROM unified_accounts a WHERE a.id=").WillReturnRows(accountRows())
			case "policy-changed":
				mock.ExpectRollback()
			case "completed-retry":
				mock.ExpectRollback()
				mock.ExpectQuery("INSERT INTO unified_attempts").WillReturnRows(sqlmock.NewRows([]string{"attempts"}).AddRow(1))
				mock.ExpectQuery("SELECT .* FROM unified_accounts a WHERE email=").WithArgs("user@example.test").WillReturnRows(accountRows())
			}
			a, err := s.Register(context.Background(), in)
			if state == "policy-changed" {
				require.ErrorIs(t, err, blocked)
				require.Nil(t, a)
			} else {
				require.NoError(t, err)
				require.Equal(t, id, a.ID)
			}
			if state == "completed-retry" {
				require.Zero(t, calls, "completed registrations are not treated as new registrations")
			} else {
				require.Equal(t, 1, calls)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
