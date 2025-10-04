package main

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"gopkg.in/ini.v1"
)

// 全局变量
var ipv6Addresses []string // 用于存储当前可用的IPv6地址列表
var counter *Counter       // 用于轮询选择IPv6地址的计数器
var osName string          // 存储当前操作系统名称

// Counter 是一个线程安全的计数器，用于循环获取地址
type Counter struct {
	mu     sync.Mutex
	count  int
	maxVal int
}

// NewCounter 创建一个新的计数器实例
func NewCounter(maxVal int) *Counter {
	return &Counter{
		maxVal: maxVal,
	}
}

// Increment 增加计数器的值并返回当前值，到达最大值后会归零
func (c *Counter) Increment() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.count == c.maxVal {
		c.count = 0
	}

	currentCount := c.count
	c.count++
	return currentCount
}

// isFirstCharacterTwo 检查字符串的第一个字符是否为'2'，用于判断是否为公网IPv6地址
func isFirstCharacterTwo(input string) bool {
	if len(input) == 0 {
		return false
	}
	firstChar := input[0]
	return firstChar == '2'
}

// getIPv6Addresses 获取指定网卡上所有的公网IPv6地址
func getIPv6Addresses(networkName string) ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var addresses []string
	for _, iface := range interfaces {
		if iface.Name == networkName {
			addrs, err := iface.Addrs()
			if err != nil {
				return nil, err
			}

			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
					// 确保是IPv6地址且为公网地址
					if ipnet.IP.To4() == nil && ipnet.IP.To16() != nil {
						if isFirstCharacterTwo(ipnet.IP.String()) {
							addresses = append(addresses, ipnet.IP.String())
						}
					}
				}
			}
		}
	}
	return addresses, nil
}

// handleClient 处理单个SOCKS5客户端连接
func handleClient(clientConn net.Conn) {
	defer clientConn.Close()

	// 缓冲区
	buf := make([]byte, 256)

	// SOCKS5第一阶段：版本和认证方法协商
	// +----+----------+----------+
	// |VER | NMETHODS | METHODS  |
	// +----+----------+----------+
	// | 1  |    1     | 1 to 255 |
	// +----+----------+----------+
	_, err := clientConn.Read(buf)
	if err != nil {
		// 客户端可能已断开，静默处理
		return
	}

	// 检查SOCKS版本是否为5
	if buf[0] != 0x05 {
		return
	}

	// SOCKS5服务器响应：选择无需认证方法
	// +----+--------+
	// |VER | METHOD |
	// +----+--------+
	// | 1  |   1    |
	// +----+--------+
	_, err = clientConn.Write([]byte{0x05, 0x00}) // 0x00 表示无需认证
	if err != nil {
		return
	}

	// SOCKS5第二阶段：接收客户端的连接请求
	// +----+-----+-------+------+----------+----------+
	// |VER | CMD |  RSV  | ATYP | DST.ADDR | DST.PORT |
	// +----+-----+-------+------+----------+----------+
	// | 1  |  1  | X'00' |  1   | Variable |    2     |
	// +----+-----+-------+------+----------+----------+
	n, err := clientConn.Read(buf)
	if err != nil {
		return
	}

	// 必须是SOCKS5版本(0x05)和CONNECT命令(0x01)
	if buf[0] != 0x05 || buf[1] != 0x01 {
		return
	}

	// 解析目标地址类型(ATYP)
	addressType := buf[3]
	var destAddr string

	switch addressType {
	case 0x01: // IPv4地址
		// 不支持IPv4
		return
	case 0x03: // 域名
		// 域名长度在 buf[4]
		domainLength := int(buf[4])
		destAddr = string(buf[5 : 5+domainLength])
	case 0x04: // IPv6地址
		destAddr = net.IP(buf[4:20]).String()
	default:
		// 不支持的地址类型
		return
	}

	// 解析目标端口（网络字节序，大端）
	destPort := int(buf[n-2])<<8 | int(buf[n-1])

	// 使用指定的IPv6地址建立到目标服务器的连接
	destConn, err := zdipfw("tcp6", fmt.Sprintf("[%s]:%d", destAddr, destPort), ipv6Addresses[counter.Increment()])
	if err != nil {
		// 连接目标失败
		return
	}
	defer destConn.Close()

	// SOCKS5服务器响应：告诉客户端连接已成功建立
	// +----+-----+-------+------+----------+----------+
	// |VER | REP |  RSV  | ATYP | BND.ADDR | BND.PORT |
	// +----+-----+-------+------+----------+----------+
	// | 1  |  1  | X'00' |  1   | Variable |    2     |
	// +----+-----+-------+------+----------+----------+
	_, err = clientConn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	if err != nil {
		return
	}

	// 开始双向转发数据
	go func() {
		io.Copy(destConn, clientConn)
	}()
	io.Copy(clientConn, destConn)
}

