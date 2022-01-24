#!/bin/bash

DEBUG=true
WORKDIR="/tmp/acme" #工作目录，默认为/tmp/acme

getTimestamp(){
  timestamp=$(date '+%s')
}

generateRandom(){
  checkSum=$RANDOM
}

calculateToken(){
  token=$(echo -n "${domain}$1${password}${timestamp}${checkSum}"|md5sum|cut -d ' ' -f1)
  if $DEBUG; then echo "token:$token"; fi
}

requestServer(){
  # $1:server address, $2:filename
  generateRandom #每次请求生成一个新的随机值
  calculateToken "$2"
  url=$1'?domain='$domain'&file='$2'&t='$timestamp'&sign='$token'&checksum='$checkSum
  requestRes=$(curl -s -f -o "${WORKDIR}/${domain}/temp" -w %{http_code} "$url")
  if $DEBUG; then echo "requestUrl: $url"; echo "requestResult: $requestRes"; fi
}

checkUpdate(){
  if [ ! -d "${WORKDIR}" ]; then #判断工作目录是否存在
    mkdir "${WORKDIR}"
  fi
  if [ ! -d "${WORKDIR}/${domain}" ]; then #判断域名目录是否存在
    mkdir "${WORKDIR}/${domain}"
  fi
  if [ -f "${WORKDIR}/${domain}/timestamp.txt" ]; then
    #检测服务器时间戳是否有更新
    requestServer "${server}" "ecc_time.log" #向服务端请求服务端更新时间戳
    if [ "$requestRes" != '200' ]; then
      echo "请求服务端时间戳失败！"
      #exit 1 #取消注释以在服务端响应时间戳失败时退出脚本
    fi

    # shellcheck disable=SC2162
    read ts1 < "${WORKDIR}/${domain}/temp" && read ts2 < "${WORKDIR}/${domain}/timestamp.txt"
    if [ "$ts1" = "$ts2" ]; then
        echo "时间戳相同，无需更新"
        return
    else
        echo "时间戳不同，将会开始下载"
    fi
  else
    echo "本地不存在时间戳文件，将会开始下载"
    requestServer "${server}" "ecc_time.log" #向服务端请求服务端更新时间戳
    if [ "$requestRes" != '200' ]; then
      echo "请求服务端时间戳失败！"
      #exit 1 #取消注释以在服务端响应时间戳失败时退出脚本
    else
      cp -f "${WORKDIR}/${domain}/temp" "${WORKDIR}/${domain}/timestamp.tmp" #将刚对比的时间戳保存
    fi
  fi
  mv -f "${WORKDIR}/${domain}/temp" "${WORKDIR}/${domain}/timestamp.tmp" #将刚对比的时间戳临时保存

  for file_name_d in "${domain}.cer" "${domain}.key" "ca.cer" "fullchain.cer"
  do
    echo "下载文件名：${file_name_d}"
    requestServer "${server}" "${file_name_d}" #向服务端请求服务端更新时间戳
    if [ "$requestRes" != '200' ]; then
      printf "请求文件失败！文件名：%s,状态码：%s" "$file_name_d" "$requestRes"
      flag_fail=true
      #exit 1 #取消注释以在服务端响应文件失败时退出脚本
    fi
    mv -f "${WORKDIR}/${domain}/temp" "${WORKDIR}/${domain}/${file_name_d}"
  done
  if [ ${flag_fail} ]; then
    echo "下载过程中有文件下载失败！"
    return 1
  fi
  mv -f "${WORKDIR}/${domain}/timestamp.tmp" "${WORKDIR}/${domain}/timestamp.txt" #将刚对比的时间戳作为保存的时间戳
  return 0
}

deployCert(){
  echo "deploy cert file function"
}

echo_help(){
  echo "Usage: [-c execute check update job] [-h help] [-d domain name] [-p password] [-s server address] [-n file name] [-w workdir(not necessary)]
使用方法：[-c 执行自动更新任务] [-h 帮助] [-d 域名] [-p 密码] [-s acmeDeliver服务器地址] [-n 要获取的文件名] [-w 工作目录(可选)]"
}

#解析命令行参数
while getopts "chp:s:d:n:w:" arg #选项后面的冒号表示该选项需要参数
do
  case $arg in
    h)
      echo_help
      exit 0
      ;;
    c)
      check_update_job=true
      ;;
    p)
      password=$OPTARG
      if $DEBUG; then echo "password:$password"; fi
      ;;
    s)
      server=$OPTARG
      if $DEBUG; then echo "server address:$server"; fi
      ;;
    d)
      domain=$OPTARG
      if $DEBUG; then echo "domain:$domain"; fi
      ;;
    n)
      filename=$OPTARG
      if $DEBUG; then echo "filename:$filename"; fi
      ;;
    w)
      WORKDIR=$OPTARG
      if $DEBUG; then echo "workdir:$WORKDIR"; fi
      ;;
    ?)  #当有不认识的选项的时候arg为?
      echo "unknown argument"
      echo_help
      exit 1
    ;;
  esac
done

main(){
  # 检测是否缺少必要参数
  if [ -z "$server" ] || [ -z "$domain" ] || [ -z "$password" ]; then
    echo "缺少必要参数"
    exit 1
  fi

  getTimestamp #获取当前时间戳
  if test ${check_update_job}; then checkUpdate;exit 0; fi

  # 未设置工作模式时默认是获取指定文件
  if [ -z "$filename" ]; then
    echo "缺少文件名"
    exit 1
  fi

  requestServer "$server" "$filename" #请求服务器指定文件
  if [ "$requestRes" != '200' ]; then
    printf "请求文件失败！文件名：%s,状态码：%s" "$file_name_d" "$requestRes"
    exit 1
  fi

  mv -f "${WORKDIR}/${domain}/temp" "$filename" #默认存放在命令行工作目录下
  # shellcheck disable=SC2046
  printf "下载成功！文件保存在%s/%s" $(pwd) "${filename}"
  exit 0
}
main