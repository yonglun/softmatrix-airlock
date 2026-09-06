#!/usr/bin/env bash
#
# Airlock 一键安装。在客户的机器上运行，**不需要外网**。
#
# 五步：前置检查 → 导入镜像 → 生成配置 → 起栈等健康 → 初始化 schema 并自检。
# 可重复执行：不覆盖已有 .env，schema 步骤幂等，重复导入镜像无副作用。

set -euo pipefail

cd "$(dirname "$0")"

RED=$'\033[31m'; YELLOW=$'\033[33m'; GREEN=$'\033[32m'; RESET=$'\033[0m'

info()  { echo "${GREEN}==>${RESET} $*"; }
warn()  { echo "${YELLOW}警告:${RESET} $*"; }
# die 的第二个参数起是「该怎么办」，必须给，不能只报错不给出路。
die() {
    echo "${RED}错误:${RESET} $1" >&2
    shift
    if [ $# -gt 0 ]; then
        echo "" >&2
        echo "该怎么办：" >&2
        for line in "$@"; do echo "  - $line" >&2; done
    fi
    exit 1
}

# ---------- 第 1 步：前置检查 ----------
step_precheck() {
    info "第 1/5 步：前置检查"

    command -v docker >/dev/null 2>&1 || die \
        "未找到 docker 命令" \
        "先安装 Docker Engine 20.10 或更高版本" \
        "安装后执行 docker info 确认守护进程在运行"

    docker info >/dev/null 2>&1 || die \
        "docker 守护进程没有响应" \
        "执行 systemctl start docker 启动守护进程" \
        "若当前用户不在 docker 组，改用 sudo 运行本脚本，或把用户加入 docker 组后重新登录"

    docker compose version >/dev/null 2>&1 || die \
        "未找到 docker compose 插件（v2）" \
        "安装 docker-compose-plugin 包" \
        "注意本脚本要的是 docker compose（带空格），不是旧的 docker-compose"

    # openssl 用于生成随机口令与加密密钥。最小化安装的 Linux 上未必有，
    # 而这台机器没有外网、装不了包——必须在动任何东西之前就问清楚。
    command -v openssl >/dev/null 2>&1 || die \
        "未找到 openssl 命令" \
        "安装 openssl 包（RHEL/CentOS: yum install openssl；Debian/Ubuntu: apt install openssl）" \
        "本脚本用它生成数据库口令与加密密钥，没有替代方案"

    [ -f VERSION ] || die \
        "当前目录下没有 VERSION 文件" \
        "确认已解压完整的交付包，并在解压出来的目录里运行 ./install.sh"

    local pkg_arch host_arch
    pkg_arch=$(grep '^ARCH=' VERSION | cut -d= -f2-)
    host_arch=$(uname -m)
    # uname 说 x86_64/aarch64，Docker 说 amd64/arm64，先归一化
    case "$host_arch" in
        x86_64)  host_arch=amd64 ;;
        aarch64) host_arch=arm64 ;;
    esac

    if [ "$pkg_arch" != "$host_arch" ]; then
        die "交付包的架构与本机不符：包是 ${pkg_arch}，本机是 ${host_arch}（uname -m 报 $(uname -m)）" \
            "向供应方索取 ${host_arch} 架构的交付包" \
            "强行安装会在容器启动时报 exec format error，那个错误与真实原因看不出关系"
    fi

    info "  docker 与 compose 就绪，架构匹配（${pkg_arch}）"
}

