package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"testing"
	"time"

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
	changed, err := repo.ProcessOpenAIAutoGroup(context.Background(), 7, "", autoGroupDowngrade)
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
	changed, err := repo.ProcessOpenAIAutoGroup(context.Background(), 7, "", autoGroupDowngrade)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIAutoGroupRepositorySkipsStaleTrigger(t *testing.T) {
	repo, mock := autoGroupRepository(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT .*FROM "accounts".*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "type", "extra"}).AddRow(7, service.PlatformOpenAI, service.AccountTypeAPIKey, `{"openai_auto_group_state":{"generation":"new"}}`))
	mock.ExpectRollback()
	changed, err := repo.ProcessOpenAIAutoGroup(context.Background(), 7, "old", func(*service.Account) (*service.OpenAIAutoGroupDecision, error) {
		t.Fatal("stale sample evaluated")
		return nil, nil
	})
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func autoGroupDowngrade(account *service.Account) (*service.OpenAIAutoGroupDecision, error) {
	return &service.OpenAIAutoGroupDecision{State: service.ReadOpenAIAutoGroupState(account.Extra), Action: "down", ProtectionMinutes: 30}, nil
}

func expectAutoGroupRecovery(mock sqlmock.Sqlmock, state service.OpenAIAutoGroupState) {
	payload, _ := json.Marshal(map[string]any{service.OpenAIAutoGroupStateExtraKey: state})
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT .*FROM "accounts".*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "type", "status", "schedulable", "extra"}).AddRow(7, service.PlatformOpenAI, service.AccountTypeAPIKey, service.StatusActive, true, string(payload)))
	mock.ExpectQuery(`(?s)SELECT .*FROM "account_groups"`).WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id", "priority"}).AddRow(7, 2, 2).AddRow(7, 1, 3))
	mock.ExpectQuery(`(?s)SELECT .*FROM "groups"`).WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "rate_multiplier"}).AddRow(2, service.PlatformOpenAI, 2).AddRow(1, service.PlatformOpenAI, 1))
}

type autoGroupSavedState struct{ state service.OpenAIAutoGroupState }

func (v *autoGroupSavedState) Match(value driver.Value) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	var extra map[string]any
	if json.Unmarshal([]byte(text), &extra) != nil {
		return false
	}
	v.state = service.ReadOpenAIAutoGroupState(extra)
	return true
}

