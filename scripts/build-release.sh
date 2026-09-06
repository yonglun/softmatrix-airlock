#!/usr/bin/env bash
#
# 打交付包。在**我们的构建机**上运行，需要外网。
#
#   scripts/build-release.sh [--version v1.0.0] [--platform linux/amd64] [--allow-dirty]
#
# 产出 dist/airlock-<版本>-<os>-<arch>.tar.gz。

set -euo pipefail

cd "$(dirname "$0")/.."

PLATFORM="linux/amd64"
VERSION=""
ALLOW_DIRTY=0

while [ $# -gt 0 ]; do
    case "$1" in
        --version)     VERSION="$2"; shift 2 ;;
        --platform)    PLATFORM="$2"; shift 2 ;;
        --allow-dirty) ALLOW_DIRTY=1; shift ;;
        *) echo "未知参数: $1" >&2; exit 2 ;;
    esac
done

# ---- 版本号 ----
if [ -z "$VERSION" ]; then
    VERSION=$(git describe --tags --always)
fi

if [ -n "$(git status --porcelain)" ]; then
    if [ "$ALLOW_DIRTY" -eq 0 ]; then
        echo "错误: 工作区有未提交的改动，拒绝打包。" >&2
        echo "" >&2
        echo "从带未提交改动的树里打出来的包无法复现——客户报障时我们拿不出" >&2
        echo "与他手上一模一样的代码。请先提交，或显式传 --allow-dirty。" >&2
        exit 1
    fi
    VERSION="${VERSION}-dirty"
fi

ARCH="${PLATFORM##*/}"
OS="${PLATFORM%%/*}"
NAME="airlock-${VERSION}-${OS}-${ARCH}"
OUT="dist/${NAME}"

echo "==> 版本 ${VERSION}，目标平台 ${PLATFORM}"

rm -rf "$OUT"
mkdir -p "$OUT"

# ---- 1. 构建 airlock 镜像 ----
echo "==> 构建 airlock 镜像"
AIRLOCK_IMAGE="airlock:${VERSION}"
docker build --platform "$PLATFORM" \
    --build-arg "VERSION=${VERSION}" \
    -t "$AIRLOCK_IMAGE" .

# ---- 2. 拉第三方镜像 ----
# 名字（tag，不含 digest）从生产 compose 里读，避免两处各写一份迟早不一致。
echo "==> 拉取第三方镜像"
# 排掉带 ${} 的那行（airlock 自己的镜像，上面刚 build 出来）。
# 注意不能用 grep -oE '^\s+image: [^$]\S+' —— airlock:${VER} 是以 a 开头的，
# 那个 [^$] 排不掉它，会把自己的镜像又拉一遍（且 registry 上根本没有）。
# 也不用 mapfile：那是 bash 4+ 的内建，而 macOS 自带的还是 bash 3.2。
THIRD_PARTY_NAMES=()
while IFS= read -r img; do
    [ -n "$img" ] && THIRD_PARTY_NAMES+=("$img")
done < <(grep -E '^[[:space:]]+image: ' deploy/release/docker-compose.yml \
         | grep -v '\${' | awk '{print $2}')

[ ${#THIRD_PARTY_NAMES[@]} -gt 0 ] || { echo "错误: 没从 compose 里解析出第三方镜像" >&2; exit 1; }

# 钉死到 P1.1–P1.5b 全程实测跑通的那一份（index digest，架构无关，见
# 设计文档 §0.2/§0.3）。这不写进生产 compose——生产环境完全离线、
# pull_policy: never，Docker 永远不会拿它去 registry 验证，写在那里没有
# 任何运行时效果；真正需要「钉死到实测跑通的那一份」的地方是这里，打包
# 阶段用它去拉、去校验，再打回本地 tag 交给 compose 按 tag 引用。
pinned_digest() {
    case "$1" in
        postgres:17) echo "sha256:67f41722b7a8cbdb868a44a4995c846eddfdc2973bccb291ce937dce88ad5675" ;;
        clickhouse/clickhouse-server:25.3) echo "sha256:b627d7a9bc0e0c1bac26cdbe9d2fc6316faa29c5d8a174f28f5abd57d0fa6ba2" ;;
        ghcr.io/berriai/litellm:main-stable) echo "sha256:20b5044b619055374061a6d5b7b08754cad75aeabbf82ddf4f69cc0cf80ddaf4" ;;
        casbin/casdoor:latest) echo "sha256:f332047c325588fb047bc1a34b0ce473f12ac044c0255a520f30f521fd7c249b" ;;
        *)
            echo "错误: ${1} 没有登记的已验证 digest" >&2
            echo "compose 里新增了这个服务，但打包脚本还不认识它。" >&2
            echo "先手工验证这个镜像能正常跑，再把它的 index digest 加进" >&2
            echo "build-release.sh 的 pinned_digest() 里。" >&2
            return 1
            ;;
    esac
}

