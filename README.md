<p align="center">
  <img src="assets/icon.png" width="112" height="112" alt="Skysight Sync 图标">
</p>

<h1 align="center">Skysight Sync</h1>
<p align="center">多台 Mac，一份电脑历史。</p>
<p align="center">
  <a href="https://github.com/ump45nose/skysight-sync/releases/latest">下载</a> ·
  <a href="#安装与首次同步">开始使用</a> ·
  <a href="#恢复原生历史">恢复原生历史</a>
</p>

每天汇集各台 Mac 的 Skysight 摘要，直接显示在 Codex 原生“电脑历史记录”中。另一台电脑不必同时在线，也不要求 Codex 正在运行。

**Go 单文件程序 · 私有 SFTP 中转 · macOS LaunchAgent**

- 原始 Markdown 按设备归档，归并正文保留所选摘要全文，不调用模型。
- 仅处理北京时间今天零点前已经结束的摘要；今天的采集继续由客户端管理。
- 默认每天北京时间 09:00 同步，登录时补做；每天最多成功一次。
- 不增加历史界面、数据库、HTTP 服务或公网端口。

## 支持范围

发布包面向 **Apple Silicon Mac（macOS arm64）**。已在两台 Mac 的 Codex `26.1007.71326` 验证原生列表、正文、更新刷新和继续采集。核心可交叉编译到 Windows，Windows 后台服务及原生兼容性尚未验证。

本工具依赖客户端当前的本地摘要格式。升级客户端后，先用 `sync --dry-run` 检查；需要退出历史页再进入才能刷新已打开的列表。本项目独立维护，与 OpenAI 无隶属关系。

## 安装与首次同步

### 1. 准备私有中转

两台 Mac 需要能通过已有 SSH 配置访问同一个 SFTP 服务器。以下示例使用 SSH 别名 `history-hub`，远端数据目录为 `/home/syncuser/.codex/skysight-sync`；请替换为自己的连接和绝对路径。

先核对服务器主机指纹，确认 `sftp history-hub` 可以登录。后台使用严格主机身份检查和非交互认证：加密私钥需要可用的 SSH Agent，程序不会保存密码或复制私钥。SFTP 服务必须支持 OpenSSH 的 `posix-rename` 扩展。

所有参与设备使用同一远端数据目录，并互相信任。每个安装使用不同、固定的设备 ID，默认是 `macair`、`macpro`；不要把同一份配置复制给另一台设备。

### 2. 在每台 Mac 安装