// zdipfw 使用指定的本地IP地址(fwip)来建立TCP连接
func zdipfw(netw, addr string, fwip string) (net.Conn, error) {
	// 解析本地地址，端口为0表示由系统自动选择
	lAddr, err := net.ResolveTCPAddr(netw, "["+fwip+"]:0")
	if err != nil {
		return nil, err
	}
	// 解析远程目标地址
	rAddr, err := net.ResolveTCPAddr(netw, addr)
	if err != nil {
		return nil, err
	}
	// 使用指定的本地地址拨号
	conn, err := net.DialTCP(netw, lAddr, rAddr)
	if err != nil {
		return nil, err
	}
	// 设置35秒超时
	deadline := time.Now().Add(35 * time.Second)
	conn.SetDeadline(deadline)
	return conn, nil
}

// main 程序主入口
func main() {
	osName = runtime.GOOS

	switch osName {
	case "windows":
		fmt.Println("检测到系统: Windows")
	case "linux":
		fmt.Println("检测到系统: Linux")
	default:
		errhandling(fmt.Errorf("未知的操作系统"))
	}

	// 加载INI配置文件
	cfg, err := ini.Load("config.ini")
	if err != nil {
		errhandling(err)
	}

	// 读取配置项
	section := cfg.Section("")
	networkName := section.Key("Networkname").String()
	port := section.Key("port").String()
	if networkName == "" || port == "" {
		fmt.Println("NetworkName:" + networkName)
		fmt.Println("Port:" + port)
		errhandling(fmt.Errorf("请检查 config.ini 配置文件"))
	}
	fmt.Println("使用的网卡名称:", networkName)

	// 获取所有前缀为/64的IPv6地址的完整IP
	ya, err := get64(networkName)
	if err != nil {
		errhandling(err)
	}

	// 获取当前所有的IPv6地址
	ipv6Addresses, _ = getIPv6Addresses(networkName)

	// 提示用户是否要删除除/64地址之外的其他地址
	p := promptForYesNo("是否删除除/64地址以外的IPv6地址(!!!)")
	if p {
		fmt.Println("开始删除地址...")
		processIPv6Addresses(ipv6Addresses, networkName, ya)
		fmt.Println("删除完成")
	}

	// 提示用户是否要添加新的IPv6地址
	p = promptForYesNo("是否要添加新的IPv6地址")
	if p {
		var userInput int
		fmt.Print("请输入添加数量: ")
		fmt.Scanf("%d", &userInput)

		fmt.Println("开始添加地址...")
		// 基于/64地址的前缀生成随机地址
		na := generateRandomIPv6Batch(ya[0], userInput)
		progress := pb.StartNew(len(na))
		for c := 0; c < len(na); c++ {
			setaddres("add", networkName, na[c])
			progress.Increment()
		}
		progress.Finish()
		fmt.Println("添加完成")
	}

	// 重新获取最新的地址列表并初始化计数器
	ipv6Addresses, _ = getIPv6Addresses(networkName)
	maxVal := len(ipv6Addresses)
	counter = NewCounter(maxVal)
	fmt.Printf("当前共有 %d 个可用的IPv6地址。\n", len(ipv6Addresses))

	// 启动SOCKS5代理服务器
	listenAddr := "0.0.0.0:" + port
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fmt.Println("启动代理失败:", err)
		os.Exit(1)
	}
	defer listener.Close()

	fmt.Printf("SOCKS5 代理正在监听 %s...\n", listenAddr)

	// 循环接受客户端连接
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			fmt.Println("接受客户端连接失败:", err)
			continue
		}
		go handleClient(clientConn)
	}
}

// promptForYesNo 向用户提问并获取y/n的答复
func promptForYesNo(prompt string) bool {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print(prompt + " (y/n): ")
		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("读取输入错误:", err)
			return false
		}
		input = strings.TrimSpace(input)

		if strings.EqualFold(input, "y") {
			return true
		} else if strings.EqualFold(input, "n") {
			return false
		}
		fmt.Println("请输入 'y' 或 'n'")
	}
}

