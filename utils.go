package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ConfigTypeInfo 用于存储功能按钮的信息，以便排序。
type ConfigTypeInfo struct {
	FunctionName string
	FileName     string
	Order        int // 用于排序的额外字段
}

// configTypeMap 定义根键到功能名的映射及其显示顺序。
var configTypeMap = map[string]ConfigTypeInfo{
	"log":          {FunctionName: "日志", Order: 1},
	"experimental": {FunctionName: "实验性", Order: 2},
	"dns":          {FunctionName: "DNS", Order: 3},
	"inbounds":     {FunctionName: "入站", Order: 4},
	"outbounds":    {FunctionName: "出站", Order: 5},
	"route":        {FunctionName: "路由规则", Order: 6},
	"ntp":          {FunctionName: "NTP", Order: 7},
	"fakedns":      {FunctionName: "FakeDNS", Order: 8},
	"warp":         {FunctionName: "WARP", Order: 9},
	// 可以根据需要添加更多 Sing-box 配置的根键
}

// isValidConfigDir 验证给定路径是否存在且是一个可读的目录
func isValidConfigDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) && !os.IsPermission(err) {
			log.Printf("无法访问路径 '%s': %v", path, err)
		}
		return false
	}
	if !info.IsDir() {
		return false
	}
	// 尝试列出目录内容以验证可读性
	_, err = os.ReadDir(path)
	if err != nil {
		log.Printf("路径 '%s' 不可读: %v", path, err)
		return false
	}
	return true
}

// validateFilename 辅助函数，用于安全验证文件名。
// 确保文件存在于 currentConfigPath 且是 .json 文件，防止路径遍历。
func validateFilename(filename string) (string, error) {
	currentConfigPathMutex.RLock() // 读取锁
	baseDir := currentConfigPath
	currentConfigPathMutex.RUnlock()

	if baseDir == "" {
		return "", fmt.Errorf("未设置配置目录，请先选择一个目录。")
	}

	if filename == "" {
		return "", fmt.Errorf("文件名为空")
	}
	if !strings.HasSuffix(filename, ".json") {
		return "", fmt.Errorf("非法文件类型，只允许 .json 文件")
	}

	fullPath := filepath.Join(baseDir, filename)
	cleanPath := filepath.Clean(fullPath)

	// 再次验证清理后的路径是否仍然在 baseDir 内部
	if !strings.HasPrefix(cleanPath, filepath.Clean(baseDir)) {
		return "", fmt.Errorf("禁止访问配置目录之外的文件")
	}

	// 检查文件名是否在 baseDir 的根目录下实际存在
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return "", fmt.Errorf("无法验证文件名：读取配置目录失败：%v", err)
	}

	isValidFile := false
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() == filename {
			isValidFile = true
			break
		}
	}

	if !isValidFile {
		return "", fmt.Errorf("文件未找到或不允许在根配置目录中")
	}

	return cleanPath, nil
}

// writeJSONResponse 辅助函数，用于向客户端返回JSON格式的成功响应。
func writeJSONResponse(w http.ResponseWriter, status, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  status,
		"message": message,
	})
}

// writeJSONError 辅助函数，用于向客户端返回JSON格式的错误响应。
func writeJSONError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