# ---------- 第 2 步：导入镜像 ----------
step_load_images() {
    info "第 2/5 步：导入镜像"

    [ -f images.tar ] || die \
        "当前目录下没有 images.tar" \
        "确认交付包解压完整（tar 包里应含 images.tar，约 1 GB）" \
        "若下载中断，重新获取完整的 tar.gz 后再解压"

    docker load -i images.tar || die \
        "docker load 失败" \
        "检查磁盘剩余空间：docker 需要约 3 GB 可用空间来解压镜像" \
        "检查 images.tar 是否完整（大小应与交付说明一致）"

    # 逐个确认镜像真的在本地了。compose 设了 pull_policy: never，
    # 缺任何一个都会在起栈时立刻失败，不如在这里先说清楚缺的是哪个。
    #
    # 只按 tag（去掉 @digest 后缀）校验：镜像在打包时已经按平台专属的
    # digest 拉取、校验过架构、再打回本地 tag（见 build-release.sh），
    # VERSION 里的 @digest 后缀只是留给人核对用的记录。docker save/load
    # 之间 RepoTags 保真是 Docker 从不含糊的保证，RepoDigests 则因版本与
    # 存储驱动而异，不能依赖——按 tag 查是唯一在所有 Docker 版本上都可靠
    # 的做法。
    local missing=0
    while IFS= read -r ref; do
        [ -n "$ref" ] || continue
        local tag_ref="${ref%@*}"
        if ! docker image inspect "$tag_ref" >/dev/null 2>&1; then
            echo "  缺少镜像: $ref" >&2
            missing=1
        fi
    done < <(grep '^IMAGE=' VERSION | cut -d= -f2-)

    [ "$missing" -eq 0 ] || die \
        "有镜像未成功导入" \
        "重新执行本脚本（重复导入无副作用）" \
        "若仍然缺失，说明 images.tar 不完整，请重新获取交付包"

    info "  镜像已全部导入"
}

