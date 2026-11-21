CREATE TABLE parties (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    partner_id TEXT NOT NULL,
    external_id_hash BYTEA NOT NULL,
    party_type TEXT NOT NULL CHECK (party_type IN ('PERSON', 'ORGANIZATION', 'UNKNOWN')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (partner_id, external_id_hash)
);

CREATE TABLE purchase_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    partner_id TEXT NOT NULL,
    order_ref TEXT NOT NULL,
    buyer_party_id UUID NOT NULL REFERENCES parties(id),
    seller_party_id UUID NOT NULL REFERENCES parties(id),
    occurred_at TIMESTAMPTZ NOT NULL,
    payload_hash BYTEA NOT NULL,
    response_body JSONB,
    correlation_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (partner_id, order_ref)
);

CREATE TABLE product_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    manufacturer TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE product_identifiers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_instance_id UUID NOT NULL REFERENCES product_instances(id),
    scheme TEXT NOT NULL,
    value TEXT NOT NULL,
    qualifiers JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (scheme, value, qualifiers)
);

CREATE INDEX product_identifiers_product_idx ON product_identifiers (product_instance_id);

CREATE TABLE purchase_products (
    purchase_event_id UUID NOT NULL REFERENCES purchase_events(id),
    product_instance_id UUID NOT NULL REFERENCES product_instances(id),
    PRIMARY KEY (purchase_event_id, product_instance_id)
);

CREATE TABLE ownership_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_instance_id UUID NOT NULL REFERENCES product_instances(id),
    owner_party_id UUID NOT NULL REFERENCES parties(id),
    source_purchase_event_id UUID NOT NULL REFERENCES purchase_events(id),
    event_type TEXT NOT NULL CHECK (event_type IN ('PURCHASED', 'TRANSFERRED')),
    sequence BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (product_instance_id, source_purchase_event_id),
    UNIQUE (product_instance_id, sequence)
);

CREATE INDEX ownership_events_current_idx
    ON ownership_events (product_instance_id, sequence DESC);

CREATE TABLE external_resources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_instance_id UUID NOT NULL REFERENCES product_instances(id),
    resource_type TEXT NOT NULL CHECK (resource_type IN ('DPP', 'MANUAL', 'WARRANTY', 'RECEIPT', 'REPAIR', 'CERTIFICATE', 'OTHER')),
    uri TEXT NOT NULL,
    media_type TEXT,
    language TEXT,
    access_level TEXT NOT NULL CHECK (access_level IN ('PUBLIC', 'OWNER', 'ROLE_RESTRICTED')),
    resolver_type TEXT NOT NULL CHECK (resolver_type IN ('GS1_DIGITAL_LINK', 'DIRECT_HTTPS', 'STATIC_MAPPING', 'VENDOR_API')),
    source_partner_id TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX external_resources_product_idx ON external_resources (product_instance_id, active);

CREATE TABLE order_additions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    purchase_event_id UUID NOT NULL REFERENCES purchase_events(id),
    partner_id TEXT NOT NULL,
    event_ref TEXT NOT NULL,
    payload_hash BYTEA NOT NULL,
    response_body JSONB,
    correlation_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (partner_id, event_ref)
);

CREATE TABLE audit_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    partner_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id UUID NOT NULL,
    correlation_id TEXT NOT NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_entity_idx ON audit_events (entity_type, entity_id, created_at);

CREATE TABLE outbox_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    partner_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    correlation_id TEXT NOT NULL,
    payload JSONB NOT NULL,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts INTEGER NOT NULL DEFAULT 0,
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX outbox_events_pending_idx ON outbox_events (available_at, created_at)
    WHERE delivered_at IS NULL;
