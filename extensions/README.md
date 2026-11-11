# 扩展

dsh-ee 的扩展把企业自己的系统接进来（设计见 [docs/plugins.md](../docs/plugins.md)）。SDK、示例、规格和技能都在这个目录下，和服务端一起改、一起测。

| 目录 | 内容 |
|---|---|
| [examples/demo-declarative](./examples/demo-declarative) | 声明式扩展的示例：清单 + OpenAPI 文档，不写代码。复制它就能开始 |
| [official/ee-jwt-adapter](./official/ee-jwt-adapter) | `ee_jwt` 适配层样例：放在企业系统前面，验 dsh-ee 签的身份令牌，换成系统自己认的身份。给企业系统那一侧用，见 [docs/ee-jwt.md](../docs/ee-jwt.md) |
| [schemas](./schemas) | 机器能读的规格：清单的 JSON Schema |
| [skills/dsh-ee-extension-dev](./skills/dsh-ee-extension-dev/SKILL.md) | 给 Agent 的技能：照着做就能写出一个通过检查的扩展 |

写完用 `dsh-ee extension check <目录>` 检查，`--json` 给程序和 Agent 看。规格用 `dsh-ee schema list` / `dsh-ee schema get <名字>` 查，离线可用。

## 安装

声明式扩展在管理后台「企业连接 → 安装扩展」里装：上传扩展包（zip），或直接选清单和接口文档两个文件。安装前页面会列出请求发往哪里、用谁的身份、有哪些工具、哪些会改数据、哪些参数由管理员统一设置，管理员确认之后才装上。

装好的扩展就是一个企业连接，和手工建的连接不同的地方都由清单决定，保存时每次从清单重新推出来：

- 请求只发往清单 `servers` 里的地址，凭据只带给它；
- 用谁的身份照 `credential`（`ee_jwt` / `user_secret` / `shared`）；
- 只有清单标了 `readOnly` 的工具算只读，写操作装好后默认不启用；
- `fill` 是 `admin` / `app` 的参数不出现在给模型的工具定义里，调用时由 dsh-ee 补上，模型自己带了也不算；
- 工具设了 `keep` 的，返回给模型时只留这些字段；
- 每次调用都记下扩展的「身份@版本」和清单哈希。

升级：在连接页面点「扩展设置 / 升级」上传新版本。新出现的写操作、从只读变成写操作的工具先不启用；连接名不变，已经用了这些工具的应用不受影响。一个连接不能换成另一个扩展。

这个版本还没有的：按应用分别设置 `fill: app` 的参数（现在和 `admin` 一样由管理员统一设置）、多份共享凭据、从 Git 仓库安装、清单里的 `config` 表单、附带应用的导入。服务扩展、端侧扩展和 SDK 在后面的批次（docs/plugins.md §12）。
