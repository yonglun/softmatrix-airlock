-- +goose Up
CREATE TABLE organizations (
    id          TEXT PRIMARY KEY,
    parent_id   TEXT REFERENCES organizations(id) ON DELETE RESTRICT,
    name        TEXT NOT NULL,
    -- path 是从根到本节点的 id 序列，用 / 分隔，便于按前缀查子树。
    path        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX organizations_parent_idx ON organizations(parent_id);
CREATE INDEX organizations_path_idx ON organizations(path text_pattern_ops);

CREATE TABLE api_keys (
    id                    TEXT PRIMARY KEY,
    key_hash              TEXT NOT NULL UNIQUE,
    key_prefix            TEXT NOT NULL,
    org_id                TEXT NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    user_id               TEXT NOT NULL,
    name                  TEXT NOT NULL DEFAULT '',
    -- 上游 LiteLLM 密钥，AES-256-GCM 加密后的 base64
    upstream_key_enc      TEXT NOT NULL,
    status                TEXT NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active', 'revoked')),
    expires_at            TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX api_keys_org_idx ON api_keys(org_id);

CREATE TABLE model_prices (
    id                 TEXT PRIMARY KEY,
    provider           TEXT NOT NULL,
    model              TEXT NOT NULL,
    effective_from     TIMESTAMPTZ NOT NULL,
    currency           TEXT NOT NULL DEFAULT 'CNY',
    -- tiers 是 pricing.Tier 的 JSON 数组，按 max_input_tokens 升序。
    -- 金额单位为 Micro（1e-6 元）。
    tiers              JSONB NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, model, effective_from)
);

CREATE INDEX model_prices_lookup_idx ON model_prices(provider, model, effective_from DESC);

-- +goose Down
DROP TABLE model_prices;
DROP TABLE api_keys;
DROP TABLE organizations;
