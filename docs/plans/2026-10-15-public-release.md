# Skysight Sync 首次公开发布

## 范围与决定

- 为已完成的后台同步工具制作独立图标，提供透明 PNG 与 macOS ICNS。
- 在 `ump45nose/skysight-sync` 创建公开仓库，采用用户确认的 MIT 许可证。
- 发布 `v1.0.0`：macOS arm64 程序、说明、许可证、依赖声明及图标。
- 保留既有同步逻辑、数据语义和运行中的服务；不新增 GUI，不扩展 Windows 部署。
- 新安装显式指定远端目录；已有安装及 `init --resume` 保留原配置。
- 私有验证记录和原始开发提交留在本地，公开分支从脱敏后的独立根提交开始；只推送公开分支与版本标签。

## 验收

- 实际查看图标及小尺寸图形，检查 PNG 透明度、尺寸和 ICNS 导出。
- CLI 新安装缺少远端目录时报错；已有配置的重新初始化和恢复入口保持兼容。
- `go test -race ./...`、`go vet ./...` 与 macOS arm64 打包通过。
- 审核公开树、提交祖先及发布包：无真实历史、个人机器地址、凭据或本地部署路径。
- 仓库确为 public；Release 正式发布；下载发布包执行版本命令，核对公开分支与标签。

## 进度

- 已完成原生跨端兼容性验证与两台 Mac 的后台部署；真实资料仅本地保留。
- 图标已完成并实际检查 32px / 128px 显示；PNG 为 1024px 且含透明通道，ICNS 可解出全部标准与 Retina 尺寸。
- `go test -race ./...` 与 `go vet ./...` 通过；新安装与已有配置兼容测试通过。
- macOS arm64 发布包构建成功，版本命令返回 `1.0.0`；已核对压缩包目录、源码与二进制中的私人信息。
- 公开仓库已建立：<https://github.com/ump45nose/skysight-sync>，默认分支 `main`，许可证识别为 MIT；公开历史从独立根提交开始，远端只有公开分支与版本标签。
- GitHub CI 的测试、vet 和 macOS arm64 打包全部通过：<https://github.com/ump45nose/skysight-sync/actions/runs/38726671928>。
- `v1.0.0` 已正式发布：<https://github.com/ump45nose/skysight-sync/releases/tag/v1.0.0>，包含程序压缩包、PNG、ICNS 和下载校验文件。
- 已从 Release 下载全部附件并通过传输完整性校验，实际执行发布包中的程序，版本返回 `1.0.0`。
- 已启用 GitHub 私密漏洞报告。本轮图标、公开仓库和版本发布均已完成。
