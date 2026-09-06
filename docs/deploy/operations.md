# Airlock 运维手册

装完 Airlock 之后，日常维护、备份、升级与故障排查都在这份文档里。所有命令都假定你的当前目录是解压出来的交付包目录（`.env`、`docker-compose.yml` 所在的地方）。

---

## §1 日常操作

- **看整体状态**：

  ```bash
  docker compose ps
  ```

  健康的系统里六个服务（`postgres`、`clickhouse`、`litellm`、`casdoor`、`airlock-control`、`airlock-edge`）应全部是 `healthy` 或 `running`。

- **看某个服务的日志**（把服务名换成你要看的那个）：

  ```bash
  docker compose logs -f airlock-control
  ```

- **重启单个服务**（例如改完 `.env` 后让配置生效）：

  ```bash
  docker compose restart airlock-control
  ```

- **停止全部服务**（**不会删除数据卷**，数据仍在，随时可以 `up -d` 拉起）：

  ```bash
  docker compose down
  ```

- **查当前部署的版本号**：

  ```bash
  docker compose exec airlock-control airlock version
  ```

---

## §2 备份与恢复

### 备份

Postgres 保存了组织架构、成员、密钥元数据（不含明文密钥）、审批记录等控制面数据：

```bash
docker compose exec -T postgres pg_dumpall -U airlock > airlock-pg-$(date +%F).sql
```

ClickHouse 保存了用量与成本明细，数据存在 `chdata` 这个 Docker 卷里。备份整卷：

```bash
docker run --rm \
  -v release_chdata:/var/lib/clickhouse \
  -v "$(pwd)":/backup \
  alpine tar czf /backup/ch-$(date +%F).tar.gz /var/lib/clickhouse
```

（卷名可能因 compose 项目名不同而有前缀差异，用 `docker volume ls | grep chdata` 确认实际名称。）

**必须与数据库一起备份的东西：`.env` 里的 `AIRLOCK_ENCRYPTION_KEY`。** 这个密钥用来加密每一把虚拟密钥对应的上游凭据。丢了它，数据库里保存的所有已签发密钥的上游凭据都无法解密，等同于全部作废——所有客户端都要重新签发密钥。备份 `.env`（或至少这一行）与备份数据库同等重要。

### 恢复

```bash
# 1. 起一个空的 postgres/clickhouse（不启动 control/edge）
docker compose up -d postgres clickhouse

# 2. 恢复 Postgres
docker compose exec -T postgres psql -U airlock -d postgres < airlock-pg-<日期>.sql

# 3. 恢复 ClickHouse 卷（先停容器，替换卷内容，再起）
docker compose stop clickhouse
docker run --rm -v release_chdata:/var/lib/clickhouse -v "$(pwd)":/backup \
  alpine sh -c "rm -rf /var/lib/clickhouse/* && tar xzf /backup/ch-<日期>.tar.gz -C /"
docker compose start clickhouse

# 4. 确认 .env 里的 AIRLOCK_ENCRYPTION_KEY 与备份数据库时使用的一致

# 5. 起齐全部服务
docker compose up -d
```

---

## §3 升级

1. 按 §2 做一次完整备份。
2. 拿到新版交付包，解压到**新目录**（不要覆盖当前正在运行的目录）。
3. 把旧目录的 `.env` 拷贝到新目录：

   ```bash
   cp <旧目录>/.env <新目录>/.env
   ```

4. 停掉旧版本，在新目录运行安装脚本：

   ```bash
   cd <旧目录> && docker compose down
   cd <新目录> && ./install.sh
   ```

   `install.sh` 检测到 `.env` 已存在会保留不动，只会把版本号同步成新交付包里 `VERSION` 记录的值，然后走完起栈、schema 初始化与自检。

5. 按部署文档 §6 的验证清单确认新版本工作正常。

6. **回滚**：如果新版本有问题，停掉新版本、回到旧目录重新拉起即可，数据卷没有被新版本动过：

   ```bash
   cd <新目录> && docker compose down
   cd <旧目录> && docker compose up -d
   ```

---

## §4 授权管理

- **续期**：拿到供应方签发的新 license 文件，覆盖 `.env` 里 `LICENSE_HOST_PATH` 指向的那个文件，然后：

  ```bash
  docker compose restart airlock-control
  ```

- **查看当前授权状态**：控制台顶部的横幅会直接显示；也可以调接口查看（需要带上有效的登录会话）：

  ```bash
  curl -b "airlock_session=<会话 cookie>" http://localhost:8081/api/license
  ```

- **席位用尽**：新成员登录会被拒绝，提示「席位已满」。到控制台里把一个不再使用的账号置为停用，即可立刻腾出一个席位；或联系供应方购买更多席位并重新签发 license。

- **过期后的行为**：**AI 调用完全不受影响**，数据面 `airlock-edge` 不检查授权状态。受影响的只是管理面的写操作（建组织、签发密钥、审批等）会返回 402；查询类接口与密钥吊销操作**仍然可用**——吊销是止损动作，不应该被授权状态卡住。

---

## §5 密钥吊销

密钥吊销在控制台的「虚拟密钥」页面完成，有三种粒度：

- **单把吊销**：密钥列表里对着某一把点「吊销」，只影响这一把。
- **按组织子树批量吊销**：在组织节点上执行，会吊销该节点及其所有子节点下的全部密钥。用于成员离职、团队解散等场景。
- **全局吊销**：吊销系统内全部密钥，只有持有 `key:revoke_all` 权限的账号能操作，用于怀疑主密钥或大范围凭据泄漏时的应急止损。

