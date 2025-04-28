package utils

import (
	"crypto/sha256"
	"fmt"
	"math/rand"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func SplitAddr(addr string) (string, int) {
	parsed := strings.Split(addr, ":")
	port, _ := strconv.Atoi(parsed[1])
	return parsed[0], port
}

func ResetInt(period int, rester *int, orgin *int) {
	valueOfr := reflect.ValueOf(rester)
	valueOfr = valueOfr.Elem()
	select {
	case <-time.After(time.Duration(period) * time.Millisecond):

		valueOfr.SetInt(int64(*orgin))
	}

}

func RandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var seededRand = rand.New(rand.NewSource(time.Now().UnixNano()))

	// 创建结果字节数组
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(result)
}

func RandPort() int {
	return rand.Intn(30000-20000) + 20000
}

func JoinHostPort(ip string, port int) string {
	return fmt.Sprintf("%s:%d", ip, port)
}

func GetLocalAddr() string {
	addrs, err := net.InterfaceAddrs()

	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	for _, address := range addrs {

		// 检查ip地址判断是否回环地址
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

func getMachineMacAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	// 选择第一个有效的网络接口
	for _, iface := range interfaces {
		if len(iface.HardwareAddr) > 0 {
			return iface.HardwareAddr.String(), nil
		}
	}
	return "", fmt.Errorf("no valid MAC address found")
}

func GenerateMachineHash() string {
	mac, err := getMachineMacAddress()
	if err != nil {
		return RandomString(32)
	}

	// 使用 SHA-256 算法生成哈希
	hash := sha256.New()
	hash.Write([]byte(mac))
	hashBytes := hash.Sum(nil)
	return fmt.Sprintf("%x", hashBytes)
}

func GetLocalSubnet() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ss []string
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if i := ipnet.IP.To4(); i != nil && i[0] != 169 {
				ss = append(ss, ipnet.String())
			}
		}
	}
	return ss
}
