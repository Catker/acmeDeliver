package main

import (
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/thinkeridea/go-extend/exnet"
	"github.com/zekroTJA/timedmap"
)

// ============== 常量与全局变量 ==============

// VERSION 版本号
const VERSION = "2.0"

// 命令行参数变量
var (
	// h 帮助信息
	h bool
	// bind 绑定监听地址
	bind string
	// port 服务端口
	port string
	// tls TLS 监听开关
	tls bool
	// tlsport TLS 服务端口
	tlsport string
	// certFile TLS 服务证书文件
	certFile string
	// keyFile TLS 服务私钥文件
	keyFile string
	// baseDir 证书文件所在目录
	baseDir string
	// key 密码
	key string
	// timeRange 时间戳误差，单位秒
	timeRange int64
)

// Creates a new timed map which scans for expired keys every 1 second
var tm = timedmap.New(1 * time.Second)

// ============== 结构体定义 ==============

// CertInfo 证书信息结构体
type CertInfo struct {
	Domain  string   `json:"domain"`
	Files   []string `json:"files"`
	TimeLog int64    `json:"timeLog,omitempty"`
}

// RequestParams 请求参数结构体，替代全局变量
type RequestParams struct {
	T        string
	Checksum string
	Sign     string
	Domain   string
	File     string
}

// ============== 初始化与主函数 ==============

func init() {
	// 初始化从命令行获取参数
	flag.BoolVar(&h, "h", false, "显示帮助信息")
	flag.StringVar(&bind, "b", "", "绑定监听地址,默认绑定所有接口")
	flag.StringVar(&port, "p", "9090", "服务端口,默认 9090")
	flag.BoolVar(&tls, "tls", false, "是否监听 TLS,默认关闭")
	flag.StringVar(&tlsport, "tlsport", "9443", "TLS 服务端口,默认 9443")
	flag.StringVar(&certFile, "cert", "cert.pem", "TLS 服务证书文件,默认 cert.pem")
	flag.StringVar(&keyFile, "key", "key.pem", "TLS 服务私钥文件,默认 key.pem")
	flag.StringVar(&baseDir, "d", "./", "证书文件所在目录,默认当前目录")
	flag.StringVar(&key, "k", "passwd", "密码,默认 passwd")
	flag.Int64Var(&timeRange, "t", 60, "时间戳误差,默认 60 秒")
	// 修改默认 Usage
	flag.Usage = usage
}

func main() {
	// 从 arguments 中解析注册的 flag
	// 必须在所有 flag 都注册好而未访问其值时执行
	// 未注册却使用 flag -help 时，会返回 ErrHelp
	flag.Parse()

	// 显示帮助信息
	if h {
		flag.Usage()
		return
	}

	// 设置访问的路由
	http.HandleFunc("/list", listCerts)        // 列出所有证书
	http.HandleFunc("/download", downloadFile) // 下载指定文件
	http.HandleFunc("/", home)                 // 首页信息

	// 启动 TLS 端口监听
	if tls {
		// 使用 goroutine 进行监听防阻塞
		go func() {
			if err := http.ListenAndServeTLS(bind+":"+tlsport, certFile, keyFile, nil); err != nil {
				log.Fatal("ListenAndServeTLS:", err)
			}
		}()
	}

	// 启动端口监听
	if err := http.ListenAndServe(bind+":"+port, nil); err != nil {
		log.Fatal("ListenAndServe:", err)
	}
}

// 自定义帮助信息
func usage() {
	fmt.Fprintf(os.Stderr, `acmeDeliver version: `+VERSION+`
Usage: acmeDeliver [-h] [-p port] [-d dirname] [-k password] [-t time] [-b address] [-tls] [-tlsport port] [-cert filename] [-key filename]

Options:
`)
	flag.PrintDefaults()
}

// ============== HTTP 处理器 ==============

// 首页信息
func home(response http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(response, "API Running~\n")
	fmt.Fprintf(response, "version: "+VERSION+"\n")
}

