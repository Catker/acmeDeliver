#!/bin/bash

# 默认参数
KEY="passwd"
SERVER="http://127.0.0.1:9090"
CERT_DIR="/tmp/acme"
RELOAD_CMD=""
FORCE_UPDATE="n"
SPECIFIC_DOMAINS=""
IP_MODE=""
DEBUG="n"
LIST_ONLY="n"

# 显示用法
usage() {
    echo "用法: $0 [-k key] [-s server] [-d cert_dir] [-r reload_cmd] [-n domains] [-f] [-4] [-6] [--debug] [--list] [-h]"
    echo ""
    echo "选项:"
    echo "  -k  密钥，默认为 passwd"
    echo "  -s  服务器地址，默认为 http://127.0.0.1:9090"
    echo "  -d  证书保存目录，默认为 /tmp/acme"
    echo "  -r  更新证书后执行的命令，例如 'systemctl reload nginx'"
    echo "  -n  指定要下载的域名，多个域名用逗号分隔，例如 'example.com,example.org'"
    echo "  -f  强制更新，忽略时间戳检查"
    echo "  -4  仅使用IPv4"
    echo "  -6  仅使用IPv6"
    echo "  --debug  调试模式，显示详细日志"
    echo "  --list   仅列出服务器上的域名证书，不下载"
    echo "  -h  显示此帮助信息"
    exit 1
}

# 下载文件函数
download_file() {
    local domain=$1
    local file=$2
    local output_file=$3
    
    # 当前时间戳
    local timestamp=$(date +%s)
    # 随机校验和
    local checksum=$(openssl rand -hex 16)
    # 计算签名
    local sign=$(echo -n "${domain}${file}${KEY}${timestamp}${checksum}" | md5sum | cut -d ' ' -f 1)
    
    # 下载URL
    local url="${SERVER}/download?domain=${domain}&file=${file}&t=${timestamp}&checksum=${checksum}&sign=${sign}"
    
    # 设置IP模式标志
    local curl_ip_flag=""
    if [[ "$IP_MODE" == "4" ]]; then
        curl_ip_flag="-4"
    elif [[ "$IP_MODE" == "6" ]]; then
        curl_ip_flag="-6"
    fi
    
    # 下载文件
    if [ "$DEBUG" = "y" ]; then
        echo "  调试: 下载URL: $url"
        echo "  调试: curl选项: $curl_ip_flag"
    fi
    
    # 使用临时文件保存响应和状态码
    local temp_header=$(mktemp)
    curl $curl_ip_flag -s -o "$output_file" -D "$temp_header" "$url"
    local curl_status=$?
    
    if [ "$DEBUG" = "y" ]; then
        echo "  调试: curl退出状态: $curl_status"
        echo "  调试: 响应头:"
        cat "$temp_header"
    fi
    
    # 从响应头中提取状态码
    local status_code=$(head -n 1 "$temp_header" | grep -Eo '[0-9]{3}')
    rm -f "$temp_header"
    
    # 检查结果
    if [ $curl_status -eq 0 ] && [ -s "$output_file" ] && [ "$status_code" = "200" ]; then
        return 0  # 成功
    elif [ "$status_code" = "404" ]; then
        if [ "$DEBUG" = "y" ]; then
            echo "  调试: 文件不存在 (HTTP 404)"
        fi
        return 2  # 文件不存在
    else
        if [ "$DEBUG" = "y" ]; then
            echo "  调试: 下载失败，状态码: $status_code, curl状态: $curl_status"
        fi
        return 1  # 其他错误
    fi
}

