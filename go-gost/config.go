package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
)

// Config 配置结构体
type Config struct {
	Addr   string `json:"addr"`
	Secret string `json:"secret"`
	Http   int    `json:"http"`
	Tls    int    `json:"tls"`
	Socks  int    `json:"socks"`
	UseTLS bool   `json:"use_tls"`
}

// LoadConfig 加载配置文件
func LoadConfig(configPath string) (*Config, error) {
	// 检查文件是否存在
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("配置文件不存在: %s", configPath)
	}

	// 读取文件内容
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %v", err)
	}

	// 解析JSON
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %v", err)
	}

	// 验证必要的配置项
	if config.Addr == "" {
		return nil, fmt.Errorf("服务器地址不能为空")
	}

	// Normalize IPv6 addresses: ensure brackets for bare IPv6 like "2001:db8::1:8080"
	config.Addr = normalizeAddr(config.Addr)

	return &config, nil
}

// normalizeAddr ensures IPv6 addresses are properly bracketed for URL use.
// e.g. "example.com:8080" → unchanged, "[::1]:8080" → unchanged,
// "2001:db8::1" → "[2001:db8::1]" (bare IPv6 without port)
func normalizeAddr(addr string) string {
	// Already bracketed — fine
	if len(addr) > 0 && addr[0] == '[' {
		return addr
	}
	// Try host:port split; if it works and host is not an IP, it's a domain — fine
	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		// host:port split succeeded; check if host is IPv6
		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
			// IPv6 host without brackets — add them
			return "[" + host + "]:" + port
		}
		return addr
	}
	// No port — check if the whole string is an IPv6 address
	if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
		return "[" + addr + "]"
	}
	return addr
}
