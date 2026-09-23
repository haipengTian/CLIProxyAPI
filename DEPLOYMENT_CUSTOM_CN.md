# CLIProxyAPI Fork 维护与 Linux 部署

本文档适用于以下场景：

- `origin` 是自己的 GitHub Fork。
- `upstream` 是原作者仓库 `router-for-me/CLIProxyAPI`。
- 自己会长期修改代码，同时需要持续合并原作者更新。
- Linux 服务器使用 Docker Compose 部署自己构建的版本。

## 1. 分支和远程仓库

推荐固定使用以下结构：

- `upstream/main`：原作者的最新代码，只读。
- `origin/main`：自己 Fork 中的上游镜像，不放自定义修改。
- `origin/custom/main`：自己的长期部署分支。
- 功能分支：从 `custom/main` 创建，完成后合并回 `custom/main`。

首次设置开发机仓库：

```bash
git remote add upstream https://github.com/router-for-me/CLIProxyAPI.git
git fetch upstream --prune
git switch main
git merge --ff-only upstream/main
git push origin main
git switch -c custom/main
git push -u origin custom/main
```

不要直接在 `main` 上开发，也不要直接修改服务器中的代码。服务器只拉取已经提交并推送到 `origin/custom/main` 的版本。

## 2. 同步原作者更新

在开发机执行：

```bash
bash ./deploy/sync-upstream.sh custom/main
```

脚本会：

1. 拉取 `origin` 和 `upstream`。
2. 将本地 `main` 快进到 `upstream/main`。
3. 切换到 `custom/main` 并合并 `main`。
4. 遇到冲突时停止，由开发者解决，不会自动推送。

处理冲突并完成检查后推送：

```bash
git push origin main
git push -u origin custom/main
```

合并上游后重点检查这些文件的变化：

```bash
git diff <升级前提交>..<升级后提交> -- \
  config.example.yaml docker-compose.yml Dockerfile
```

`config.yaml`、`auths/`、`logs/` 和 `plugins/` 都是运行数据，不应被上游文件覆盖。尤其不要直接执行：

```bash
cp config.example.yaml config.yaml
```

应该对比新增配置项，再手工合并到现有 `config.yaml`。

## 3. Linux 首次部署

服务器需要安装 Git、Docker Engine、Docker Compose Plugin 和 curl。推荐让 Nginx 或 Caddy 对外提供 HTTPS，CPA 只监听本机地址。

克隆自己的部署分支：

```bash
git clone --branch custom/main https://github.com/<你的账号>/CLIProxyAPI.git
cd CLIProxyAPI
bash ./deploy/cpa.sh init
```

然后编辑：

```bash
vi config.yaml
vi deploy/.env
```

至少确认 `config.yaml` 中：

```yaml
remote-management:
  allow-remote: true
  secret-key: "替换成足够长的随机密码"
```

Docker 网桥访问在容器内通常不属于 localhost，所以使用管理接口时需要 `allow-remote: true`。同时必须由反向代理阻止公网访问管理页面和管理 API。

首次构建并启动：

```bash
bash ./deploy/cpa.sh deploy
bash ./deploy/cpa.sh status
bash ./deploy/cpa.sh logs
```

脚本会使用当前 Git commit 构建不可变镜像，例如：

```text
cli-proxy-api:custom-673131f5abcd
```

镜像构建成功后才会切换容器，并通过 `http://127.0.0.1:8317/healthz` 检查服务。失败时会恢复升级前的部署状态。

## 4. Nginx 入口

生产 Compose 默认只把 API 和 OAuth 回调端口绑定到 `127.0.0.1`。Nginx 的核心配置可以按下面处理：

```nginx
location = /management.html {
    return 404;
}

location = /v0/management {
    return 404;
}

location ^~ /v0/management/ {
    return 404;
}

location / {
    proxy_pass http://127.0.0.1:8317;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 3600s;
}
```

HTTPS 证书和域名配置由 Nginx/Caddy 管理，公网防火墙只开放 `80/443`，不要开放 `8317` 和 OAuth 回调端口。

管理页面或 OAuth 登录需要本地访问时，使用 SSH 隧道：

```bash
ssh \
  -L 8317:127.0.0.1:8317 \
  -L 1455:127.0.0.1:1455 \
  -L 54545:127.0.0.1:54545 \
  -L 51121:127.0.0.1:51121 \
  user@server
```

## 5. 自定义代码升级

先在开发机完成代码修改、提交并推送到 `origin/custom/main`。然后在服务器仓库中执行：

```bash
bash ./deploy/cpa.sh upgrade
```

`upgrade` 只允许当前分支与 `deploy/.env` 中的 `CPA_DEPLOY_BRANCH` 一致，并使用 `--ff-only` 拉取，避免服务器产生本地合并提交。之后会按新 commit 构建镜像、重建容器并执行健康检查。

查看当前和上一个部署版本：

```bash
bash ./deploy/cpa.sh status
bash ./deploy/cpa.sh images
```

不要在升级成功后立刻运行 `docker image prune -a`。至少保留当前镜像和前一个镜像，才能快速回滚。

## 6. 回滚

升级后发现业务问题时执行：

```bash
bash ./deploy/cpa.sh rollback
```

脚本会切换到上一个 commit 对应的本地镜像，并再次执行健康检查。回滚只切换程序镜像，不会回滚以下持久化数据：

- `config.yaml`
- `auths/`
- `logs/`
- `plugins/`

因此，在进行不兼容的配置或数据结构修改前，应单独备份这些目录。

## 7. 日常操作顺序

推荐固定为以下流程：

```text
开发机同步 upstream
  -> 合并到 custom/main
  -> 解决冲突并检查配置变化
  -> 提交并推送 origin/custom/main
  -> 服务器执行 bash ./deploy/cpa.sh upgrade
  -> 检查 healthz、日志和实际 API 请求
  -> 出现问题执行 bash ./deploy/cpa.sh rollback
```

这套方式把 Git 提交、Docker 镜像和线上版本一一对应，能够明确知道服务器运行的是哪次修改，也不会因为 `latest` 标签变化而失去回滚依据。