# 处理单个域名
process_domain() {
    local domain=$1
    echo "处理域名: $domain"
    
    # 为域名创建目录
    domain_dir="$CERT_DIR/$domain"
    mkdir -p "$domain_dir"
    
    # 尝试下载time.log检查域名是否存在
    local domain_exists=true
    local temp_time_log=$(mktemp)
    
    echo "  检查域名是否存在..."
    if ! download_file "$domain" "time.log" "$temp_time_log"; then
        result=$?
        if [ $result -eq 2 ]; then
            echo "  域名 $domain 在服务器上不存在或没有可用证书"
            domain_exists=false
        else
            echo "  无法连接到服务器或其他错误"
            domain_exists=false
        fi
        
        # 如果域名不存在，删除创建的目录
        rm -rf "$domain_dir"
        rm -f "$temp_time_log"
        return 2
    fi
    
    # 获取本地时间戳
    local_timestamp_file="$domain_dir/time.log"
    if [ -f "$local_timestamp_file" ]; then
        local_timestamp=$(cat "$local_timestamp_file")
        echo "  本地时间戳: $local_timestamp"
    else
        local_timestamp=0
        echo "  本地无时间戳记录，默认为: $local_timestamp"
    fi
    
    # 读取服务器时间戳
    server_timestamp=$(cat "$temp_time_log")
    # 保存时间戳文件
    cp "$temp_time_log" "$local_timestamp_file"
    rm -f "$temp_time_log"
    
    # 检查是否为有效时间戳
    if [[ $server_timestamp =~ ^[0-9]+$ ]]; then
        echo "  服务器时间戳: $server_timestamp"
    else
        server_timestamp=0
        echo "  服务器时间戳无效，设置为: $server_timestamp"
    fi
    
    # 检查是否需要更新证书
    need_update=false
    if [ "$FORCE_UPDATE" = "y" ]; then
        echo "  强制更新模式"
        need_update=true
    elif [ $server_timestamp -gt $local_timestamp ]; then
        echo "  服务器时间戳较新，需要更新"
        need_update=true
    else
        echo "  证书是最新的，无需更新"
        need_update=false
    fi
    
    # 如果需要更新，下载证书文件
    if [ "$need_update" = true ]; then
        # 标准证书文件
        cert_files="cert.pem key.pem fullchain.pem"
        
        echo "  下载证书文件..."
        download_success=true
        
        for file in $cert_files; do
            echo "  - 下载 $file"
            if download_file "$domain" "$file" "$domain_dir/$file"; then
                file_size=$(stat -c%s "$domain_dir/$file")
                echo "    成功 ($file_size 字节)"
            else
                echo "    失败"
                download_success=false
            fi
        done
        
        # 如果所有证书文件都下载成功，增加更新计数
        if [ "$download_success" = true ]; then
            return 0  # 表示成功更新
        fi
    fi
    
    return 1  # 表示没有更新
}

# 获取证书列表
get_cert_list() {
    local timestamp=$(date +%s)
    local checksum=$(openssl rand -hex 16)
    local sign=$(echo -n "${KEY}${timestamp}${checksum}" | md5sum | cut -d ' ' -f 1)
    
    # 设置IP模式标志
    local curl_ip_flag=""
    if [[ "$IP_MODE" == "4" ]]; then
        curl_ip_flag="-4"
    elif [[ "$IP_MODE" == "6" ]]; then
        curl_ip_flag="-6"
    fi
    
    local cert_list=$(curl $curl_ip_flag -s -X GET "${SERVER}/list?t=${timestamp}&checksum=${checksum}&sign=${sign}")
    
    if [ -z "$cert_list" ]; then
        echo "获取证书列表失败，请检查网络连接或服务器状态。"
        return 1
    fi
    
    if [[ "$cert_list" == *"Unauthorized access"* ]]; then
        echo "授权失败，请检查密钥是否正确。"
        return 1
    fi
    
    # 如果是仅列表模式，解析和显示简化信息
    if [ "$LIST_ONLY" = "y" ]; then
        # 使用临时文件解析JSON
        local tmp_file=$(mktemp)
        echo "$cert_list" > "$tmp_file"
        
        echo "======== 服务器证书列表 ========"
        echo "服务器: $SERVER"
        
        # 提取域名列表
        local domains=$(grep -o '"domain":"[^"]*"' "$tmp_file" | cut -d'"' -f4)
        
        # 计算域名数量
        local domain_count=$(echo "$domains" | wc -l)
        echo "共有 $domain_count 个域名的证书:"
        echo ""
        
        # 显示域名列表
        local counter=1
        while read -r domain; do
            echo "[$counter] $domain"
            counter=$((counter + 1))
        done <<< "$domains"
        
        rm -f "$tmp_file"
        return 0
    fi
    
    # 返回证书列表
    echo "$cert_list"
    return 0
}

# 处理长选项
TEMP_ARGS=()
for arg in "$@"; do
    case "$arg" in
        --debug)
            DEBUG="y"
            ;;
        --list)
            LIST_ONLY="y"
            ;;
        *)
            TEMP_ARGS+=("$arg")
            ;;
    esac
done

# 替换原始参数为过滤后的参数
set -- "${TEMP_ARGS[@]}"

# 获取参数
while getopts "k:s:d:r:n:f46h" opt; do
    case $opt in
        k) KEY="$OPTARG" ;;
        s) SERVER="$OPTARG" ;;
        d) CERT_DIR="$OPTARG" ;;
        r) RELOAD_CMD="$OPTARG" ;;
        n) SPECIFIC_DOMAINS="$OPTARG" ;;
        f) FORCE_UPDATE="y" ;;
        4) 
           if [[ "$IP_MODE" == "6" ]]; then
               echo "错误: -4 和 -6 选项不能同时使用。"
               usage
           fi
           IP_MODE="4" 
           ;;
        6)
           if [[ "$IP_MODE" == "4" ]]; then
               echo "错误: -4 和 -6 选项不能同时使用。"
               usage
           fi 
           IP_MODE="6" 
           ;;
        h) usage ;;
        \?) usage ;;
    esac
