# ua3f-win

Windows 11 amd64 上的 UA3F 重写版，生成单个 `ua3f-win.exe`。

## 使用

双击 `ua3f-win.exe` 后会自动请求管理员权限，并用 Microsoft Edge 打开本地控制页面。

页面可配置：

- UA 预设：`wechat` / `pc`
- TTL
- 日志级别：`info` / `debug` / `warn`

程序会自动检测所有 TCP 端口上的明文 HTTP 请求，无需配置端口。



## 命令行

```powershell
.\ua3f-win.exe -ua wechat
.\ua3f-win.exe -ua pc
.\ua3f-win.exe -ua wechat -log debug
.\ua3f-win.exe -ttl 65
```

HTTP 会在所有 TCP 端口上自动检测。

## 限制

- 只处理 IPv4 明文 HTTP。
- 不处理 HTTPS 内容。
- 不做完整 TCP 流重组。
- UA 只在已有 `User-Agent` 请求头内等长覆盖，不插入缺失的 `User-Agent` 行。
- WinDivert 需要管理员权限。

## 编译

```powershell
cd "path\to\ua3f-win"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:GOPROXY = "https://goproxy.cn,direct"
go mod tidy
go build -ldflags "-s -w" -o ua3f-win.exe
```

也可以运行：

```powershell
powershell -ExecutionPolicy Bypass -File .\build.ps1
```

## 页面样式

<img width="2557" height="1391" alt="ua3f-win web ui" src="https://github.com/user-attachments/assets/a075a74a-a161-4cd3-9d2f-cdf13e35b35e" />

## 可靠性与本地控制

驱动的两个原始文件名和两个运行时文件名均以可执行文件中的内嵌资源为基准进行 SHA-256 校验。损坏或缺失时先写入并校验同目录临时文件，再替换目标，不会先清空已有文件。如果文件正在使用或没有写权限，启动会返回明确错误，保留旧文件。

启动、运行、停止使用独立会话管理。启动过程中收到停止请求后，即使驱动稍后打开成功，也会关闭该句柄，不再进入运行循环。重复启停不会创建并行抓包循环。接收、发送或包处理异常会清理句柄并显示错误，正常退出最多等待 5 秒完成停止。

本地页面提供“检查驱动服务”：只读取 WinDivert 服务的注册路径、运行状态，以及文件是否存在。启动时也会把诊断写入实时日志。其他程序注册的驱动可能位于不同目录，路径不同本身不代表故障。

### 移动目录后的排查

1. 以管理员权限打开程序，点击“检查驱动服务”，记录具体服务名称及注册路径。
2. 如果路径对应的文件仍存在，先确认它是否属于其他正在使用 WinDivert 的程序；不要直接删除服务。
3. 如果旧路径已失效，优先恢复原目录并正常退出使用该驱动的程序，或重启 Windows 后再检查。
4. 只有确认服务属于已移走的旧实例，且所有相关程序均已退出时，才在管理员 PowerShell 中对诊断得到的**具体服务名称**进行人工修复：

```powershell
$driverService = '从诊断结果复制的具体服务名称'
sc.exe qc $driverService
sc.exe query $driverService
# 核对服务名称、BINARY_PATH_NAME 及其他使用者后再执行：
sc.exe stop $driverService
sc.exe delete $driverService
```

若显示服务正在删除，关闭相关工具或重启后再尝试启动。程序不会自动执行停止、删除或重新配置共享驱动服务。

### 本地 API

仍只监听 `127.0.0.1`。每次启动服务生成随机会话令牌，控制页面通过 `X-UA3F-Token` 请求头访问所有 `/api/*` 接口，包括状态和日志。程序重启后应刷新页面。API 会校验实际监听地址的 Host、同源 Origin，拒绝跨站请求；配置请求只接受单个 JSON 值、`application/json`，上限 16 KiB。

脚本调用示例（端口以程序显示的地址为准）：

```powershell
$controlUrl = 'http://127.0.0.1:18080'
$page = (Invoke-WebRequest "$controlUrl/").Content
$sessionToken = [regex]::Match($page, '<meta name="ua3f-token" content="([a-f0-9]{64})">').Groups[1].Value
$apiHeaders = @{ 'X-UA3F-Token' = $sessionToken; Origin = $controlUrl }
Invoke-RestMethod "$controlUrl/api/diagnostics" -Headers $apiHeaders
Invoke-RestMethod "$controlUrl/api/status" -Headers $apiHeaders
```

令牌用于防止网页跨站操纵本地接口，不是 Windows 账户间的权限隔离机制。

### HTTP 改写范围

只改写单个 TCP 数据包中完整且格式有效的 HTTP/1.0 或 HTTP/1.1 请求头。字段名必须准确匹配 `User-Agent`（忽略大小写）；HTTP 正文、类似 `X-User-Agent` 的字段、响应、分段请求头、IP 分片和格式异常的请求会保持 UA 不变。

仍采用等长覆盖：较短 UA 以空格补齐，较长 UA 截断；不增加缺失字段，不改变 TCP 序列号空间。尚未实现 TCP 流重组、IPv6 或 HTTPS 内容改写。

## 验证

在 Windows amd64 上运行：

```powershell
go test ./...
go vet ./...
node --test web_ui_test.cjs
go build -trimpath -ldflags "-s -w" -o ua3f-win.exe
# 需要可供 Go 使用的 C 编译器，例如 MinGW-w64：
$env:CGO_ENABLED = '1'
go test -race ./...
# 可选：协议边界模糊测试
go test -run '^FuzzRewritePacket$' -fuzz '^FuzzRewritePacket$' -fuzztime 10s
go test -run '^FuzzUserAgentRewrite$' -fuzz '^FuzzUserAgentRewrite$' -fuzztime 10s
```

`.github/workflows/ci.yml` 在 Windows runner 上执行格式检查、Go 测试、vet、race、前端测试和构建。单元测试会加载 DLL 检查所需导出，但不会调用 WinDivertOpen、安装驱动或截获系统流量。真实驱动加载、目录移动后的恢复、数据包收发需要在独立的管理员测试环境中另行验收。