三种吊销操作在授权过期后**均可正常使用**（见 §4）。

---

## §6 邮件通知

`.env` 里的 `SMTP_ADDR` 留空时，Airlock **不会发送任何审批通知邮件**——这是设计好的行为，不是故障。此时审批流程仍然完整可用，审批人从控制台的「待我审批」列表里直接处理即可，只是不会收到邮件提醒。

日志里会看到这样一行，确认系统知道邮件被禁用了：

```
邮件通知未启用，跳过投递
```

需要邮件通知时，在 `.env` 里把 `SMTP_ADDR` 填成企业内网的 SMTP relay 地址（形如 `smtp.example.com:25`），重启控制台生效：

```bash
docker compose restart airlock-control
```

一个需要了解的细节：无论邮件是否真的发出，数据库里 `notifications` 表对应记录的 `status` 都会被标成 `sent`。这个字段的含义是「已交给通知渠道处理」，而在 `SMTP_ADDR` 为空时，当前生效的渠道就是「不发送」——所以看到 `sent` 不代表邮件真的到了收件人手上，请以 `SMTP_ADDR` 是否配置为准判断。

---

## §7 故障排查表

| 现象 | 原因 | 动作 |
|---|---|---|
| 容器启动即退出，日志有 `exec format error` | 交付包架构与机器不符 | `uname -m` 对照包内 `VERSION` 的 `ARCH=`；向供应方索取对应架构的包 |
| `docker compose up` 立刻报镜像不存在 | `images.tar` 未导入或导入不全 | 重跑 `./install.sh`（第 2 步会重新导入，无副作用） |
| control 启动失败：`授权文件校验失败` | license 文件损坏或被改过 | 向供应方索取完好的 license；不要手工编辑该文件 |
| control 启动失败：`未配置 AIRLOCK_ENCRYPTION_KEY` | `.env` 里该项为空 | 由 `install.sh` 自动生成；若手工清空过，**不能随便重新生成**——换密钥会导致已签发密钥无法解密 |
| control 启动失败：无管理员且 bootstrap 为空 | `AIRLOCK_BOOTSTRAP_ADMIN` 没填 | 在 `.env` 里填首个管理员的 email，重启 control |
| control 启动失败：`OIDC discovery 失败 ... connection refused` | `OIDC_DISCOVERY_URL` 被误改了 | 改回 `http://casdoor:8000`（compose 内部服务名 + 容器内部端口）；这一项不跟 `CASDOOR_ORIGIN`/`OIDC_ISSUER` 走，见部署文档 §3 |
| control 启动失败：`issuer URL provided ... did not match` | `OIDC_ISSUER` 与 `CASDOOR_ORIGIN` 不一致 | 让两项完全相同，见部署文档 §3 |
| 登录时报 `x509: certificate signed by unknown authority` | 客户 IdP 用内部 CA 签的证书 | 把 CA 证书挂进 airlock-control 容器的 `/etc/ssl/certs/` 并重启 |
| 登录后跳回登录页 / 回调失败 | `OIDC_REDIRECT_URL` 与浏览器实际访问的地址不一致 | 两者必须完全一致，含协议、主机名与端口 |
| 控制台能开但数据面调用报上游 401 | 供应商密钥未配或无效 | 检查 `.env` 里的 `DEEPSEEK_API_KEY` 等，改完 `docker compose restart litellm` |
| 用量与成本页面空白 | `airlock.usage_records` 表不存在 | 重跑 `./install.sh`（第 5 步幂等）；确认 `CLICKHOUSE_DSN` 已配置 |
| casdoor 容器不断重启，日志有 `panic: pq: database "casdoor" does not exist` | `litellm`/`casdoor` 数据库没建出来 | 用官方 `install.sh` 全流程安装（它会在起 casdoor 之前先建库）；若是手工分步操作导致的，手动执行一次 `docker compose exec -T postgres psql -U <用户> -d postgres < schema/postgres-init.sql` 再重启 casdoor |
| 控制台顶部红条，写操作返回 402 | 授权已过期 | 换新 license 后 `docker compose restart airlock-control`；AI 调用不受影响 |
| 新成员登录报「席位已满」 | 席位用尽 | 停用离职成员腾出席位，或联系供应方扩容 |
| 审批邮件没收到 | 未配置 `SMTP_ADDR` | 预期行为，见 §6。需要发信就填内网 relay 后重启 control |
| 端口冲突，compose 起不来 | 8080/8081/8000 被占用 | 改 `.env` 里的 `CONTROL_PORT` / `EDGE_PORT` / `CASDOOR_PORT`；改了 `CASDOOR_PORT` 要同步改 `CASDOOR_ORIGIN` 与 `OIDC_*` |

---

## §8 收集诊断信息

联系供应方排查问题前，请准备好以下信息，能大幅缩短排查时间：

- 交付包目录下的 `VERSION` 文件完整内容（版本号、架构、各镜像 digest）
- `docker compose ps` 的完整输出
- 出问题的服务最近的日志：`docker compose logs --tail=200 <服务名>`
- `.env` 的内容，**但务必先去掉所有口令与密钥的实际值**（`POSTGRES_PASSWORD`、`CLICKHOUSE_PASSWORD`、`AIRLOCK_ENCRYPTION_KEY`、`LITELLM_MASTER_KEY`、各供应商 API_KEY、`OIDC_CLIENT_SECRET`）——只需要让对方知道这些项「已填」还是「为空」，不需要暴露实际值
