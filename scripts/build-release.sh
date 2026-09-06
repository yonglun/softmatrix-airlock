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
# 从生产 compose 里读，避免两处各写一份 digest 迟早不一致。
echo "==> 拉取第三方镜像（按 digest）"
# 排掉带 ${} 的那行（airlock 自己的镜像，上面刚 build 出来）。
# 注意不能用 grep -oE '^\s+image: [^$]\S+' —— airlock:${VER} 是以 a 开头的，
# 那个 [^$] 排不掉它，会把自己的镜像又拉一遍（且 registry 上根本没有）。
# 也不用 mapfile：那是 bash 4+ 的内建，而 macOS 自带的还是 bash 3.2。
THIRD_PARTY=()
while IFS= read -r img; do
    [ -n "$img" ] && THIRD_PARTY+=("$img")
done < <(grep -E '^[[:space:]]+image: ' deploy/release/docker-compose.yml \
         | grep -v '\${' | awk '{print $2}')

[ ${#THIRD_PARTY[@]} -gt 0 ] || { echo "错误: 没从 compose 里解析出第三方镜像" >&2; exit 1; }

for img in "${THIRD_PARTY[@]}"; do
    echo "  $img"
    # --platform 必须显式传：构建机可能是 arm64，靠默认行为会打出一个
    # 在客户 x86 服务器上报 exec format error 的包。
    docker pull --platform "$PLATFORM" "$img"
done

# ---- 3. save ----
echo "==> 导出镜像到 images.tar"
docker save -o "${OUT}/images.tar" "$AIRLOCK_IMAGE" "${THIRD_PARTY[@]}"

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
