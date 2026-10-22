# 安装面板

支持 Debian / Ubuntu、amd64 / arm64，使用原生 systemd。

## 本地安装包

将 `install-panel.sh` 与对应架构的安装包放在同一目录：

```bash
sudo bash install-panel.sh --package ./nekopass-panel-linux-amd64.tar.gz
```

ARM64 使用 `nekopass-panel-linux-arm64.tar.gz`。

## 网络安装

从 GitHub Releases 下载：

```bash
wget https://github.com/musicclub1563/nekopass/releases/latest/download/install-panel.sh -O install-panel.sh && sudo bash install-panel.sh
```

安装器可选择系统源或 PostgreSQL 官方源及数据库主版本，并创建数据库、配置 systemd、生成管理员随机密码和开启自启。支持 PostgreSQL 14 或以上。

| 默认入口 | 端口 |
| --- | --- |
| 网页 HTTP | `8080` |
| 节点控制连接 | `9443` |

打开 `http://服务器地址:8080`。云服务器还需放行安全组端口。

节点连接可选 `plain`（测试明文 HTTP/2）、`existing`（已有公共 CA 证书）或 `proxy`（Nginx 终止 TLS）；默认 `plain`，不生成自签 CA。网页 HTTPS 使用 [反向代理](./proxy.md)。

常用参数：

| 参数 | 用途 |
| --- | --- |
| `--host` | 用户和节点能访问的域名 / IP。 |
| `--http-port` / `--grpc-port` | 网页和节点控制监听端口。 |
| `--agent-host` / `--agent-port` | 节点连接的外部地址 / 端口，适用于代理或 NAT。 |
| `--postgres-source` / `--postgres-version` | 数据库来源和主版本。 |
| `--admin` / `--site-name` | 初始管理员和站点名称。 |
| `--reinstall` | 清空该服务的面板数据库并全新安装。 |
| `--yes` | 使用参数与默认值，不交互询问。 |
| `--dry-run` | 只验证参数，不安装。 |

全部参数：`bash install-panel.sh --help`。忘记密码见 [日常管理](./manage.md#重置管理员密码)。

## 重新安装

默认以 `control.env` 是否存在判断已有安装；配置不存在时允许重跑，复用同名数据库并保留已有管理员。

清空数据库并全新安装：

```bash
sudo bash install-panel.sh --reinstall
```

**此操作会删除所选 PostgreSQL 实例中该服务的全部面板数据，包括用户、余额、订单、套餐、节点和规则，并重新生成管理员密码。** 执行前自行备份。交互时输入服务名确认；加 `--yes` 会直接执行，不再确认。

只处理 `--service-name` 指定的面板，默认 `nekopass`；同机节点端的配置与状态保留。原节点密钥会随面板数据库清空，节点需在新面板重新添加并配置密钥。
