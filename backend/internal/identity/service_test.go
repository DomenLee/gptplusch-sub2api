package identity

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
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
