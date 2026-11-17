# dn

一个多连接分片下载的命令行工具. 把文件切成多段并发下载, 并且会把慢分片动态切开交给空闲连接. 支持断点续传.

```
$ dn -n 8 https://mirrors.tuna.tsinghua.edu.cn/ubuntu/ls-lR.gz
22:30:44 INFO 开始下载 链接=https://mirrors.tuna.tsinghua.edu.cn/ubuntu/ls-lR.gz 目录=. 连接数=8
22:30:44 INFO 探测完成 文件名=ls-lR.gz 大小="38 MiB" 支持分片=true 耗时=192ms
22:30:44 INFO 初始分片 数量=8 最小分片="512 KiB"
22:30:46 INFO 下载完成 文件=ls-lR.gz 大小="38 MiB" 耗时=2.096s 平均速度="18 MiB/s" 分裂次数=7 重试次数=0
```

慢分片会被切开交给空闲连接, 所以 `分裂次数` 大于 0. 在终端里运行时, 还会显示一条实时进度条, 包含已下载大小, 速度, 剩余时间和活动连接数.

## 特性

- 多连接分片下载, 默认 8 个连接.
- 动态分片: 有连接空闲时, 把剩余最多的分片从中间切开, 交给空闲连接.
- 断点续传: 中断后用同一条命令重新运行即可继续, 已完成的部分不会重新下载.
- 服务器不支持 Range 时自动退回单连接下载.
- 服务器限流 (429 / 503) 时自动降低并发连接数, 之后逐步恢复.
- 某个分片反复失败时先跳过, 让其它分片继续, 而不是直接终止整个下载.
- 连接卡住超过阈值自动断开重连.
- 目标文件已存在时自动改名为 `name (1).ext`, 不会误覆盖.
- 浏览器集成: `dn serve` 兼容 AB Download Manager 的浏览器扩展, 下载自动带上 Cookie 等请求头, 并在终端里管理任务.

## 构建与安装

```
just build          # 生成 bin/dn, 版本号显示 dev-build
just dist           # 生成当前平台的发布产物到 dist/ (注入版本号)
just test           # 跑测试
go install ./cmd/dn # 安装到 GOBIN
```

