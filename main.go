package main

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
)

//go:embed templates/*
var staticContent embed.FS

// 解析HTML模板
var templates = template.Must(template.ParseFS(staticContent, "templates/index.html"))

func main() {
	// 1. 初始化配置路径 (来自 config.go)
	_, _, _ = initConfigPaths()

	addr := "0.0.0.0:80"

	// 2. 注册路由 (处理函数都在 api.go 中)
	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/api/get_config_paths", getConfigPathsHandler)
	http.HandleFunc("/api/set_active_config_path", setActiveConfigPathHandler)
	http.HandleFunc("/api/get_functional_configs", getFunctionalConfigsHandler)
	http.HandleFunc("/api/get_top_keys", getTopKeysHandler)
	http.HandleFunc("/api/get_content", getFileContentHandler)
	http.HandleFunc("/api/save_content", saveFileContentHandler)
	http.HandleFunc("/api/restart_singbox", restartSingboxHandler)
	http.HandleFunc("/api/check_config", checkConfigHandler)

	// 3. 打印启动信息
	fmt.Printf("Go Web 服务器正在监听地址: %s\n", addr)
	fmt.Println("您可以通过在浏览器中访问以下地址来测试：")
	fmt.Println("  - 主页: http://localhost/")
	fmt.Println("  - 获取可用配置路径: http://localhost/api/get_config_paths")
	fmt.Println("  - 如果在远程服务器上运行，请将 localhost 替换为服务器的IP地址。")
	fmt.Println("\n***** 注意事项 *****")
	fmt.Println("1. 端口 80 是特权端口，程序可能需要 root 权限运行。")
	fmt.Println("2. 请确保已配置 sudo 免密重启权限。")

	// 4. 启动服务器
	err := http.ListenAndServe(addr, nil)
	if err != nil {
		log.Fatalf("无法启动服务器: %v", err)
	}
}
