-- 用量记录表。Go 侧从不创建它（internal/usage 里没有任何 CREATE TABLE），
-- 所以这份脚本是它唯一的来源。全部 IF NOT EXISTS，可重复执行。
CREATE DATABASE IF NOT EXISTS airlock;

CREATE TABLE IF NOT EXISTS airlock.usage_records
(
    request_id          String,
    ts                  DateTime64(3),
    org_id              String,
    user_id             String,
    key_id              String,
    provider            LowCardinality(String),
    model               LowCardinality(String),
    input_tokens        Int64,
    cached_input_tokens Int64,
    output_tokens       Int64,
    reasoning_tokens    Int64,
    cost_micro          Int64,
    status_code         UInt16,
    latency_ms          UInt32,
    ttft_ms             UInt32,
    stream              UInt8,
    error_type          LowCardinality(String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (org_id, ts, request_id);
