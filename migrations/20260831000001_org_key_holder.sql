-- +goose Up
-- 标记「这个节点是密钥与预算边界」，它决定该节点是否在 LiteLLM 侧对应一个 Team。
--
-- 为什么不用「是不是叶子节点」当判据：给一个叶子加子节点会让它不再是叶子，
-- 同步逻辑就被迫删掉它已经存在的 Team，而那个 Team 上完全可能绑着在用的 Key
-- （api_keys.org_id 并不限制只能挂叶子）。组织扩张是最正常的操作，
-- 不该炸掉线上密钥。
ALTER TABLE organizations
    ADD COLUMN is_key_holder BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE organizations DROP COLUMN is_key_holder;
