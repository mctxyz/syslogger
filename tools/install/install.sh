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
    local initialize=false
    if [[ $# -ne 0 ]]; then
        [[ $# -eq 1 ]] || die '仅支持单独使用 --init 或 --help。'
        case "$1" in
            --init)
                initialize=true
                ;;
            -h|--help)
                printf '%s\n' \
                    '用法：bash install.sh [--init]' \
                    '默认下载并校验最新 GitHub Release，交互安装或更新命令及配套文档，保持主机日志配置原样。' \
                    '--init  额外询问四项容量并执行初始化：重新生成受管 journal 配置，覆盖其中的手动修改，重启四个专用 journald 实例。' \
                    '首次安装默认也只安装程序，需要配置日志接收入口时显式使用 --init，或以 root 运行 mct-syslogger init。' \
                    '支持 Linux amd64/arm64，需要交互终端、curl、GNU tar/coreutils、awk，以及 root 或 sudo。' \
                    '安装前缀默认 /usr/local；--init 另需 systemd 245+、groupadd 及 init 所需系统工具，创建 root:mct-syslogger、2775 的名称根目录，四空间容量默认 1G/2G/1G/1G。'
                return
                ;;
            *)
                die '不支持此参数，使用 --help 查看用法。'
                ;;
        esac
    fi

    local dependency arch
    for dependency in uname id curl tar sha256sum awk mktemp mkdir rm install; do
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
        command -v sudo >/dev/null || die '安装需要 root，请先安装 sudo 或由 root 运行。'
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
    local -a groups=(debug info warning error) capacities=(1G 2G 1G 1G) probe_args=(init)
    local index
    for index in "${!groups[@]}"; do
        probe_args+=("--journal-${groups[$index]}-max-use=${capacities[$index]}")
    done
    # 参数必须置于帮助之前，防止 main 的脚本先于配套 Release 发布时覆盖已有安装。
    if ! "$candidate" "${probe_args[@]}" --help >"$work_dir/init-help.txt" 2>&1; then
        die '发布包不支持四个等级空间的 init 参数或无法运行，请等待配套 Release 并检查架构。'
    fi
    # 初始化帮助声明目录的权限契约，防止脚本先发布时安装旧属主模型的 Release。
    [[ $(<"$work_dir/init-help.txt") == *'root:mct-syslogger'* ]] ||
        die '发布包尚未使用 root:mct-syslogger 共享组权限，请等待配套 Release。'

    local prefix target doc_dir status
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
    if [[ "$initialize" == true ]]; then
        for index in "${!groups[@]}"; do
            prompt "${groups[$index]} journal 持久化容量 [${capacities[$index]}]（正整数字节或 K/M/G/T/P/E）：" "${capacities[$index]}"
            capacities[$index]=$REPLY
        done
    fi
    target="$prefix/bin/mct-syslogger"
    doc_dir="$prefix/share/doc/mct-syslogger"
    printf '\n版本：%s（linux/%s）\n命令：%s\n文档：%s\n' \
        "$tag" "$arch" "$target" "$doc_dir"
    if [[ "$initialize" == true ]]; then
        printf '名称根目录：root:mct-syslogger，2775；程序组成员由管理员管理。\n'
        for index in "${!groups[@]}"; do
            printf 'journal %s 容量：%s\n' "${groups[$index]}" "${capacities[$index]}"
        done
        printf '%s\n' \
            '将更新上述命令和文档，并以 root 运行 init：准备共享组，将根及已有项目目录修正为 root:mct-syslogger，原地修正受管文件权限，重新生成受管配置并重启四个专用 journald 实例。' \
            '受管配置中的手动修改会被覆盖；管理员独立配置保留并参与冲突检查。' \
            '需要 Linux、systemd 245+ 及 init 所需系统工具；属组、权限和容量规则由 init 校验，程序账户自行加入共享组。' \
            '权限迁移前先停止相关写入进程；清单内容和稳定锁 inode 保留，受管链接/ACL 等异常需人工处理后重试；缩小容量可能清理旧归档。默认系统 journal 保持原样。'
        prompt '确认安装并初始化？[y/N]：' 'n'
    else
        printf '%s\n' '本次仅安装或更新上述命令和文档，保持日志配置、名称目录和 journald 服务状态原样。'
        prompt '确认安装或更新？[y/N]：' 'n'
    fi
    case "$REPLY" in
        y|Y|yes|YES)
            ;;
        *)
            printf '已取消，未安装或初始化。\n'
            return
            ;;
    esac

    # 安装写入集中在确认之后，主机权限和 journal 配置仅在显式初始化时由 init 管理。
    "${elevate[@]}" install -d -m 0755 -- "$prefix/bin" "$doc_dir/tools" "$doc_dir/docs"
    for relative in tools/README.md tools/THIRD_PARTY_NOTICES.md docs/journal.md; do
        "${elevate[@]}" install -m 0644 -T -- "$work_dir/unpacked/$package/$relative" "$doc_dir/$relative"
    done
    "${elevate[@]}" install -m 0755 -T -- "$candidate" "$target"
    if [[ "$initialize" == false ]]; then
        printf '安装或更新完成：%s\n本次保持主机日志配置原样。首次配置日志接收入口时，可显式使用安装脚本 --init，或以 root 执行：%q init\n' "$target" "$target"
        return
    fi
    local -a init_args=(init)
    for index in "${!groups[@]}"; do
        init_args+=("--journal-${groups[$index]}-max-use=${capacities[$index]}")
    done
    if "${elevate[@]}" "$target" "${init_args[@]}"; then
        printf '安装及初始化完成：%s\n名称根目录为 root:mct-syslogger；程序账户加入共享组后重启，systemd-journal 读取权限另行管理。\n' "$target"
    else
        status=$?
        printf '命令已安装到 %s，但初始化失败；已完成步骤保留，请修正原因后重跑 init。\n' "$target" >&2
        return "$status"
    fi
}

main "$@"
