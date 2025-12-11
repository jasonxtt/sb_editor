package main

import (
	"io/ioutil"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// 全局变量来存储当前活动的配置目录，并使用互斥锁确保并发安全
var (
	currentConfigPath      string
	currentConfigPathMutex sync.RWMutex // 读写锁
)

// DEFAULT_CONFIG_PATHS 预设查找路径列表
var DEFAULT_CONFIG_PATHS = []string{
	"/usr/local/etc/sing-box/conf/",
	"/etc/sing-box/conf/",
	"/root/singbox/conf/",
	"/root/sing-box/conf/",
}

// initConfigPaths 在程序启动时初始化配置路径
// 返回找到的所有可用路径，systemd默认路径，以及初始的 current_active_path
func initConfigPaths() (foundPaths []string, systemdDefaultPath string, initialActivePath string) {
	// 1. 智能默认路径检测 (Systemd)
	systemdDefaultPath = detectSystemdConfigPath()
	if systemdDefaultPath != "" {
		log.Printf("检测到 systemd 默认配置路径: %s", systemdDefaultPath)
		// 验证 systemd 路径是否存在且可读
		if isValidConfigDir(systemdDefaultPath) {
			foundPaths = append(foundPaths, systemdDefaultPath)
		} else {
			log.Printf("systemd 检测到的路径 '%s' 不存在或不可读。", systemdDefaultPath)
			systemdDefaultPath = "" // 如果无效则清空
		}
	}

	// 2. 预设查找路径列表
	for _, p := range DEFAULT_CONFIG_PATHS {
		if isValidConfigDir(p) {
			// 避免重复添加 systemdDefaultPath
			isDuplicate := false
			for _, fp := range foundPaths {
				if fp == p {
					isDuplicate = true
					break
				}
			}
			if !isDuplicate {
				foundPaths = append(foundPaths, p)
			}
		}
	}

	// 对找到的路径进行排序，使显示顺序一致
	sort.Strings(foundPaths)

	// 3. 确定 initialActivePath
	if systemdDefaultPath != "" && isValidConfigDir(systemdDefaultPath) {
		initialActivePath = systemdDefaultPath
	} else if len(foundPaths) > 0 {
		initialActivePath = foundPaths[0] // 如果 systemd 路径无效，则默认第一个找到的
	}

	currentConfigPathMutex.Lock()
	currentConfigPath = initialActivePath
	currentConfigPathMutex.Unlock()

	log.Printf("初始化完成。找到路径: %v, systemd默认: %s, 当前活动: %s", foundPaths, systemdDefaultPath, currentConfigPath)
	return
}

// detectSystemdConfigPath 尝试从 systemd 服务文件检测 Sing-box 配置路径
func detectSystemdConfigPath() string {
	serviceFiles := []string{
		"/etc/systemd/system/sing-box.service",
		"/etc/systemd/system/singbox.service",
		"/usr/lib/systemd/system/sing-box.service", // 增加一些常见的系统服务路径
		"/lib/systemd/system/sing-box.service",
	}

	for _, serviceFile := range serviceFiles {
		content, err := ioutil.ReadFile(serviceFile)
		if err != nil {
			continue
		}

		// 查找 ExecStart
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ExecStart=") {
				// 查找 -C 参数
				parts := strings.Fields(line)
				for i, part := range parts {
					if (part == "-C" || part == "-D") && i+1 < len(parts) { // 兼容 -C 或 -D
						configRoot := strings.TrimSpace(parts[i+1])
						// 修复：直接使用读取到的路径，不再拼接 "conf"
						// 使用 Clean 清理路径中的多余斜杠
						detectedPath := filepath.Clean(configRoot)
						log.Printf("通过 ExecStart %s 参数检测到配置路径: %s (来自 %s)", part, detectedPath, serviceFile)
						return detectedPath
					}
				}
			}
		}
	}
	return ""
}