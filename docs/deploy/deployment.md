# Airlock 部署文档

本文档指导你在一台**完全无外网**的机器上从零装起 Airlock。全程离线，不需要任何网络连接。

---

## §1 前置要求

| 项 | 要求 |
|---|---|
| 操作系统 | Linux x86_64（amd64 包）或 aarch64（arm64 包）；内核 3.10+ |
| Docker | Engine 20.10 或更高，含 compose v2 插件（`docker compose version` 能正常输出） |
| CPU / 内存 | 最低 4 核 8 GB，推荐 8 核 16 GB |
| 磁盘 | 至少 20 GB 可用（镜像约 3 GB，其余留给数据库与用量数据增长） |
| 端口 | 8080（数据面）、8081（控制台）、8000（身份提供方）需对使用者放通 |
| 外网 | **不需要**。全程离线安装，只用交付包里带的镜像 |

交付包按目标架构区分（`airlock-<版本>-linux-amd64.tar.gz` 或 `-arm64.tar.gz`）。装错架构会在容器启动时报 `exec format error`——`install.sh` 的第一步会先检查这一点并挡住你，见 §7。

---

## §2 安装步骤

1. 解压交付包并进入解压出的目录：

   ```bash
   tar -xzf airlock-<版本>-linux-amd64.tar.gz
   cd airlock-<版本>-linux-amd64
   ```

2. 首次运行安装脚本：

   ```bash
   ./install.sh
   ```

   脚本会依次走「前置检查 → 导入镜像 → 生成配置」三步。第 3 步会自动生成随机的数据库口令与加密密钥，然后**停下来**，列出还差哪几项必须人工填写（见 §3），退出码为 3。这是预期行为，不是失败。

3. 用文本编辑器打开当前目录下的 `.env`，补齐 §3 列出的必填项。

4. 再次运行 `./install.sh`。这次脚本会走完全部五步：前置检查 → 导入镜像 → 生成配置 → 起栈等健康 → 初始化 schema 并自检。全部完成后打印控制台地址。

5. 按脚本末尾的提示，去配置身份提供方（见 §4）。

安装脚本可以安全地重复执行：已生成的 `.env` 不会被覆盖，schema 初始化是幂等的，重复导入镜像没有副作用。遇到任何一步失败，脚本会打印原因和「该怎么办」，修正后直接重跑即可，不需要清理任何状态。

---

## §3 必须人工填写的配置

`.env` 里以下几项在首次生成时是空的，必须手工填写：

- **`AIRLOCK_BOOTSTRAP_ADMIN`**：首个平台管理员的 email 或 OIDC subject。系统内还没有任何管理员、且这一项为空时，`airlock-control` 会拒绝启动——这是刻意设计的安全闸门，不允许出现「谁都能登、登进去就是管理员」的窗口期。填一个你打算用来登录的账号的 email。

- **`OIDC_CLIENT_SECRET`**：Casdoor 里 `airlock` 应用的客户端密钥。这一项要等你在 §4 里建好应用之后才能拿到，属于「先跑一遍脚本卡在这里、去 Casdoor 建完应用再回来填」的正常流程。

- **`CASDOOR_ORIGIN`** 与 **`OIDC_REDIRECT_URL`**：**必须是浏览器实际能访问到的地址**，不能是 `localhost`（除非你确实就在这台服务器本机开浏览器）。这是最常见的配置错误——如果这台机器有一个外部可访问的主机名或 IP（比如 `airlock.example.com` 或 `10.0.1.5`），这两项就要用它，形如：

  ```
  CASDOOR_ORIGIN=http://airlock.example.com:8000
  OIDC_REDIRECT_URL=http://airlock.example.com:8081/auth/callback
  ```

  写错这两项的典型症状是「登录后又跳回登录页」或「OIDC 回调失败」。

- **至少一个模型供应商密钥**：`DEEPSEEK_API_KEY`、`DASHSCOPE_API_KEY`、`OPENAI_API_KEY` 三选一。没有任何一个，所有模型调用都会拿到上游 401。

---

## §4 配置身份提供方

Airlock 自带 Casdoor 作为默认身份提供方，需要在其中创建一个 OIDC 应用：

1. 浏览器打开 `http://<CASDOOR_ORIGIN 里的地址>:8000`，用默认账号 `admin` / `123` 登录。**登录后第一件事是修改这个默认口令**（Casdoor 管理界面里 Users → admin → 修改密码）。

2. 在 Casdoor 管理界面新建一个 Application，名称填 `airlock`。

3. 该 Application 的 Redirect URL 填 `.env` 里 `OIDC_REDIRECT_URL` 的值（例如 `http://airlock.example.com:8081/auth/callback`）。

4. 保存后 Casdoor 会生成一个 Client ID 与 Client Secret。把 Client Secret 回填进 `.env` 的 `OIDC_CLIENT_SECRET`，然后重启控制台使其生效：

   ```bash
   docker compose restart airlock-control
   ```

5. 在 Casdoor 里建首批用户。其中至少一个用户的 email，必须与 `.env` 里的 `AIRLOCK_BOOTSTRAP_ADMIN` 完全一致——这个人首次登录 Airlock 控制台时会被自动授予平台管理员角色。

---

## §5 投放授权文件

不配置授权文件时，Airlock 以**试用模式**运行：5 个席位、不设到期时间，控制台顶部会有橙色提示条。这足够先把系统跑起来验证功能，但不适合生产使用。

拿到供应方签发的正式 license 文件后：

1. 把该文件放到这台主机上的任意路径，例如 `/etc/airlock/license.txt`。
2. 编辑 `.env`：

   ```
   LICENSE_HOST_PATH=/etc/airlock/license.txt
   AIRLOCK_LICENSE_FILE=/etc/airlock/license.txt
   ```

   这两项必须同时填写——只填一项会导致控制台启动失败，`install.sh` 的配置检查会在这一点上拦住你并提示修正。

3. 重启控制台使其生效：

   ```bash
   docker compose restart airlock-control
   ```

授权到期后，管理类写操作会暂停（控制台顶部变红条），但**查询、密钥吊销与所有 AI 调用不受影响**。详见运维手册「授权管理」一节。

---

## §6 验证清单

装完之后，逐条确认以下几点，确保整套系统真的可用：

- [ ] `docker compose ps` 显示六个服务（`postgres`、`clickhouse`、`litellm`、`casdoor`、`airlock-control`、`airlock-edge`）均为 `healthy` 或 `running` 状态
- [ ] 浏览器打开 `http://<主机>:8081`，能自动跳转到 Casdoor 的登录页
- [ ] 用 §3 里配置的 bootstrap 管理员账号登录，能看到「平台管理」工作台
- [ ] 控制台顶部横幅符合预期：未配置 license 时显示橙色试用提示；正式授权且距到期还有一段时间时不显示任何提示条
- [ ] 在控制台里签发一把虚拟密钥，用它对数据面发起一次真实调用：

  ```bash
  curl -X POST http://<主机>:8080/v1/chat/completions \
    -H "Authorization: Bearer <签发的密钥>" \
    -H "Content-Type: application/json" \
    -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}'
  ```

  返回 HTTP 200 且带有模型的回复内容，说明控制台、数据面、上游模型三层全部打通。

全部确认无误后，安装即告完成。

---

## §7 遇到问题

安装或使用过程中遇到任何报错，先查运维手册（`operations.md`）的「故障排查表」——那张表按「现象 → 原因 → 动作」整理了已知会遇到的问题，包括架构不匹配、镜像未导入、证书信任、授权过期等常见情况。表里列出的每一条都能在这个仓库的实测中复现过。
