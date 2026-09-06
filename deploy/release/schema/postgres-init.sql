-- 建 LiteLLM 与 Casdoor 各自的库。
--
-- 不放 docker-entrypoint-initdb.d：那个钩子只在数据卷为空的首次启动时
-- 执行，安装失败后重跑（卷还在）就会被静默跳过，是最难查的一类故障。
-- 改由 install.sh 显式执行，因此这里必须幂等。
--
-- Postgres 的 CREATE DATABASE 没有 IF NOT EXISTS，也不能放进 DO 块
-- （CREATE DATABASE 不能在事务里跑），所以用 \gexec：先 SELECT 出需要
-- 执行的语句，再由 psql 逐条执行；已存在时 SELECT 不返回行，什么都不做。
SELECT 'CREATE DATABASE litellm'
 WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'litellm')\gexec

SELECT 'CREATE DATABASE casdoor'
 WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'casdoor')\gexec
