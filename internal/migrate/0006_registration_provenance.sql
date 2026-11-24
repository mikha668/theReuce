ALTER TABLE product_instances
    ADD COLUMN registered_by_partner_id TEXT,
    ADD COLUMN registration_correlation_id TEXT;

ALTER TABLE product_identifiers
    ADD COLUMN registered_by_partner_id TEXT,
    ADD COLUMN registration_correlation_id TEXT;

ALTER TABLE purchase_products
    ADD COLUMN registered_at TIMESTAMPTZ,
    ADD COLUMN registered_by_partner_id TEXT,
    ADD COLUMN registration_correlation_id TEXT;

ALTER TABLE ownership_events
    ADD COLUMN registered_by_partner_id TEXT,
    ADD COLUMN registration_correlation_id TEXT;

ALTER TABLE external_resources
    ADD COLUMN registration_correlation_id TEXT;
