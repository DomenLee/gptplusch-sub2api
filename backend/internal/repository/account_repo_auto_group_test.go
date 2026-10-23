package repository

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func autoGroupRepository(t *testing.T) (*accountRepository, sqlmock.Sqlmock) {
	t.Helper()
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, database)))
	return &accountRepository{client: client, sql: database}, mock
}

func expectAutoGroupAccount(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT .*FROM "accounts".*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "type"}).AddRow(7, service.PlatformOpenAI, service.AccountTypeAPIKey))
	mock.ExpectQuery(`(?s)SELECT .*FROM "account_groups"`).WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id", "priority"}).AddRow(7, 3, 1).AddRow(7, 2, 2))
	mock.ExpectQuery(`(?s)SELECT .*FROM "groups"`).WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "rate_multiplier", "name"}).AddRow(3, service.PlatformOpenAI, 3, "自用").AddRow(2, service.PlatformOpenAI, 2, "不降智"))
}

func TestOpenAIAutoGroupRepositoryNoFallbackPreservesBindings(t *testing.T) {
	repo, mock := autoGroupRepository(t)
	expectAutoGroupAccount(mock)
	mock.ExpectQuery(`(?s)SELECT .*FROM "groups".*"auto_route_enabled".*FOR SHARE`).WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "rate_multiplier"}))
	mock.ExpectRollback()
	changed, err := repo.DowngradeOpenAIAccountGroups(context.Background(), 7, 3, service.OpenAIAutoGroupState{})
	require.ErrorContains(t, err, "no compatible active GPT groups")
	require.False(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIAutoGroupRepositoryAtomicBindingsAndOutbox(t *testing.T) {
	repo, mock := autoGroupRepository(t)
	expectAutoGroupAccount(mock)
	mock.ExpectQuery(`(?s)SELECT .*FROM "groups".*"auto_route_enabled".*FOR SHARE`).WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "rate_multiplier"}).AddRow(1, service.PlatformOpenAI, 1).AddRow(2, service.PlatformOpenAI, 2))
	mock.ExpectExec(`DELETE FROM "account_groups"`).WithArgs(int64(7), int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO "account_groups"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE accounts SET extra`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	changed, err := repo.DowngradeOpenAIAccountGroups(context.Background(), 7, 3, service.OpenAIAutoGroupState{Status: "downgraded"})
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIAutoGroupRepositorySkipsStaleTrigger(t *testing.T) {
	repo, mock := autoGroupRepository(t)
	expectAutoGroupAccount(mock)
	mock.ExpectRollback()
	changed, err := repo.DowngradeOpenAIAccountGroups(context.Background(), 7, 99, service.OpenAIAutoGroupState{})
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}
