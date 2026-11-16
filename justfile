[private]
default:
    @just --list

# 构建当前平台的二进制到 bin/dn, 版本号显示为 dev-build.
build:
    go build -o bin/dn ./cmd/dn

# 根据当前平台生成发布产物 (注入版本号, 产物落在 dist/).
dist:
    uv run --no-project python scripts/dist.py

# 运行全部测试 (含竞态检测).
test:
    go test -race ./... -count=1

# 静态检查.
vet:
    go vet ./...

# 检查格式是否符合 gofmt, 只报告不修改.
fmt-check:
    @unformatted=$(shell gofmt -l .); \
    if [ -n "$$unformatted" ]; then \
        echo "以下文件未格式化:"; \
        echo "$$unformatted"; \
        exit 1; \
    fi

# 整理依赖.
tidy:
    go mod tidy

# just run https://example.com/file.iso
# 直接运行 CLI.
run *args:
    go run ./cmd/dn {{args}}

# 输出当前生效的配置.
config:
    @go run ./cmd/dn config show

# 写入默认配置文件到用户配置目录.
config-init:
    @go run ./cmd/dn config init
