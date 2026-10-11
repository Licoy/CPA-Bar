# AGENTS.md

给在本仓库工作的代码助手和贡献者的约定。

## 文档

- 一切以代码为准。业务逻辑、实现细节、接口调用方式、数据字段、内部策略（如采集、重试、缓存、限流）只写在代码和代码注释里，不写进任何文档。
- 不新增 `docs/` 目录，也不新增设计说明、架构说明、发布手册之类的独立文档。不维护 `CHANGELOG.md`，版本说明由 GitHub Release 自动生成。
- README 只面向用户：程序是什么、主要功能（一句话一项，不解释原理）、截图、系统要求、安装、快速开始、从源码构建、致谢。
- README 用到的截图放在根目录的 `screenshots/` 中，两份 README 共用同一套截图。
- README 分为英文 `README.md`（主文件）和简体中文 `README-ZH_CN.md`，两者内容和结构保持一致：修改其中一个时必须同步修改另一个，顶部保留相互跳转的语言链接。
- 新增了需要致谢的第三方项目（运行依赖的服务、框架、图标库等）时，在两份 README 底部的「致谢 / Acknowledgements」中补上链接。
- 本文件只写协作约定（代码规范、资源来源、文档规则），不写业务逻辑。

## 图标

### 界面图标：只用 Remix Icon

- 程序中所有界面图标（侧栏、按钮、卡片标题、状态等）都必须来自 [Remix Icon](https://remixicon.com/)（Apache 2.0），不要手绘 SVG，也不要混用其他图标库（Lucide、Heroicons、SF Symbols 等）。
- 图标由 `scripts/gen-glyphs.py` 从 npm 包 `remixicon` 生成到 `internal/view/glyphs_gen.go`，这个文件不要手改。
- 新增图标：在脚本的 `REMIX` 表里加一行“Go 变量名: Remix 图标名”（例如 `"icBack": "arrow-left-s-line"`），然后运行 `python3 scripts/gen-glyphs.py`。图标名可在 remixicon.com 上搜索，优先用 `-line` 风格，与现有图标保持一致。
- 图标以 `ui.Icon(c, icXxx)` 显示，颜色跟随文字颜色。

### 服务商图标：只用 LobeHub Icons

- 服务商（Claude、Codex、Gemini、OpenAI 等）的标志都必须来自 [LobeHub Icons](https://icons.lobehub.com/)（MIT），由同一个脚本从 npm 包 `@lobehub/icons-static-svg` 复制到 `internal/view/assets/providers/`。
- 有彩色版（`<name>-color`）的用彩色版，没有的用单色版；单色版以 `currentColor` 绘制，跟随文字颜色。
- 新增服务商：在脚本的 `LOBE` 表里加上文件名并运行脚本，再在 `internal/view/providers.go` 的 `providerLogoFiles` 里把服务商 ID 映射到该文件。LobeHub 没有的服务商显示 Remix 的 `plug-line` 作为占位，不要自行绘制标志。
- 凡是显示服务商或账号的地方都用 `ProviderIcon` / `providerTile` / `credTile` 显示服务商标志。

### 应用标志

- 应用标志、托盘图标和 Dock 图标来自 `resources/` 下的 SVG，由 `scripts/gen-icons.sh` 渲染，不属于上面两类。
