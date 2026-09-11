#!/bin/sh
# 容器启动收尾：把 /data 的归属弄对，再降权运行。
#
# 为什么需要这一步：
# bind mount（-v /宿主/路径:/data）进来的目录归属是**宿主机**那边的 uid，
# 而容器里跑的是非 root 用户。不处理的话第一件事就是
# "mkdir /data/cache: permission denied"，程序根本起不来 ——
# 而这个目录里装着数据库、加密密钥和日志，写不进去就等于数据全丢。
#
# 用 PUID/PGID 覆盖默认 uid，和常见的自建服务镜像一个习惯；
# 也可以直接 docker run --user，那样这段会自动跳过。
set -e

APP=/usr/local/bin/ovh-server
DATA="${DATA_DIR:-/data}"

if [ "$(id -u)" = "0" ]; then
  PUID="${PUID:-10001}"
  PGID="${PGID:-10001}"

  # 组/用户可能已存在（重启同一个容器），存在就沿用
  if ! getent group "$PGID" >/dev/null 2>&1; then
    addgroup -g "$PGID" ovh 2>/dev/null || true
  fi
  if ! getent passwd "$PUID" >/dev/null 2>&1; then
    adduser -D -H -u "$PUID" -G "$(getent group "$PGID" | cut -d: -f1)" ovh 2>/dev/null || true
  fi

  mkdir -p "$DATA"
  # 只在归属不对时才 chown：数据多了之后递归 chown 很慢，
  # 每次启动都来一遍会让重启变成几十秒。
  CURRENT_UID="$(stat -c %u "$DATA" 2>/dev/null || echo -1)"
  if [ "$CURRENT_UID" != "$PUID" ]; then
    echo "[entrypoint] 把 $DATA 的归属改成 $PUID:$PGID（首次挂载或 PUID 变了）"
    chown -R "$PUID:$PGID" "$DATA" || {
      echo "[entrypoint] ⚠️ chown 失败。如果 $DATA 是只读挂载或网络文件系统，"
      echo "[entrypoint]    请改用 docker run --user $(id -u):$(id -g)，或把宿主目录 chown 成 $PUID:$PGID"
    }
  fi

  exec su-exec "$PUID:$PGID" "$APP" "$@"
fi

# 已经是非 root（用了 --user）：不做任何归属处理，直接跑。
# 这时候宿主目录的写权限得由调用方保证 —— 我们没有能力改。
if [ ! -w "$DATA" ]; then
  echo "[entrypoint] ⚠️ $DATA 当前用户($(id -u):$(id -g))写不进去。"
  echo "[entrypoint]    数据库、加密密钥、日志都要写在这里，起不来是必然的。"
  echo "[entrypoint]    把宿主目录 chown 成这个 uid，或者去掉 --user 让容器自己处理。"
fi
exec "$APP" "$@"
