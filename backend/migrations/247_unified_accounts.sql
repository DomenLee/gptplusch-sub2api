-- Shared credentials only. Website sessions and business users stay independent.
CREATE TABLE IF NOT EXISTS unified_accounts (
    id UUID PRIMARY KEY,
    email VARCHAR(120) NOT NULL UNIQUE,
    alias_key VARCHAR(120) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    verified_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS unified_links (
    site TEXT NOT NULL CHECK (site IN ('store', 'ai')),
    local_id TEXT NOT NULL,
    session_version BIGINT NOT NULL DEFAULT 0,
    account_id UUID NOT NULL REFERENCES unified_accounts(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (site, local_id),
    UNIQUE (site, account_id)
);
CREATE TABLE IF NOT EXISTS unified_codes (
    id UUID PRIMARY KEY,
    email VARCHAR(120) NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN ('register', 'link', 'recover', 'email-old', 'email-new')),
    code_hash TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    operation_key TEXT,
    result_id UUID REFERENCES unified_accounts(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS unified_codes_email_time ON unified_codes(email, created_at DESC);
CREATE TABLE IF NOT EXISTS unified_attempts (
    key TEXT PRIMARY KEY,
    attempts INTEGER NOT NULL DEFAULT 1,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS unified_events (
    id BIGSERIAL PRIMARY KEY,
    account_id UUID REFERENCES unified_accounts(id),
    site TEXT NOT NULL,
    action TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
