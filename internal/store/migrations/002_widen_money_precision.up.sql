ALTER TABLE api_keys      ALTER COLUMN dollar_quota   TYPE NUMERIC(14,8);
ALTER TABLE usage_records ALTER COLUMN cost_usd       TYPE NUMERIC(14,8);
ALTER TABLE quota_totals  ALTER COLUMN total_cost_usd TYPE NUMERIC(14,8);
