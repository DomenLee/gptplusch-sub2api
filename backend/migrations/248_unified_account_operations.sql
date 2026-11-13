CREATE TABLE IF NOT EXISTS unified_operations (
    request_key TEXT PRIMARY KEY,
    site TEXT NOT NULL CHECK (site IN ('store', 'ai')),
    action TEXT NOT NULL CHECK (action IN ('password', 'recover', 'email')),
    payload_hash TEXT NOT NULL,
    account_id UUID NOT NULL REFERENCES unified_accounts(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS unified_operations_expiry ON unified_operations(expires_at);
