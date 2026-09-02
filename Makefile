.PHONY: test build lint up down migrate console console-install

test:
	go test ./... -race -count=1
	@test -d web/node_modules || { echo "前端依赖未安装：请先执行 make console-install"; exit 1; }
	cd web && npm test

console-install:
	cd web && npm ci

console:
	cd web && npm run build

build: console
	go build -o bin/airlock ./cmd/airlock

lint:
	go vet ./...
	cd web && npm run typecheck
	@echo "检查管理面与数据面的包边界..."
	@! go list -deps ./internal/control/... 2>/dev/null | grep -q 'airlock/internal/edge' \
		|| { echo "违规：internal/control 依赖了 internal/edge"; exit 1; }
	@! go list -deps ./internal/edge/... 2>/dev/null | grep -q 'airlock/internal/control' \
		|| { echo "违规：internal/edge 依赖了 internal/control"; exit 1; }
	@echo "检查 authz 包的独立性..."
	@! go list -deps ./internal/authz/... 2>/dev/null | grep -qE 'airlock/internal/(control|edge)' \
		|| { echo "违规：internal/authz 依赖了 internal/control 或 internal/edge"; exit 1; }
	@echo "检查 litellm 客户端包的独立性..."
	@! go list -deps ./internal/litellm/... 2>/dev/null | grep -qE 'airlock/internal/(control|edge)' \
		|| { echo "违规：internal/litellm 依赖了 internal/control 或 internal/edge"; exit 1; }
	@echo "检查 notify 包的独立性..."
	@! go list -deps ./internal/notify/... 2>/dev/null | grep -qE 'airlock/internal/(control|edge)' \
		|| { echo "违规：internal/notify 依赖了 internal/control 或 internal/edge"; exit 1; }
	@echo "包边界检查通过"

up:
	docker compose --env-file .env -f deploy/docker-compose.yml up -d

down:
	docker compose -f deploy/docker-compose.yml down

migrate:
	go run ./cmd/airlock migrate