# ---------- 第 3 步：生成配置 ----------
step_config() {
    info "第 3/5 步：生成配置"

    if [ -f .env ]; then
        warn ".env 已存在，保持不变（不覆盖已有配置）"
    else
        [ -f .env.example ] || die \
            "找不到 .env.example" \
            "确认交付包解压完整"

        cp .env.example .env

        # 随机口令与密钥。模板里的空值绝不能带进生产。
        local pg_pass ch_pass enc_key llm_key
        pg_pass=$(openssl rand -hex 24)
        ch_pass=$(openssl rand -hex 24)
        enc_key=$(openssl rand -base64 32)
        llm_key="sk-$(openssl rand -hex 24)"

        # 用 | 作分隔符：base64 里可能出现 /，用 / 会把 sed 打断
        sed -i.bak "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pg_pass}|"        .env
        sed -i.bak "s|^CLICKHOUSE_PASSWORD=.*|CLICKHOUSE_PASSWORD=${ch_pass}|"    .env
        sed -i.bak "s|^AIRLOCK_ENCRYPTION_KEY=.*|AIRLOCK_ENCRYPTION_KEY=${enc_key}|" .env
        sed -i.bak "s|^LITELLM_MASTER_KEY=.*|LITELLM_MASTER_KEY=${llm_key}|"      .env
        rm -f .env.bak

        chmod 600 .env
        info "  已生成 .env，随机口令与加密密钥已填入"
    fi

    # 版本号每次都从 VERSION 同步——升级后重跑脚本时它必须跟着变。
    local ver
    ver=$(grep '^VERSION=' VERSION | cut -d= -f2-)
    sed -i.bak "s|^AIRLOCK_VERSION=.*|AIRLOCK_VERSION=${ver}|" .env
    rm -f .env.bak

    # 必填项检查。这些填不上，装完也用不了。
    local missing=()
    # 统一用 -E（ERE）+ 不转义的 +。混用 BRE 的 \+（GNU 扩展，表示「一个或多个」）
    # 和 ERE 的 \+（标准语义是「字面量加号」）曾经在这里出过真 bug：ERE 模式下
    # 的 .\+ 只会匹配「任意字符后跟一个字面加号」，像 sk-abc123 这种正常取值
    # 完全匹配不上，会被误判成「没填」。
    grep -qE '^AIRLOCK_BOOTSTRAP_ADMIN=.+' .env || missing+=("AIRLOCK_BOOTSTRAP_ADMIN（首个平台管理员的 email）")
    grep -qE '^OIDC_CLIENT_SECRET=.+'      .env || missing+=("OIDC_CLIENT_SECRET（Casdoor 里 airlock 应用的密钥）")
    if ! grep -qE '^(DEEPSEEK|DASHSCOPE|OPENAI)_API_KEY=.+' .env; then
        missing+=("至少一个模型供应商密钥（DEEPSEEK_API_KEY / DASHSCOPE_API_KEY / OPENAI_API_KEY）")
    fi

    if [ ${#missing[@]} -gt 0 ]; then
        echo ""
        warn "以下配置项还没填，现在用文本编辑器打开 $(pwd)/.env 补上，然后重新运行本脚本："
        for m in "${missing[@]}"; do echo "    - $m"; done
        echo ""
        echo "填好之后执行： ./install.sh"
        exit 3
    fi

    # license 的两项必须同时设或同时留空。只设 AIRLOCK_LICENSE_FILE 的话，
    # 容器里那个路径挂的是 /dev/null，control 会读到空文件并以「签名校验失败」
    # 拒绝启动——那个错误指向的方向完全不对。
    local host_lic ctr_lic
    host_lic=$(grep '^LICENSE_HOST_PATH=' .env | cut -d= -f2-)
    ctr_lic=$(grep '^AIRLOCK_LICENSE_FILE=' .env | cut -d= -f2-)
    if [ -n "$host_lic" ] && [ -z "$ctr_lic" ]; then
        die "配置了 LICENSE_HOST_PATH 但没配 AIRLOCK_LICENSE_FILE" \
            "在 .env 里设 AIRLOCK_LICENSE_FILE=/etc/airlock/license.txt" \
            "两项要么都填，要么都留空（留空即试用模式）"
    fi
    if [ -z "$host_lic" ] && [ -n "$ctr_lic" ]; then
        die "配置了 AIRLOCK_LICENSE_FILE 但没配 LICENSE_HOST_PATH" \
            "在 .env 里把 LICENSE_HOST_PATH 设成主机上 license 文件的绝对路径" \
            "或把 AIRLOCK_LICENSE_FILE 也清空，以试用模式运行"
    fi
    if [ -n "$host_lic" ] && [ ! -f "$host_lic" ]; then
        die "LICENSE_HOST_PATH 指向的文件不存在：${host_lic}" \
            "确认路径是主机上的绝对路径，且文件确实存在" \
            "不需要授权时把 LICENSE_HOST_PATH 与 AIRLOCK_LICENSE_FILE 都留空"
    fi

    info "  配置检查通过"
}

# ---------- 第 4 步：起栈并等健康 ----------
wait_healthy() {
    local svc=$1 timeout=${2:-180} waited=0
    while [ "$waited" -lt "$timeout" ]; do
        local cid status
        cid=$(docker compose ps -q "$svc" 2>/dev/null || true)
        if [ -n "$cid" ]; then
            status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid" 2>/dev/null || echo "none")
            [ "$status" = "healthy" ] && return 0
            # 没有 healthcheck 的服务，跑起来就算数
            if [ "$status" = "none" ] && [ "$(docker inspect --format '{{.State.Running}}' "$cid")" = "true" ]; then
                return 0
            fi
        fi
        sleep 3
        waited=$((waited + 3))
    done

    echo "" >&2
    echo "${RED}服务 ${svc} 在 ${timeout} 秒内没有变健康。最后 50 行日志：${RESET}" >&2
    docker compose logs --tail=50 "$svc" >&2 || true
    return 1
}

step_up() {
    info "第 4/5 步：启动服务"

    docker compose up -d || die \
        "docker compose up 失败" \
        "若报镜像不存在，重新执行本脚本让第 2 步重新导入" \
        "若报端口被占用，改 .env 里的 CONTROL_PORT / EDGE_PORT / CASDOOR_PORT"

    for svc in postgres clickhouse litellm casdoor airlock-control airlock-edge; do
        info "  等待 ${svc} 就绪…"
        wait_healthy "$svc" || die \
            "${svc} 启动失败（日志见上）" \
            "对照运维手册的故障排查表定位上面的报错" \
            "修正 .env 后重新执行本脚本"
    done

    info "  全部服务已就绪"
}

# ---------- 第 5 步：初始化 schema 并自检 ----------
step_schema() {
    info "第 5/5 步：初始化 schema 并自检"

    # 一律用 -f2-（不是 -f2）：base64 的 padding 与 URL 查询串里都可能出现 =，
    # 截断了就会拿到半截口令，且错误现象是「认证失败」，与真实原因看不出关系。
    local pg_user ch_user ch_pass
    pg_user=$(grep '^POSTGRES_USER=' .env | cut -d= -f2-)
    ch_user=$(grep '^CLICKHOUSE_USER=' .env | cut -d= -f2-)
    ch_pass=$(grep '^CLICKHOUSE_PASSWORD=' .env | cut -d= -f2-)

    docker compose exec -T postgres psql -U "$pg_user" -d postgres < schema/postgres-init.sql >/dev/null || die \
        "创建 litellm / casdoor 数据库失败" \
        "确认 postgres 容器健康：docker compose ps postgres" \
        "查看日志：docker compose logs postgres"

    docker compose exec -T clickhouse clickhouse-client \
        --user "$ch_user" --password "$ch_pass" --multiquery < schema/clickhouse-init.sql >/dev/null || die \
        "创建 ClickHouse 用量表失败" \
        "确认 clickhouse 容器健康：docker compose ps clickhouse" \
        "查看日志：docker compose logs clickhouse"

    # 自检一：用量表确实存在。它只由上面这一步创建，Go 侧从不建表，
    # 漏了的话数据面会在第一次记账时失败。
    docker compose exec -T clickhouse clickhouse-client \
        --user "$ch_user" --password "$ch_pass" \
        --query "SELECT count(*) FROM airlock.usage_records" >/dev/null || die \
        "用量表 airlock.usage_records 不存在" \
        "重新执行本脚本（schema 步骤幂等，可安全重跑）"

    # 自检二：两个进程的健康端点都答话。
    # 走容器内的 wget 而不是宿主机的 curl——最小化安装的 Linux 上未必有 curl，
    # 而 alpine 基础镜像里的 busybox 一定带 wget。
    docker compose exec -T airlock-control \
        wget -q -O- http://localhost:8081/healthz >/dev/null || die \
        "控制台健康检查失败" \
        "查看日志：docker compose logs airlock-control"
    docker compose exec -T airlock-edge \
        wget -q -O- http://localhost:8080/healthz >/dev/null || die \
        "数据面健康检查失败" \
        "查看日志：docker compose logs airlock-edge"

    info "  schema 就绪，自检通过"
}

# ---------- 主流程 ----------
main() {
    echo ""
    echo "Airlock 安装程序"
    echo "================"
    echo ""

    step_precheck
    step_load_images
    step_config
    step_up
    step_schema

    local control_port casdoor_origin
    control_port=$(grep '^CONTROL_PORT=' .env | cut -d= -f2-)
    casdoor_origin=$(grep '^CASDOOR_ORIGIN=' .env | cut -d= -f2-)

    echo ""
    echo "${GREEN}安装完成。${RESET}"
    echo ""
    echo "控制台：      http://localhost:${control_port}"
    echo "身份提供方：  ${casdoor_origin}"
    echo ""
    echo "接下来："
    echo "  1. 在 Casdoor 里创建 airlock 应用与首批用户（见部署文档「配置身份提供方」）"
    echo "  2. 用 AIRLOCK_BOOTSTRAP_ADMIN 对应的账号登录控制台，它会自动成为平台管理员"
    echo "  3. 未配置授权文件时系统以试用模式运行（5 席位）；正式授权见部署文档「投放授权文件」"
    echo ""
}

main "$@"
