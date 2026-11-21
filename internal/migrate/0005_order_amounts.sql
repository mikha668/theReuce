ALTER TABLE purchase_events
    ADD COLUMN currency TEXT,
    ADD COLUMN total_amount NUMERIC(19,4),
    ADD COLUMN vat_amount NUMERIC(19,4),
    ADD CONSTRAINT purchase_amounts_check CHECK (
        (currency IS NULL AND total_amount IS NULL AND vat_amount IS NULL) OR
        (currency IS NOT NULL AND currency ~ '^[A-Z]{3}$' AND total_amount IS NOT NULL AND vat_amount IS NOT NULL
         AND total_amount >= 0 AND vat_amount >= 0 AND vat_amount <= total_amount)
    );

ALTER TABLE purchase_products
    ADD COLUMN total_amount NUMERIC(19,4),
    ADD COLUMN vat_amount NUMERIC(19,4),
    ADD CONSTRAINT purchase_product_amounts_check CHECK (
        (total_amount IS NULL AND vat_amount IS NULL) OR
        (total_amount IS NOT NULL AND vat_amount IS NOT NULL
         AND total_amount >= 0 AND vat_amount >= 0 AND vat_amount <= total_amount)
    );