// 列出所有证书
func listCerts(response http.ResponseWriter, req *http.Request) {
	// 获取请求参数并验证
	params, ip, reqTime, err := parseAndValidateRequest(response, req, false)
	if err != nil {
		return
	}

	// 计算 token 并验证签名
	token := md5.New()
	io.WriteString(token, key)
	io.WriteString(token, params.T)
	io.WriteString(token, params.Checksum)
	checkToken := fmt.Sprintf("%x", token.Sum(nil))

	// 校验签名
	if params.Sign != checkToken {
		// 签名错误
		httpError(response, 401, "Unauthorized access.", "Incoming unauthorized access: "+params.Sign, ip)
		return
	}

	// 检测验证码是否重复
	if !validateChecksum(response, params.Checksum, reqTime, ip) {
		return
	}

	// 获取目录下所有证书信息
	certs, err := getScanCerts()
	if err != nil {
		httpError(response, 500, "Internal server error.", "Error scanning certs: "+err.Error(), ip)
		return
	}

	// 返回 JSON 格式的证书列表
	response.Header().Set("Content-Type", "application/json")
	json.NewEncoder(response).Encode(certs)
	fmt.Println("Access from IP:", ip)
	fmt.Println("Listed all certificates")
}

// 下载指定文件
func downloadFile(response http.ResponseWriter, req *http.Request) {
	// 获取请求参数并验证
	params, ip, reqTime, err := parseAndValidateRequest(response, req, true)
	if err != nil {
		return
	}

	// 计算 token
	token := md5.New()
	io.WriteString(token, params.Domain)
	io.WriteString(token, params.File)
	io.WriteString(token, key)
	io.WriteString(token, params.T)
	io.WriteString(token, params.Checksum)
	checkToken := fmt.Sprintf("%x", token.Sum(nil))

	// 校验签名
	if params.Sign != checkToken {
		// 签名错误
		httpError(response, 401, "Unauthorized access.", "Incoming unauthorized access: "+params.Sign, ip)
		return
	}

	// 检测验证码是否重复
	if !validateChecksum(response, params.Checksum, reqTime, ip) {
		return
	}

	// 校验文件访问权限
	if !validateFileAccess(response, params.Domain, params.File, ip) {
		return
	}

	// 全部校验通过，放行文件
	filepath := path.Join(baseDir, params.Domain, params.File)
	fmt.Println("Access from IP:", ip)
	fmt.Println("Access file:", filepath)
	http.ServeFile(response, req, filepath)
}

// ============== 认证与请求处理 ==============

// 解析和验证请求参数
func parseAndValidateRequest(response http.ResponseWriter, req *http.Request, needFileParams bool) (RequestParams, string, int64, error) {
	params := RequestParams{}

	// 解析 url 传递的参数，对于 POST 则解析响应包的主体（request body）
	err := req.ParseForm()
	if err != nil {
		log.Println("ParseForm:", err)
		response.WriteHeader(400)
		fmt.Fprintf(response, "Failed to parse form.")
		return params, "", 0, err
	}

	// 获取来访 IP 地址
	var ip = exnet.ClientPublicIP(req)
	if ip == "" {
		ip = exnet.ClientIP(req)
	}

	// 如果需要文件参数，则检查域名和文件名
	if needFileParams {
		// 获取传入域名
		if len(req.Form.Get("domain")) == 0 {
			response.WriteHeader(400)
			fmt.Fprintf(response, "No domain specified.")
			return params, ip, 0, fmt.Errorf("no domain specified")
		}
		params.Domain = req.Form.Get("domain")

		// 获取传入文件名
		if len(req.Form.Get("file")) == 0 {
			response.WriteHeader(400)
			fmt.Fprintf(response, "No file specified.")
			return params, ip, 0, fmt.Errorf("no file specified")
		}
		params.File = req.Form.Get("file")
	}

	// 获取传入签名
	if len(req.Form.Get("sign")) == 0 {
		response.WriteHeader(400)
		fmt.Fprintf(response, "No sign specified.")
		return params, ip, 0, fmt.Errorf("no sign specified")
	}
	params.Sign = req.Form.Get("sign")

	// 获取传入验证码
	if len(req.Form.Get("checksum")) == 0 {
		response.WriteHeader(400)
		fmt.Fprintf(response, "No checksum specified.")
		return params, ip, 0, fmt.Errorf("no checksum specified")
	}
	params.Checksum = req.Form.Get("checksum")

	// 获取传入时间戳
	if len(req.Form.Get("t")) == 0 {
		response.WriteHeader(400)
		fmt.Fprintf(response, "No timestamp specified.")
		return params, ip, 0, fmt.Errorf("no timestamp specified")
	}
	params.T = req.Form.Get("t")

	// 校验时间戳是否合法
	reqTime, err := strconv.ParseInt(params.T, 10, 64)
	if err != nil {
		fmt.Println("Access from IP:", ip)
		fmt.Println("Incoming illegal timestamp:", params.T)
		response.WriteHeader(403)
		fmt.Fprintf(response, "Timestamp not allowed.")
		return params, ip, 0, fmt.Errorf("illegal timestamp")
	}

	// 时间戳验证
	if !validateTimestamp(response, reqTime, ip) {
		return params, ip, 0, fmt.Errorf("timestamp validation failed")
	}

	return params, ip, reqTime, nil
}

