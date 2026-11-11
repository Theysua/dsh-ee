# 声明式扩展示例：IT 工单

复制它就能开始。一个声明式扩展只有两个文件：

| 文件 | 作用 |
|---|---|
| `dsh-ee.extension.json` | 清单：身份和版本、请求能发往哪些地址、用谁的凭据、哪些工具是只读的、参数由谁填 |
| `openapi.yaml` | 对方系统的接口文档（OpenAPI 3.x）。工具名就是每个接口的 `operationId` |

不写代码，不部署任何东西：dsh-ee 读接口文档，自己发请求。

这个示例连的是 dsh-ee 自带的示例工单系统（一键导入示例公司后在 `/demo/itsm/` 下），所以 `servers` 写的是本机的 dsh-ee。

## 检查

在仓库根目录下（`dsh-ee` 不在 PATH 里就写它的完整路径）：

```bash
dsh-ee extension check extensions/examples/demo-declarative
dsh-ee extension check extensions/examples/demo-declarative --json   # 给 Agent 看
```

全部通过才算合格。每条结果都带位置（`where`）和改法（`fix`），照着改完再跑一遍。

## 改成你自己的

1. 把 `openapi.yaml` 换成你们系统的接口文档。没有 `operationId` 的接口补上，它就是工具名。
2. 改 `id`（`作者/名字`）、`name`、`description`。
3. `servers` 写你们系统的地址。dsh-ee 只往这些地址发请求。
4. `credential.mode` 选凭据模式：对方能验 JWT 用 `ee_jwt`，员工各有 token 用 `user_secret`，只有一份公共凭据用 `shared`。对方要求凭据放在特定的请求头里，写 `credential.header`（示例里是 `X-Demo-Key`）。见 [docs/ee-jwt.md](../../../docs/ee-jwt.md)。
5. `tools` 里把只读的工具标上 `"readOnly": true`。**没标的一律当写操作**，调用前要员工确认；不要把会改数据的接口标成只读。
6. `tools[].params` 里只写你要补充或限制的参数。示例里有两种写法：
   - `body.category`：请求体里的字段写成 `body.<字段名>`，这里给它限定了可选的值；
   - `body.priority`：`"fill": "admin"` 表示这个值由管理员安装时统一设置，模型看不到也改不了，`default` 是预先填好的值。
7. 接口文档里的说明写得不好时，在 `tools[].description` 里改写成模型看得懂的话；知道返回里有哪些字段、又嫌太多时，用 `keep` 只留需要的。示例里工单列表的返回是 `{"items": [...]}`，所以写成 `items.id`、`items.title`……（列表没被包一层时直接写 `id`、`title`）。

字段的完整说明：`dsh-ee schema get manifest`。检查项的列表：`dsh-ee schema get errors`。

## 安装

管理后台「企业连接 → 安装扩展」，把这个目录压成 zip 拖进去（也可以直接选 `dsh-ee.extension.json` 和 `openapi.yaml` 两个文件）。页面会先给你看：请求发往哪里、用谁的身份、有哪些工具、哪些会改数据、哪些参数由你统一设置，确认之后才装。

装好之后它就是一个企业连接：可见范围、工具开关、按角色限定都在连接页面上管。写操作（这里的 `create_ticket`）默认不启用，要你逐个打开。以后拿到新版本，在连接页面点「扩展设置 / 升级」上传新的包。

想在本机直接试这个示例（它连的是 dsh-ee 自带的示例工单系统）：

- dsh-ee 要跑在 `http://127.0.0.1:8080`，因为清单的 `servers` 写的是这个地址；
- 不要先「一键导入示例公司」，它已经带了一个同名的连接；
- 连接名用默认的 `demo-itsm`，共享凭据随便填一个值（示例工单系统只认这个连接自己存的那份凭据；它放在哪个请求头，清单的 `credential.header` 已经写了）。