command -v jq >/dev/null 2>&1 || {
    echo "错误: 需要 jq 来解析多架构镜像清单" >&2
    echo "安装: brew install jq（macOS）或 apt install jq（Debian/Ubuntu）" >&2
    exit 1
}

WANT_OS="${PLATFORM%%/*}"
WANT_ARCH="${PLATFORM##*/}"

THIRD_PARTY=()  # 记进 VERSION 的完整引用：name:tag@digest，供人工审计
for name in "${THIRD_PARTY_NAMES[@]}"; do
    list_digest=$(pinned_digest "$name") || exit 1
    repo="${name%%:*}"
    ref="${name}@${list_digest}"
    echo "  $ref"
    THIRD_PARTY+=("$ref")

    # 关键坑（P1.5c 验收时在这台机器上实测踩到过一次）：manifest-list
    # digest 在所有架构下完全一致（这正是「按它钉死跨架构通用」的原因）。
    # 如果本机之前已经用另一种架构拉过同一个 name:tag@digest 引用（这台
    # 构建机很可能两种架构都手工验证过），docker pull --platform 会认为
    # 「本地已经满足这个引用」而直接跳过实际替换，不会真的换成目标架构的
    # 内容——且不报任何错，docker save 出来的东西会静默是错误架构，直到
    # 客户在 x86 机器上启动容器报 exec format error 才会暴露。
    #
    # 解法：先用 imagetools 解析出该架构专属的 manifest digest（不是清单
    # 列表 digest——这个值本身带架构信息，天然不会有歧义：本地缓存里的
    # 同一个 digest 只可能对应一种架构的内容），再用它去 pull。
    platform_digest=$(docker buildx imagetools inspect --raw "$ref" \
        | jq -r --arg os "$WANT_OS" --arg arch "$WANT_ARCH" \
            '.manifests[]? | select(.platform.os == $os and .platform.architecture == $arch) | .digest')
    [ -n "$platform_digest" ] || {
        echo "错误: ${ref} 在 registry 上找不到 ${PLATFORM} 的变体" >&2
        exit 1
    }

    docker pull "${repo}@${platform_digest}"

    # 硬校验：拉下来的东西架构必须对得上，不对就立刻失败，绝不静默放行——
    # 这条断言就是刚才那个坑的解药。
    got_arch=$(docker image inspect "${repo}@${platform_digest}" --format '{{.Architecture}}')
    [ "$got_arch" = "$WANT_ARCH" ] || {
        echo "错误: ${ref} 拉到的是 ${got_arch}，不是要求的 ${WANT_ARCH}" >&2
        exit 1
    }

    # 打回本地 tag——生产 compose 按 tag（不是 digest）引用镜像；docker
    # save/load 之间 RepoTags 保真是 Docker 从不含糊的保证，RepoDigests
    # 则因版本与存储驱动而异，不能依赖（这条也是本次验收实测出来的）。
    docker tag "${repo}@${platform_digest}" "$name"
done

# ---- 3. save ----
echo "==> 导出镜像到 images.tar"
docker save -o "${OUT}/images.tar" "$AIRLOCK_IMAGE" "${THIRD_PARTY_NAMES[@]}"

# ---- 4. VERSION 文件 ----
echo "==> 生成 VERSION"
{
    echo "VERSION=${VERSION}"
    echo "ARCH=${ARCH}"
    echo "OS=${OS}"
    echo "BUILT_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "GIT_COMMIT=$(git rev-parse HEAD)"
    echo "IMAGE=${AIRLOCK_IMAGE}"
    for img in "${THIRD_PARTY[@]}"; do
        echo "IMAGE=${img}"
    done
} > "${OUT}/VERSION"

# ---- 5. 拷贝其余材料 ----
echo "==> 组装交付包"
cp deploy/release/docker-compose.yml "$OUT/"
cp deploy/release/.env.example       "$OUT/"
cp deploy/release/install.sh         "$OUT/"
chmod +x "${OUT}/install.sh"
cp -r deploy/release/schema          "$OUT/"
cp -r deploy/release/litellm         "$OUT/"
mkdir -p "${OUT}/docs"
cp docs/deploy/deployment.md "${OUT}/docs/"
cp docs/deploy/operations.md "${OUT}/docs/"

# ---- 6. 打 tar ----
echo "==> 打包"
tar -czf "dist/${NAME}.tar.gz" -C dist "$NAME"

echo ""
echo "完成: dist/${NAME}.tar.gz ($(du -h "dist/${NAME}.tar.gz" | cut -f1))"
