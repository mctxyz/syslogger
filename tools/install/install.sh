#!/usr/bin/env bash
set -euo pipefail

work_dir=

# cleanup 只清理本次下载目录，退出和信号中断共用这一生命周期。
cleanup() {
    if [[ -n "$work_dir" ]]; then
        rm -rf -- "$work_dir"
    fi
}

die() {
    printf '安装失败：%s\n' "$*" >&2
    exit 1
}

# prompt 从控制终端读取，避免 curl 管道占用标准输入后吞掉交互内容。
prompt() {
    printf '%s' "$1" >&2
    if ! IFS= read -r -u 3 REPLY; then
        die '未能读取交互输入，已停止。'
    fi
    REPLY=${REPLY:-$2}
}

main() {
    if [[ $# -ne 0 ]]; then
        case "$1" in
            -h|--help)
                [[ $# -eq 1 ]] || die '帮助选项不接受其他参数。'
                printf '%s\n' \
                    '用法：bash install.sh' \
                    '下载最新 GitHub Release，交互安装并运行 mct-syslogger init。' \
                    '支持 Linux amd64/arm64，需要交互终端、curl、GNU tar/coreutils、awk、getent，以及 root 或 sudo。' \
                    '安装前缀默认 /usr/local；运行账户必填；journal 持久化容量默认 5G。'
                return
                ;;
            *)
                die '不支持此参数，使用 --help 查看用法。'
                ;;
        esac
    fi

    local dependency arch
    for dependency in uname id curl tar sha256sum awk mktemp mkdir rm install getent; do
        command -v "$dependency" >/dev/null || die "缺少依赖：$dependency，请先安装。"
    done
    [[ $(uname -s) == Linux ]] || die '仅支持 Linux。'
    case "$(uname -m)" in
        x86_64|amd64)
            arch=amd64
            ;;
        aarch64|arm64)
            arch=arm64
            ;;
        *)
            die "不支持的 CPU 架构：$(uname -m)"
            ;;
    esac

    local -a elevate=()
    if [[ $(id -u) != 0 ]]; then
        command -v sudo >/dev/null || die '初始化需要 root，请先安装 sudo 或由 root 运行。'
        elevate=(sudo)
    fi
    if ! { exec 3</dev/tty; } 2>/dev/null; then
        die '需要交互终端，请在终端中运行脚本。'
    fi

    work_dir=$(mktemp -d "${TMPDIR:-/tmp}/mct-syslogger-install.XXXXXX")
    trap cleanup EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM

    local repository=https://github.com/mctxyz/syslogger
    local release_url tag download_url package asset expected candidate relative
    local -a curl_options=(--fail --location --silent --show-error --proto '=https' --proto-redir '=https')
    # 固定 latest 重定向后的 tag，保留 URL 编码，压缩包和校验清单共用该版本。
    release_url=$(curl "${curl_options[@]}" --output /dev/null --write-out '%{url_effective}' "$repository/releases/latest")
    case "$release_url" in
        "$repository/releases/tag/"*)
            tag=${release_url#"$repository/releases/tag/"}
            ;;
        *)
            die '无法解析最新 Release 的固定版本地址。'
            ;;
    esac
    [[ -n "$tag" ]] || die 'Release 版本为空。'
    download_url="$repository/releases/download/$tag"
    package="mct-syslogger_linux_$arch"
    asset="$package.tar.gz"
    printf '下载 %s（linux/%s）…\n' "$tag" "$arch"
    curl "${curl_options[@]}" --output "$work_dir/$asset" "$download_url/$asset"
    curl "${curl_options[@]}" --output "$work_dir/SHA256SUMS" "$download_url/SHA256SUMS"

    # 只校验当前资产，拒绝缺失、重复或异常校验项，避免读取清单中的其他路径。
    expected=$(awk -v name="$asset" '$2 == name || $2 == "./" name { print $1 }' "$work_dir/SHA256SUMS")
    [[ "$expected" =~ ^[[:xdigit:]]{64}$ ]] || die 'SHA256SUMS 缺少当前架构的唯一有效校验值。'
    if ! (
        cd "$work_dir"
        printf '%s  %s\n' "$expected" "$asset" | sha256sum --check --status
    ); then
        die '压缩包 SHA-256 校验失败，未安装或初始化。'
    fi

    local -a files=(mct-syslogger tools/README.md tools/THIRD_PARTY_NOTICES.md docs/journal.md)
    local -a members=()
    for relative in "${files[@]}"; do
        members+=("$package/$relative")
    done
    mkdir -p "$work_dir/unpacked"
    tar --no-same-owner --no-same-permissions -xzf "$work_dir/$asset" \
        -C "$work_dir/unpacked" -- "${members[@]}"
    for relative in "${files[@]}"; do
        [[ -f "$work_dir/unpacked/$package/$relative" && ! -L "$work_dir/unpacked/$package/$relative" ]] ||
            die "发布包中的 $relative 必须是普通文件。"
    done
    candidate="$work_dir/unpacked/$package/mct-syslogger"
    if ! "$candidate" init --help >"$work_dir/init-help.txt" 2>&1; then
        die '发布包无法运行 init --help，请检查架构及 Release 是否包含 init 子命令。'
    fi

    local prefix runtime_user capacity target doc_dir status
    while true; do
        prompt '安装前缀 [/usr/local]（命令放入其 bin/）：' '/usr/local'
        prefix=$REPLY
        while [[ "$prefix" == */ ]]; do
            prefix=${prefix%/}
        done
        if [[ "$prefix" == /* ]]; then
            break
        fi
        printf '请输入绝对安装路径，且不能使用系统根目录。\n' >&2
    done
    while true; do
        prompt '已有程序运行账户（必填）：' ''
        runtime_user=$REPLY
        if [[ -n "$runtime_user" ]]; then
            if getent -- passwd "$runtime_user" >/dev/null; then
                break
            fi
        fi
        printf '请输入一个已存在的运行账户。\n' >&2
    done
    prompt '专用 journal 持久化总容量 [5G]（正整数字节或 K/M/G/T/P/E）：' '5G'
    capacity=$REPLY
    target="$prefix/bin/mct-syslogger"
    doc_dir="$prefix/share/doc/mct-syslogger"
    printf '\n版本：%s（linux/%s）\n命令：%s\n文档：%s\n运行账户：%s\njournal 容量：%s\n' \
        "$tag" "$arch" "$target" "$doc_dir" "$runtime_user" "$capacity"
    printf '%s\n' \
        '将更新上述命令和文档，并以 root 运行 init：调整目录组权限、应用配置、重启专用 journald 实例。' \
        '需要 Linux、systemd 245+ 及 init 所需系统工具；账户、权限和容量规则由 init 校验。' \
        '首次迁移前请停止旧版业务进程；缩小容量可能清理旧归档。默认系统 journal 保持原样。'
    prompt '确认安装并初始化？[y/N]：' 'n'
    case "$REPLY" in
        y|Y|yes|YES)
            ;;
        *)
            printf '已取消，未安装或初始化。\n'
            return
            ;;
    esac

    # 系统写入集中在确认之后，主机权限和 journal 配置继续由 init 统一管理。
    "${elevate[@]}" install -d -m 0755 -- "$prefix/bin" "$doc_dir/tools" "$doc_dir/docs"
    for relative in tools/README.md tools/THIRD_PARTY_NOTICES.md docs/journal.md; do
        "${elevate[@]}" install -m 0644 -T -- "$work_dir/unpacked/$package/$relative" "$doc_dir/$relative"
    done
    "${elevate[@]}" install -m 0755 -T -- "$candidate" "$target"
    if "${elevate[@]}" "$target" init "--user=$runtime_user" "--journal-max-use=$capacity"; then
        printf '安装及初始化完成：%s\n请重新登录或重启业务服务以刷新组身份；日志读取权限仍由管理员管理。\n' "$target"
    else
        status=$?
        printf '命令已安装到 %s，但初始化失败；已完成步骤保留，请修正原因后重跑 init。\n' "$target" >&2
        return "$status"
    fi
}

main "$@"