func TestOpenAIAutoGroupRepositoryRecoveryRestoresOnlyOriginalBindings(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			repo, mock := autoGroupRepository(t)
			state := service.OpenAIAutoGroupState{Generation: "low-round", RestoreBindings: []service.OpenAIAutoGroupBinding{{ID: 3, Priority: 1}, {ID: 2, Priority: 2}}, ExpectedBindings: []service.OpenAIAutoGroupBinding{{ID: 2, Priority: 2}, {ID: 1, Priority: 3}}}
			expectAutoGroupRecovery(mock, state)
			rows := sqlmock.NewRows([]string{"id", "platform", "rate_multiplier", "status"}).AddRow(2, service.PlatformOpenAI, 2, service.StatusActive)
			if !missing {
				rows.AddRow(3, service.PlatformOpenAI, 3, service.StatusActive)
			}
			mock.ExpectQuery(`(?s)SELECT .*FROM "groups".*"auto_route_enabled".*FOR SHARE`).WillReturnRows(rows)
			saved := &autoGroupSavedState{}
			if missing {
				mock.ExpectRollback()
			} else {
				mock.ExpectExec(`DELETE FROM "account_groups"`).WithArgs(int64(7), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`INSERT INTO "account_groups"`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`UPDATE accounts SET extra`).WithArgs(saved, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			changed, err := repo.ProcessOpenAIAutoGroup(context.Background(), 7, "low-round", func(a *service.Account) (*service.OpenAIAutoGroupDecision, error) {
				return &service.OpenAIAutoGroupDecision{State: service.ReadOpenAIAutoGroupState(a.Extra), Action: "up", ProtectionMinutes: 30}, nil
			})
			if missing {
				require.ErrorContains(t, err, "original high-rate group #3")
				require.False(t, changed)
			} else {
				require.NoError(t, err)
				require.True(t, changed)
				require.Equal(t, state.RestoreBindings, saved.state.ExpectedBindings)
				require.Empty(t, saved.state.RestoreBindings)
				require.Equal(t, "upgraded", saved.state.Status)
				require.NotEqual(t, "low-round", saved.state.Generation)
				until, err := time.Parse(time.RFC3339Nano, saved.state.ProtectedUntil)
				require.NoError(t, err)
				require.WithinDuration(t, time.Now().Add(30*time.Minute), until, 3*time.Second)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAIAutoGroupRepositoryManualChangeAndProtection(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			repo, mock := autoGroupRepository(t)
			state := service.OpenAIAutoGroupState{Generation: "round", ProtectedUntil: time.Now().Add(time.Hour).Format(time.RFC3339Nano), RestoreBindings: []service.OpenAIAutoGroupBinding{{ID: 3, Priority: 1}, {ID: 2, Priority: 2}}, ExpectedBindings: []service.OpenAIAutoGroupBinding{{ID: 2, Priority: 2}, {ID: 1, Priority: 3}}}
			if manual {
				state.ExpectedBindings[0].Priority = 99
			}
			expectAutoGroupRecovery(mock, state)
			saved := &autoGroupSavedState{}
			mock.ExpectExec(`UPDATE accounts SET extra`).WithArgs(saved, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			called := false
			changed, err := repo.ProcessOpenAIAutoGroup(context.Background(), 7, "round", func(a *service.Account) (*service.OpenAIAutoGroupDecision, error) {
				called = true
				return &service.OpenAIAutoGroupDecision{State: service.ReadOpenAIAutoGroupState(a.Extra), Action: "up"}, nil
			})
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, !manual, called)
			require.Equal(t, manual, saved.state.Paused)
			require.Equal(t, state.RestoreBindings, saved.state.RestoreBindings)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAIAutoGroupRuntimeStateSurvivesStaleAccountEdits(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprint(resume), func(t *testing.T) {
			repo, mock := autoGroupRepository(t)
			state := `{"generation":"fresh","paused":true,"protected_until":"2030-01-01T00:00:00Z","restore_bindings":[{"id":3,"priority":1}]}`
			mock.ExpectQuery(`(?s)SELECT .*FOR NO KEY UPDATE`).WillReturnRows(sqlmock.NewRows(openCodeGoMergeMockColumns()).AddRow(true, false, true, nil, nil, nil, nil, nil, nil, false, nil, nil, state))
			account := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: map[string]any{service.OpenAIAutoGroupStateExtraKey: map[string]any{"generation": "stale"}, "openai_auto_group_resume": resume}}
			extra, err := lockAndMergeAccountProbeExtra(context.Background(), repo.client, account, nil, nil)
			require.NoError(t, err)
			actual := service.ReadOpenAIAutoGroupState(extra)
			require.Equal(t, "2030-01-01T00:00:00Z", actual.ProtectedUntil)
			require.True(t, actual.Paused)
			require.Equal(t, "fresh", actual.Generation)
			require.Len(t, actual.RestoreBindings, 1)

			require.NotContains(t, extra, "openai_auto_group_resume")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAIAutoGroupRepositorySkipsBoundOrRestoreGroups(t *testing.T) {
	for _, recovering := range []bool{false, true} {
		t.Run(fmt.Sprint(recovering), func(t *testing.T) {
			repo, mock := autoGroupRepository(t)
			if recovering {
				expectAutoGroupRecovery(mock, service.OpenAIAutoGroupState{RestoreBindings: []service.OpenAIAutoGroupBinding{{ID: 3, Priority: 1}}, ExpectedBindings: []service.OpenAIAutoGroupBinding{{ID: 2, Priority: 2}, {ID: 1, Priority: 3}}})
			} else {
				expectAutoGroupAccount(mock)
			}
			mock.ExpectRollback()
			changed, err := repo.ProcessOpenAIAutoGroup(context.Background(), 7, "", func(account *service.Account) (*service.OpenAIAutoGroupDecision, error) {
				action := "down"
				if recovering {
					action = "up"
				}
				return &service.OpenAIAutoGroupDecision{State: service.ReadOpenAIAutoGroupState(account.Extra), Action: action, SkipGroupIDs: []int64{3}}, nil
			})
			require.NoError(t, err)
			require.False(t, changed)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAIAutoGroupRepositoryFiltersSkippedTargets(t *testing.T) {
	for _, onlySkipped := range []bool{false, true} {
		t.Run(fmt.Sprint(onlySkipped), func(t *testing.T) {
			repo, mock := autoGroupRepository(t)
			expectAutoGroupAccount(mock)
			rows := sqlmock.NewRows([]string{"id", "platform", "rate_multiplier"}).AddRow(1, service.PlatformOpenAI, 1)
			if !onlySkipped {
				rows.AddRow(2, service.PlatformOpenAI, 2)
			}
			mock.ExpectQuery(`(?s)SELECT .*FROM "groups".*"auto_route_enabled".*FOR SHARE`).WillReturnRows(rows)
			saved := &autoGroupSavedState{}
			if onlySkipped {
				mock.ExpectRollback()
			} else {
				mock.ExpectExec(`DELETE FROM "account_groups"`).WithArgs(int64(7), int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`UPDATE accounts SET extra`).WithArgs(saved, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			changed, err := repo.ProcessOpenAIAutoGroup(context.Background(), 7, "", func(account *service.Account) (*service.OpenAIAutoGroupDecision, error) {
				return &service.OpenAIAutoGroupDecision{State: service.ReadOpenAIAutoGroupState(account.Extra), Action: "down", SkipGroupIDs: []int64{1}}, nil
			})
			if onlySkipped {
				require.ErrorContains(t, err, "skip_group_ids")
				require.False(t, changed)
			} else {
				require.NoError(t, err)
				require.True(t, changed)
				require.Equal(t, []int64{2}, saved.state.ToGroupIDs)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpenAIAutoGroupResumeOnlyUpdatesManagedState(t *testing.T) {
	repo, mock := autoGroupRepository(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT .*FROM "accounts".*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "type", "extra"}).AddRow(7, service.PlatformOpenAI, service.AccountTypeAPIKey, `{"quota_daily_limit":50,"custom":"keep","openai_auto_group_state":{"paused":true,"generation":"old","protected_until":"2030-01-01T00:00:00Z","restore_bindings":[{"id":3,"priority":1}]}}`))
	saved := &autoGroupSavedState{}
	mock.ExpectExec(`UPDATE accounts SET extra = COALESCE\(extra, '\{\}'::jsonb\) \|\|`).WithArgs(saved, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.ResumeOpenAIAutoGroup(context.Background(), 7))
	require.False(t, saved.state.Paused)
	require.Empty(t, saved.state.RestoreBindings)
	require.Equal(t, "2030-01-01T00:00:00Z", saved.state.ProtectedUntil)
	require.NotEqual(t, "old", saved.state.Generation)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIAutoGroupUnchangedBindingsPreserveCustomPriority(t *testing.T) {
	repo, mock := autoGroupRepository(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT .*FROM "accounts".*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "extra"}).AddRow(7, service.PlatformOpenAI, `{"openai_auto_group_state":{"status":"downgraded"}}`))
	mock.ExpectQuery(`(?s)SELECT .*FROM "account_groups".*ORDER BY`).WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id", "priority"}).AddRow(7, 2, 20).AddRow(7, 3, 50))
	mock.ExpectRollback()
	require.NoError(t, repo.BindGroups(context.Background(), 7, []int64{2, 3}))
	require.NoError(t, mock.ExpectationsWereMet())
}