`just dist` 需要 [uv](https://docs.astral.sh/uv/), 它只用来跑 `scripts/` 下的标准库 Python 脚本. 版本号由最近的 `v*` tag 推导: 正好在 tag 上显示 `v1.2.3`, 不在 tag 上显示 `v1.2.3-a1b2c3d`, 工作区有未提交改动时显示 `v1.2.3^a1b2c3d`. `dn --version` 只输出版本号本身.

## 用法

```
dn <url> [url...] [flags]
```

常用参数:

| 参数 | 说明 |
| --- | --- |
| `-d, --dir <dir>` | 目标目录, 不存在时自动创建 |
| `-o, --output <name>` | 指定文件名, 只能和单个 URL 一起用 |
| `-n, --connections <n>` | 并发连接数, 1 到 64, 默认 8 |
| `--min-part-size <size>` | 最小分片大小, 默认 512KiB |
| `--no-dynamic` | 关闭动态分片 |
| `-H, --header "Name: value"` | 附加请求头, 可重复 |
| `--proxy <url>` | 代理地址 |
| `--restart` | 忽略续传状态, 从头下载 |
| `--overwrite` | 目标文件已存在时覆盖 |
| `-v` / `-q` | 调试日志 / 只输出警告和错误 |

示例:

```shell
# 下载到当前目录, 16 个连接
dn -n 16 https://example.com/big.iso

# 下载到指定目录, 带上 Referer 和 Cookie
dn -d ~/Downloads -H "Referer: https://example.com" -H "Cookie: a=b" https://example.com/a.zip

# 批量下载
dn https://example.com/a.zip https://example.com/b.zip
```

## 浏览器集成 (dn serve)

`dn serve` 在本机回环地址上启动一个 HTTP 服务, 协议与 [AB Download Manager](https://github.com/amir1376/ab-download-manager) 的浏览器集成一致. 装上它的浏览器扩展, 把扩展选项里的端口指向这个服务, 浏览器里的下载就会交给 `dn`, 并且会带上 Cookie 等请求头, 解决"只有链接却下载不到"的问题.

```shell
dn serve                       # 默认端口 15151, 下载目录取配置
dn serve -d ~/Downloads        # 指定下载目录
dn serve --port 15152          # 端口被占用时换一个, 扩展选项里填同样的端口
dn serve --no-tui              # 只输出日志, 不启动终端界面
```

扩展侧需要确认两件事:

1. 扩展选项里的端口与 `dn serve` 一致, 默认都是 15151.
2. 扩展选项里的 "发送请求头" 保持开启, 这样 Cookie 才会随下载一起发过来.

如果机器上同时装了 AB Download Manager 本体并让它在后台运行, 两者会抢同一个端口. 用 `--port` 给 `dn serve` 换一个端口, 再在扩展选项里填同样的值即可.

服务只监听 `127.0.0.1` 与 `::1`, 并且拒绝来自普通网页的请求 (只接受无 Origin 或扩展来源的请求), 所以不需要额外开放端口.

终端里会显示任务列表与日志:

| 按键 | 作用 |
| --- | --- |
| `↑` / `↓` 或 `k` / `j` | 选择任务 |
| `s` 或空格 | 暂停 / 恢复选中任务 |
| `r` | 重试已暂停或失败的任务 |
| `x` | 从列表移除任务 (不删除已下载的数据) |
| `q` | 退出. 还有任务在下载时需要再按一次确认 |

暂停会让进行中的任务写出续传状态, 恢复时接着下. 退出 `dn serve` 也会保存进度, 之后用 `dn <url>` 或再次 `dn serve` 都能续传.

标准输出不是终端时 (重定向到文件, 或者跑在后台) 会自动退回纯日志模式.

api key 是可选的. 如果扩展选项里填了 key, `dn serve` 也要用 `--api-key` 配上同样的值; 再用 `--api-key-enforced` 可以让 `/ping` 与 `/add` 也必须带 key. 无论是否强制, `/start-headless-download` 都要求 key, 这个端点供脚本直接提交下载 (字段与 ABDM 的 REST API 相同, 支持 `folder` 与 `name`).

不支持 HLS: 扩展提交 `type` 为 `hls` 的下载会被拒绝.

## 配置文件

配置路径: `~/.config/dn/config.toml` (或 `$XDG_CONFIG_HOME/dn/config.toml`, Windows 为 `%APPDATA%\dn\config.toml`). 可用 `DN_CONFIG_DIR` 环境变量覆盖目录.

```
dn config path    # 查看配置路径
dn config init    # 写入默认配置
dn config show    # 查看当前生效的配置
```

字段说明见 [config.toml.example](config.toml.example). 命令行参数优先级高于配置文件.

## 断点续传

下载过程中, `dn` 会在目标位置写两个文件:

- `big.iso.part` - 已下载的数据.
- `big.iso.part.json` - 分片进度, 文件大小, ETag 等信息.

中断 (Ctrl-C, 关终端, 断网) 后重新运行同一条命令即可继续. 以下情况会放弃续传并从头下载: 服务器上的文件大小或 ETag 变化, 数据文件被删除或大小不对, 服务器不再支持 Range. 可以用 `--restart` 主动放弃已有进度.

## 动态分片原理

1. 探测阶段发一个 `Range: bytes=0-255` 的请求. 返回 206 且 `Content-Range` 带有总长度, 才认为支持分片下载.
2. 按 `min(连接数, ceil(总大小 / 最小分片))` 把文件均分成若干区间, 每个区间由一个连接用 `Range: bytes=start-end` 请求.
3. 每个连接只能写入自己的 safe zone, safe zone 逐步向前推进, 每次最多 1MiB. 这样切分时已写入的区间不会与新连接冲突.
4. 有连接空闲且没有未开始的分片时, 选出剩余最多的活动分片, 把 safe zone 之后的部分对半切开, 后半段交给空闲连接. 切出的片段小于最小分片时不再切.
5. 被切短的连接写完自己的新终点后正常关闭, 数据由新连接继续.

切分算法参考了 [AB Download Manager](https://github.com/amir1376/ab-download-manager) (Apache-2.0) 的 `PartSplitSupport`, 用 Go 重写.

## 测试

```
just test
```

集成测试在本地起 HTTP 服务器, 覆盖这些情况: 多连接下载加动态分裂, 服务器忽略 Range, 中断后续传 (并断言续传请求没有重复下载已完成区间), 文件大小变化, 中途不再支持 Range, 连接卡住后重连.

## 已知限制

- 同一个文件同时运行两个 `dn` 没有文件锁保护, 不要这样做.
- 不支持限速, HLS/m3u8, 种子.
- 长度为未知的响应只能单连接下载, 无法续传.