从 [Release](https://github.com/ump45nose/skysight-sync/releases/latest) 下载，或运行：

```sh
work="$(mktemp -d)"
curl -fL https://github.com/ump45nose/skysight-sync/releases/download/v1.0.0/skysight-sync_1.0.0_darwin_arm64.tar.gz \
  -o "$work/skysight-sync.tar.gz"
tar -xzf "$work/skysight-sync.tar.gz" -C "$work"
codex_home="${CODEX_HOME:-$HOME/.codex}"
mkdir -p "$codex_home/skysight-sync/bin"
install -m 755 "$work/skysight-sync_1.0.0_darwin_arm64/skysight-sync" \
  "$codex_home/skysight-sync/bin/skysight-sync"
export PATH="$codex_home/skysight-sync/bin:$PATH"
skysight-sync version
```

发布包附带 `SHA256SUMS`，也可单独下载校验文件核对传输完整性。程序未经过 Apple 公证。上面的 PATH 设置只影响当前终端；以后可使用程序的完整路径。

### 3. 分别配置和预览

MacAir：

```sh
skysight-sync init --device macair --ssh-target history-hub \
  --remote-dir /home/syncuser/.codex/skysight-sync
skysight-sync sync --dry-run
```

MacPro 使用同一远端目录，将 `--device` 改为 `macpro`。自定义设备列表时，两端都传入相同的 `--devices office,home`，并选择各自的 `--device`。

`init` 在已有用户 `.codex` 下新建 `skysight-sync/`，也遵循 `CODEX_HOME`。新安装必须显式提供远端目录。重新执行 `init` 会保留未指定的已有配置和安装身份。

### 4. 完成首次汇集，再启用后台

先在 MacAir 执行 `skysight-sync sync`：它会上传本机原文，并提示等待另一台的首次清单，**此时不会替换原生历史**。再在 MacPro 执行同一命令，最后回到 MacAir 重跑一次。

在两端确认原生列表与正文后，分别运行：

```sh
skysight-sync status
skysight-sync install-service
```

服务安装到当前登录用户，不需要管理员权限。Mac 的系统时区必须为 UTC+08:00，以确保 LaunchAgent 按北京时间 09:00 调度。请将程序保留在固定安装路径，不要从下载目录安装服务后再移动它。

## 日常运行与排错

```sh
skysight-sync status             # 成功时间、截止点、来源进度与错误
skysight-sync status --json      # 供脚本读取
skysight-sync sync --dry-run     # 预览上传、下载、归并及替换，不写入
skysight-sync sync               # 今天尚未成功时执行同步
skysight-sync uninstall-service  # 仅停用后台，保留展示与归档
```

后台成功静默；网络失败最多重试 3 次，间隔 15 分钟。认证、主机密钥、格式或内容冲突会直接报错。持续失败超过 24 小时后发一次系统通知，详情通过 `status` 查看，日志位于 `skysight-sync/logs/service.log`。

首次同步回填现存历史，之后也会补齐迟到摘要。两端错峰上传时，先运行的一端在后续每日同步中取得另一端新内容；取得相同来源清单后，展示窗口及摘要内容一致，本机主动删除的窗口除外。

## 摘要如何归并

```text
各端原生摘要 → 本机原文归档 → 私有 SFTP 按设备存储
                                     ↓
各端下载缺失原文 → UTC 自然 6 小时窗口归并 → 原生 resources
```

每个设备在窗口内优先选择自身完整的 6h 摘要，否则使用 10min 摘要。一台设备的 6h 不会覆盖另一台设备的 10min。

每个窗口生成一份展示文件：标题标明实际覆盖时段和设备；描述按设备连接已有描述；应用列表去重；正文按设备、时间保留所选摘要全文，并链接本机归档。所有未入选的细粒度原文仍保留在归档中。

输入变化时采用新文件名，避开客户端按路径缓存旧元数据。只有原文归档和远端提交确认成功后才替换原生文件；原子写入和发布日志支持异常后继续完成。今天和跨越截止点的 6h 留在原位，部分归并条目可能暂时与原生条目并存。

**仅同步摘要，不同步原始操作事件。** 摘要里的事件引用仍指向源设备，不能据此在另一台设备读取源设备事件。归档链接使用各自本机路径，两个设备的正文路径因此不同。

在原生界面删除归并条目，只抑制本机对应窗口，不传播至服务器或其他设备，后续更新也不自动补回。

## 恢复原生历史

```sh
skysight-sync restore-native
```

该命令停用调度、移除本工具生成的展示文件，恢复本机已归档原始摘要，保留所有归档。恢复全部本机原文也会撤销本机窗口删除抑制。验证阶段曾登记来源的导入样本会恢复到接管前位置。

需要重新启用时：

```sh
skysight-sync init --resume
skysight-sync sync --dry-run
skysight-sync sync
skysight-sync install-service
```

不要删除配置或状态文件来“修复同步”：它们保存安装身份、来源和已发布窗口。状态损坏时先停用调度、保留工作目录，再根据错误定位。

## 数据位置与安全

本机仅接管 `CODEX_HOME/memories/extensions/skysight/resources/` 中符合截止条件的摘要，不同步整个 `.codex`。

```text
$CODEX_HOME/skysight-sync/
  bin/skysight-sync
  config.json
  state.json
  pending.json                 # 仅发布尚未完成时存在
  archive/<设备>/objects/<版本>/<原始文件名>
  logs/service.log

<远端数据目录>/devices/<设备>/
  objects/<版本>/<原始文件名>
  manifest.json                # 每批最后原子提交
```

摘要含私人活动信息，请使用自己的可信服务器。目录以 0700、文件以 0600 写入；传输由 SSH 加密，磁盘上的 Markdown 不额外加密。配置、状态、日志和归档都不应提交到公开仓库。安全问题请使用 [私密漏洞报告](https://github.com/ump45nose/skysight-sync/security/advisories/new)。

## 从源码构建

需要 Go 1.27.1 或更新版本：

```sh
go test -race ./...
go vet ./...
go build -trimpath -o dist/skysight-sync ./cmd/skysight-sync
./scripts/package.sh
```

打包脚本生成 macOS arm64 压缩包和用于下载校验的 `SHA256SUMS`，包含程序、使用说明、许可证、依赖声明和 PNG / ICNS 图标。`validation/` 是开发时使用的审核、归并预览和已有客户端调试连接辅助工具，不安装为后台能力。

自动测试覆盖跨时区与跨午夜、迟到记录、重叠归并、来源归属、重复执行、中断恢复、离线补做、本机删除、恢复、格式错误、冲突、运行锁，以及重新初始化时保留已有配置。

## 许可证与图标

[MIT](LICENSE)。应用图标及生成提示词见 [assets](assets/README.md)，依赖声明见 [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt)。