// get64 扫描并返回所有后缀为/64的IPv6地址的完整IP部分
func get64(networkName string) ([]string, error) {
	iface, err := net.InterfaceByName(networkName)
	if err != nil {
		return nil, fmt.Errorf("获取网卡失败: " + err.Error())
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("获取地址信息失败: " + err.Error())
	}

	var r []string
	for _, addr := range addrs {
		addrStr := addr.String()
		if isFirstCharacterTwo(addrStr) {
			fmt.Println("发现IPv6 地址:", addr)
			if strings.HasSuffix(addrStr, "/64") {
				// 去除/64后缀，保留完整的IP地址字符串
				fullIP := strings.TrimSuffix(addrStr, "/64")
				r = append(r, fullIP)
			}
		}
	}
	if len(r) > 0 {
		return r, nil
	} else {
		return nil, fmt.Errorf("错误：未找到任何前缀为/64的IPv6地址")
	}
}

// runCmd 根据操作系统执行shell命令
func runCmd(command string) error {
	var cmd *exec.Cmd
	switch osName {
	case "windows":
		cmd = exec.Command("cmd", "/c", command)
	case "linux":
		cmd = exec.Command("bash", "-c", command)
	default:
		return fmt.Errorf("不支持的操作系统")
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("命令执行失败: %v\n输出: %s", err, output)
	}
	return nil
}

// setaddres 添加或删除IPv6地址
func setaddres(set, networkName, ipv6Address string) {
	var cmd string
	switch osName {
	case "windows":
		if set == "add" {
			cmd = fmt.Sprintf(`netsh interface ipv6 %s address "%s" %s/128`, set, networkName, ipv6Address)
		} else {
			cmd = fmt.Sprintf(`netsh interface ipv6 %s address "%s" %s`, set, networkName, ipv6Address)
		}
	case "linux":
		if set == "add" {
			// 在Linux上添加的地址默认就是/128
			cmd = fmt.Sprintf(`sudo ip addr %s %s/128 dev %s`, set, ipv6Address, networkName)
		} else {
			// 删除时需要指定完整的地址和前缀
			cmd = fmt.Sprintf(`sudo ip addr %s %s/128 dev %s`, set, ipv6Address, networkName)
		}
	}

	err := runCmd(cmd)
	if err != nil {
		// 打印错误，但不中断程序
		fmt.Println("命令执行失败:", err)
		return
	}
}

// processIPv6Addresses 处理地址列表，删除不在保留列表(ya)中的地址
func processIPv6Addresses(ipv6Addresses []string, networkName string, ya []string) {
	progress := pb.StartNew(len(ipv6Addresses))
	for _, address := range ipv6Addresses {
		found := false
		// 遍历要保留的IP列表
		for _, ipToKeep := range ya {
			// 进行精确的完全匹配
			if address == ipToKeep {
				found = true
				break
			}
		}
		// 如果地址在保留列表中，则跳过
		if found {
			progress.Increment()
			continue
		}

		// 否则，执行删除操作
		setaddres("del", networkName, address)
		progress.Increment()
	}
	progress.Finish()
}

// generateRandomIPv6Batch 基于一个完整的IPv6地址，生成多个具有相同/64前缀的随机地址
func generateRandomIPv6Batch(baseIPv6 string, count int) []string {
	// 解析基础IPv6地址
	baseIP := net.ParseIP(baseIPv6)
	if baseIP == nil {
		fmt.Println("错误：无法解析基础IPv6地址用于生成随机地址")
		return nil
	}

	// 获取前64位前缀（即IP地址的前8个字节）
	prefix := baseIP[:8]

	randomIPv6Addresses := make([]string, count)
	for i := 0; i < count; i++ {
		// 生成随机的后64位（即8个字节）
		randomSuffix := make([]byte, 8)
		_, err := rand.Read(randomSuffix)
		if err != nil {
			fmt.Println("错误：生成随机后缀失败")
			continue
		}

		// 合并前缀和随机后缀，组成新的IPv6地址
		randomIPv6 := net.IP(append(prefix, randomSuffix...)).String()
		randomIPv6Addresses[i] = randomIPv6
	}

	return randomIPv6Addresses
}

// errhandling 统一的错误处理函数，打印错误并等待用户按键退出
func errhandling(err error) {
	fmt.Println("发生错误:", err.Error())
	fmt.Printf("按任意键退出...")
	b := make([]byte, 1)
	os.Stdin.Read(b)
	os.Exit(1)
}