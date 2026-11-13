package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnifiedOperationsUseForwardMigration(t *testing.T) {
	base, err := FS.ReadFile("250_unified_accounts.sql")
	require.NoError(t, err)
	operations, err := FS.ReadFile("251_unified_account_operations.sql")
	require.NoError(t, err)

	require.NotContains(t, string(base), "unified_operations")
	require.Contains(t, string(operations), "CREATE TABLE IF NOT EXISTS unified_operations")
	require.Contains(t, string(operations), "CREATE INDEX IF NOT EXISTS unified_operations_expiry")
}