// HTTP 错误响应统一处理
func httpError(w http.ResponseWriter, statusCode int, message string, logMessage string, ip string) {
	fmt.Println("Access from IP:", ip)
	fmt.Println(logMessage)
	w.WriteHeader(statusCode)
	fmt.Fprint(w, message)
}

// 验证时间戳
func validateTimestamp(response http.ResponseWriter, reqTime int64, ip string) bool {
	expireTime := time.Now().Unix() - reqTime
	// 时间戳太超前可以判定为异常访问
	if expireTime < -timeRange {
		fmt.Println("Access from IP:", ip)
		fmt.Println("Incoming illegal timestamp:", expireTime)
		response.WriteHeader(403)
		fmt.Fprintf(response, "Timestamp not allowed.")
		return false
	}
	// 校验时间戳是否过期
	if expireTime > timeRange {
		fmt.Println("Access from IP:", ip)
		fmt.Println("Incoming expired access:", expireTime)
		response.WriteHeader(403)
		fmt.Fprintf(response, "Timestamp expired.")
		return false
	}
	return true
}

// 验证校验和
func validateChecksum(response http.ResponseWriter, checksum string, reqTime int64, ip string) bool {
	if checkTime, ok := tm.GetValue(checksum).(int64); ok {
		if checkTime > 0 && time.Now().Unix()-checkTime > timeRange {
			tm.Remove(checkTime)
		} else {
			// 检测到重放请求
			fmt.Println("Access from IP:", ip)
			fmt.Println("Incoming repeat access:", checksum)
			response.WriteHeader(403)
			fmt.Fprintf(response, "Repeat access.")
			return false
		}
	} else {
		tm.Set(checksum, reqTime, time.Duration(timeRange)*time.Second)
	}
	return true
}

// ============== 证书与文件操作 ==============

// 校验文件访问权限
func validateFileAccess(response http.ResponseWriter, domain string, file string, ip string) bool {
	// 校验域名是否在指定文件夹内
	var checkDomain, checkFile bool = false, false
	files, _ := os.ReadDir(baseDir)
	for _, f := range files {
		if domain == f.Name() && f.IsDir() {
			checkDomain = true
			break
		}
	}

	if !checkDomain {
		// 获取的域名不存在
		httpError(response, 404, "Domain not exist.", "Incoming illegal domain: "+domain, ip)
		return false
	}

	// 对应域名的文件夹存在，校验内部文件是否存在
	files, _ = os.ReadDir(path.Join(baseDir, domain))
	for _, f := range files {
		if file == f.Name() && !f.IsDir() {
			checkFile = true
			break
		}
	}

	if !checkFile {
		// 获取的文件不存在
		httpError(response, 404, "File not exist.", "Incoming illegal filename: "+file, ip)
		return false
	}

	return true
}

// 扫描目录获取所有证书信息
func getScanCerts() ([]CertInfo, error) {
	var certs []CertInfo

	// 读取基础目录
	domains, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, err
	}

	// 遍历所有域名目录
	for _, domain := range domains {
		if !domain.IsDir() {
			continue
		}

		domainName := domain.Name()
		certInfo := CertInfo{
			Domain: domainName,
			Files:  []string{},
		}

		// 获取域名目录下的所有文件
		files, err := os.ReadDir(path.Join(baseDir, domainName))
		if err != nil {
			continue
		}

		// 添加文件列表
		for _, file := range files {
			if !file.IsDir() {
				certInfo.Files = append(certInfo.Files, file.Name())

				// 如果存在time.log文件，读取其内容作为时间戳
				if file.Name() == "time.log" {
					timeContent, err := os.ReadFile(path.Join(baseDir, domainName, "time.log"))
					if err == nil {
						timeStamp, err := strconv.ParseInt(string(timeContent), 10, 64)
						if err == nil {
							certInfo.TimeLog = timeStamp
						}
					}
				}
			}
		}

		// 只添加有文件的证书信息
		if len(certInfo.Files) > 0 {
			certs = append(certs, certInfo)
		}
	}

	return certs, nil
}