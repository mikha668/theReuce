CREATE TABLE IF NOT EXISTS party_profiles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    party_type TEXT NOT NULL CHECK (party_type IN ('PERSON', 'ORGANIZATION', 'UNKNOWN')),
    display_id TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS party_aliases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    party_profile_id UUID NOT NULL REFERENCES party_profiles(id) ON DELETE CASCADE,
    partner_id TEXT NOT NULL,
    alias TEXT NOT NULL,
    external_id_hash BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (partner_id, alias)
);
