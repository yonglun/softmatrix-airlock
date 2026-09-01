-- +goose Up
-- Key 级配额与模型白名单。存的是「签发时的意图」，执行方始终是 LiteLLM。
--
-- models 默认 '[]' 与 LiteLLM 的语义一致：空数组表示放行全部模型。
-- 这是 fail-open 的，因此 API 边界强制要求显式传 models 字段（见 keyapi.go）。
ALTER TABLE api_keys
    ADD COLUMN models          JSONB   NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN max_budget      NUMERIC,
    ADD COLUMN budget_duration TEXT,
    ADD COLUMN rpm_limit       INTEGER,
    ADD COLUMN tpm_limit       INTEGER;

-- pending 表示「本地已登记、上游尚未建成」。
-- 它必须先于上游调用写入，这样「上游有、本地无」的无主凭据在结构上不可能出现。
ALTER TABLE api_keys DROP CONSTRAINT api_keys_status_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_status_check
    CHECK (status IN ('pending', 'active', 'revoked'));

-- +goose Down
DELETE FROM api_keys WHERE status = 'pending';
ALTER TABLE api_keys DROP CONSTRAINT api_keys_status_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_status_check
    CHECK (status IN ('active', 'revoked'));
ALTER TABLE api_keys
    DROP COLUMN models,
    DROP COLUMN max_budget,
    DROP COLUMN budget_duration,
    DROP COLUMN rpm_limit,
    DROP COLUMN tpm_limit;