done

# 打印调试信息
if [ "$DEBUG" = "y" ]; then
    echo "调试模式已启用"
    echo "密钥: $KEY"
    echo "服务器: $SERVER"
    echo "证书目录: $CERT_DIR"
    echo "重载命令: $RELOAD_CMD"
    echo "指定域名: $SPECIFIC_DOMAINS"
    echo "强制更新: $FORCE_UPDATE"
    echo "IP模式: $IP_MODE"
    echo "仅列表模式: $LIST_ONLY"
fi

# 如果是仅列表模式，则获取并显示证书列表后退出
if [ "$LIST_ONLY" = "y" ]; then
    get_cert_list
    exit $?
fi

# 确保目录存在
mkdir -p "$CERT_DIR"

# 如果指定了特定域名，则直接处理这些域名
if [ -n "$SPECIFIC_DOMAINS" ]; then
    echo "指定域名模式：仅处理指定的域名"
    
    # 使用IFS和read处理逗号分隔的域名列表
    IFS=',' read -ra DOMAIN_ARRAY <<< "$SPECIFIC_DOMAINS"
    
    total_specified=$(echo "${#DOMAIN_ARRAY[@]}")
    echo "共指定了 $total_specified 个域名"
    
    updated_domains=0
    invalid_domains=0
    invalid_domain_list=""
    current_domain=0
    
    for domain in "${DOMAIN_ARRAY[@]}"; do
        current_domain=$((current_domain + 1))
        echo "处理域名 ($current_domain/$total_specified): $domain"
        
        process_domain "$domain"
        result=$?
        
        if [ $result -eq 0 ]; then
            updated_domains=$((updated_domains + 1))
        elif [ $result -eq 2 ]; then
            invalid_domains=$((invalid_domains + 1))
            invalid_domain_list="$invalid_domain_list $domain"
        fi
    done
    
    echo "指定域名处理完成！"
    echo "  - 更新了 $updated_domains 个域名的证书"
    if [ $invalid_domains -gt 0 ]; then
        echo "  - $invalid_domains 个域名在服务器上不存在或没有可用证书:"
        for domain in $invalid_domain_list; do
            echo "    * $domain"
        done
    fi
    
    # 如果有更新且设置了重载命令，则执行命令
    if [ $updated_domains -gt 0 ] && [ -n "$RELOAD_CMD" ]; then
        echo "检测到证书更新，执行命令: $RELOAD_CMD"
        eval "$RELOAD_CMD"
        if [ $? -eq 0 ]; then
            echo "命令执行成功。"
        else
            echo "命令执行失败，退出代码: $?"
        fi
    fi
    
    exit 0
fi

# 处理所有域名模式
echo "获取证书列表..."
cert_list=$(get_cert_list)

if [ $? -ne 0 ]; then
    # 函数已经输出错误信息
    exit 1
fi

# 解析证书列表
tmp_file=$(mktemp)
echo "$cert_list" > "$tmp_file"

total_domains=$(grep -o '"domain":' "$tmp_file" | wc -l)
current_domain=0
updated_domains=0
invalid_domains=0
invalid_domain_list=""

echo "找到 $total_domains 个域名的证书，开始处理..."

# 处理每个域名的证书
while read -r domain; do
    current_domain=$((current_domain + 1))
    echo "处理域名 ($current_domain/$total_domains): $domain"
    
    process_domain "$domain"
    result=$?
    
    if [ $result -eq 0 ]; then
        updated_domains=$((updated_domains + 1))
    elif [ $result -eq 2 ]; then
        invalid_domains=$((invalid_domains + 1))
        invalid_domain_list="$invalid_domain_list $domain"
    fi
done < <(grep -o '"domain":"[^"]*"' "$tmp_file" | cut -d'"' -f4)

# 清理临时文件
rm -f "$tmp_file"

echo "所有证书处理完成！"
echo "  - 更新了 $updated_domains 个域名的证书"
if [ $invalid_domains -gt 0 ]; then
    echo "  - $invalid_domains 个域名在服务器上不存在或没有可用证书:"
    for domain in $invalid_domain_list; do
        echo "    * $domain"
    done
fi

# 如果有更新且设置了重载命令，则执行命令
if [ $updated_domains -gt 0 ] && [ -n "$RELOAD_CMD" ]; then
    echo "检测到证书更新，执行命令: $RELOAD_CMD"
    eval "$RELOAD_CMD"
    if [ $? -eq 0 ]; then
        echo "命令执行成功。"
    else
        echo "命令执行失败，退出代码: $?"
    fi
fi