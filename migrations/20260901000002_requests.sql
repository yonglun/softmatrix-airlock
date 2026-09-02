-- +goose Up
-- 一张表承载两类申请。审批语义（谁能批、状态流转、审计留痕）两者完全相同，
-- 是逻辑的大头；用 CHECK 约束而不是 JSONB payload，保住数据库级的形状校验。
--
-- 这张表本身就是这条流程的审计轨迹（FR-3.6 要求申请、审批、回收全程可审计）：
-- 谁申请、谁审批、何时、执行结果、何时回收，全在行里。
CREATE TABLE requests (
    id              TEXT PRIMARY KEY,
    kind            TEXT NOT NULL CHECK (kind IN ('new_key','quota_bump')),
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','rejected','approved','executed','failed')),
    requester_id    TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    org_id          TEXT NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    reason          TEXT NOT NULL DEFAULT '',

    key_name        TEXT,
    models          JSONB,

    target_key_id   TEXT REFERENCES api_keys(id) ON DELETE RESTRICT,
    bump_to_budget  NUMERIC,
    bump_expires_at TIMESTAMPTZ,
    prev_budget     NUMERIC,
    reclaimed_at    TIMESTAMPTZ,

    decided_by      TEXT REFERENCES users(id) ON DELETE RESTRICT,
    decided_at      TIMESTAMPTZ,
    executed_at     TIMESTAMPTZ,
    issued_key_id   TEXT REFERENCES api_keys(id) ON DELETE RESTRICT,
    last_error      TEXT,
    attempts        INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT requests_kind_shape_check CHECK (
      (kind = 'new_key'    AND key_name IS NOT NULL AND models IS NOT NULL
                           AND target_key_id IS NULL AND bump_to_budget IS NULL
                           AND bump_expires_at IS NULL)
   OR (kind = 'quota_bump' AND target_key_id IS NOT NULL AND bump_to_budget IS NOT NULL
                           AND bump_expires_at IS NOT NULL
                           AND key_name IS NULL AND models IS NULL)
    )
);

CREATE INDEX requests_org_idx ON requests(org_id);
CREATE INDEX requests_requester_idx ON requests(requester_id);
CREATE INDEX requests_status_idx ON requests(status);

-- 通知走 outbox：审批事务里只写一条待发记录，投递与重试交给 worker。
-- 在 P1.4 控制台出来之前，通知是这条流程唯一的可用性来源，
-- 尽力发送会让审批人根本不知道有待审申请。
CREATE TABLE notifications (
    id          TEXT PRIMARY KEY,
    request_id  TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
    event       TEXT NOT NULL
                CHECK (event IN ('submitted','approved','rejected','executed','reclaimed')),
    -- 只允许 email：钉钉/企微拿不到真实租户实测之前，
    -- 系统在物理上就存不下一条声称由它们投递的记录。
    channel     TEXT NOT NULL DEFAULT 'email'
                CONSTRAINT notifications_channel_check CHECK (channel IN ('email')),
    recipient   TEXT NOT NULL,
    subject     TEXT NOT NULL,
    body        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','sent','failed')),
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT,
    sent_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX notifications_pending_idx ON notifications(status, created_at);

-- +goose Down
DROP TABLE notifications;
DROP TABLE requests;
