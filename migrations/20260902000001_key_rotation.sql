-- +goose Up
-- 轮换用「一行两哈希」：换发客户端凭据时，新哈希写进 key_hash，
-- 旧哈希挪到 prev_key_hash 并带一个到期时间。
--
-- 为什么不是两行：两行共享同一把上游密钥的话，退休旧行时的 /key/block
-- 会连带封死新凭据；两行各持一把上游密钥的话，预算会在窗口期裂成两个桶、
-- 有效配额近乎翻倍——那直接破坏了「密钥与配额映射」这件事本身。
-- 一行两哈希同时保住：一个预算桶、不动吊销逻辑、密钥 ID 跨轮换稳定。
ALTER TABLE api_keys
    ADD COLUMN prev_key_hash           TEXT,
    ADD COLUMN prev_key_expires_at     TIMESTAMPTZ,
    ADD COLUMN rotated_at              TIMESTAMPTZ,
    ADD COLUMN upstream_blocked_at     TIMESTAMPTZ,
    ADD COLUMN upstream_block_attempts INTEGER NOT NULL DEFAULT 0;

-- 两列必须同生同死。少了这条约束，半填的一对会退化成两种糟糕状态之一：
-- expires_at 为空 = 旧凭据永久有效（安全事故），或孤儿到期时间。
ALTER TABLE api_keys ADD CONSTRAINT api_keys_prev_shape_check CHECK (
    (prev_key_hash IS NULL AND prev_key_expires_at IS NULL)
 OR (prev_key_hash IS NOT NULL AND prev_key_expires_at IS NOT NULL)
);

-- Edge 要按 prev_key_hash 查行，两行绝不能声称同一个哈希。
CREATE UNIQUE INDEX api_keys_prev_key_hash_idx
    ON api_keys(prev_key_hash) WHERE prev_key_hash IS NOT NULL;

-- 上游封禁兜底扫描的驱动索引。
CREATE INDEX api_keys_pending_block_idx
    ON api_keys(status) WHERE upstream_blocked_at IS NULL;

-- +goose Down
DROP INDEX api_keys_pending_block_idx;
DROP INDEX api_keys_prev_key_hash_idx;
ALTER TABLE api_keys DROP CONSTRAINT api_keys_prev_shape_check;
ALTER TABLE api_keys
    DROP COLUMN prev_key_hash,
    DROP COLUMN prev_key_expires_at,
    DROP COLUMN rotated_at,
    DROP COLUMN upstream_blocked_at,
    DROP COLUMN upstream_block_attempts;
