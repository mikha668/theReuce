CREATE TABLE party_profile_aliases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    party_profile_id UUID NOT NULL REFERENCES party_profiles(id) ON DELETE CASCADE,
    alias TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO party_profile_aliases (party_profile_id, alias, created_at)
SELECT party_profile_id, alias, min(created_at)
FROM party_aliases
GROUP BY party_profile_id, alias;
