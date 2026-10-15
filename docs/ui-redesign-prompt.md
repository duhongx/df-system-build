# UI 视觉参考生成记录

本次用户指定 `gpt-image-2`。本机未配置 `OPENAI_API_KEY`；内置图像生成工具不能显式选择或确认模型型号。内置工具尝试两次，均因网络错误失败，未生成参考图片。

页面按下列视觉规格直接在 Vue/CSS 中实现，实际浏览器截图与 AI 生成图分别记录，不将截图称为生成模型的输出。

## 提交给内置工具的提示词

```text
Use case: ui-mockup
Asset type: high-fidelity desktop web application redesign reference.
Primary request: redesign the existing Chinese CloudHIS operations platform UI to feel technological, exceptionally clean and professional. This is a functional enterprise tool for deploying microservices, SQL execution, Kubernetes and downloading server logs.
Composition: a polished straight-on full desktop screenshot at 1600x1000 aspect ratio. Left sidebar approximately 220 px, top header 68 px, main panel with generous 32 px spacing. No surrounding monitor or mockup frame.
Style: refined contemporary SaaS console, mostly white and very pale ice gray-blue, dark navy text, elegant cyan and electric cobalt accents, thin lines, precision typography, restrained 10-14px corner radii, minimal soft shadows. Quiet technology aesthetic inspired by well-crafted developer tooling. Subtle hairline technical grid or tiny node accent ONLY in the page masthead, not behind tables. Readable dense data; no neon black cyberpunk or busy decorative widgets.
Sidebar: CloudHIS logo text with a small geometric cloud mark. Chinese subtitle 智能管理平台. Small section labels 运维管理 / 构建与交付 / 平台配置. Navigation entries 工作台, 服务器管理, 日志收集 (selected with a pale blue rounded active background and narrow cyan accent), Kubernetes, 任务中心, 制品管理, PostgreSQL 管理, 部署管理, 系统设置. White sidebar. Small bottom help/documentation area, no fake health status.
Main header: small breadcrumb 运维管理 / 日志收集, on right subtle theme and notification controls, admin avatar.
Main content: tiny uppercase label LOG COLLECTION, large title 日志收集, subtitle 汇集分散的日志，保留每一份排查线索. Buttons 新增来源 and blue 选择日志文件. Three restrained summary metrics from configured sources: 日志来源 8, 覆盖主机 4, 日志类型 4. These are sample data only.
Below a large white table card: tabs 日志来源 (selected) and 收集记录, toolbar with search 来源名称、主机或目录 and filter 全部类型. Table columns checkbox, 来源名称, 类型, 所在主机, 日志目录, 文件规则, 操作. 5 realistic Chinese rows: 计费服务 / 微服务 / 10.0.0.11 / /data/logs/billing; PostgreSQL 主库 / 数据库 / 10.0.0.21 / /data/postgresql/log; RabbitMQ / RabbitMQ / 10.0.0.31 / /var/log/rabbitmq; Redis / Redis / 10.0.0.41 / /var/log/redis; HIS 网关 / 微服务 / 10.0.0.11 / /data/logs/gateway. Small square category icons with pale tints. File rules *.log, *.gz. Row actions 收集 编辑 更多. Footer says 按来源选择文件 · 收集后打包为 ZIP.
Constraints: Chinese text carefully legible; real controls, sensible alignment and clean information hierarchy. No line charts, fake uptime indicators, invented monitoring dashboards, terminal blocks, large sci-fi artwork, glossy 3D objects or gradient wallpaper. Preserve pragmatic operations workflow. Render one single complete screen.
```
