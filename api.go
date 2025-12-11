package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// rootHandler 处理根路径 "/" 请求的函数。
func rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := templates.Execute(w, nil)
	if err != nil {
		log.Printf("错误: 渲染模板失败: %v", err)
		http.Error(w, "服务器内部错误：无法渲染页面。", http.StatusInternalServerError)
		return
	}
}

// resolvePath 辅助函数（翻译官）
func resolvePath(content []byte, userPath string) string {
	if userPath == "" {
		return ""
	}
	parts := strings.SplitN(userPath, ".", 2)
	if len(parts) != 2 {
		return userPath
	}
	rootKey := parts[0]
	tagOrKey := parts[1]

	if rootKey == "outbounds" || rootKey == "inbounds" {
		list := gjson.GetBytes(content, rootKey)
		if list.IsArray() {
			realIndex := -1
			list.ForEach(func(key, value gjson.Result) bool {
				if value.Get("tag").String() == tagOrKey {
					realIndex = int(key.Int())
					return false
				}
				return true
			})
			if realIndex != -1 {
				return fmt.Sprintf("%s.%d", rootKey, realIndex)
			}
		}
	}
	return userPath
}

// GetTopKeysResponse ...
type GetTopKeysResponse struct {
	RootContextKey string   `json:"root_context_key,omitempty"`
	Keys           []string `json:"keys"`
}

// getTopKeysHandler ...
func getTopKeysHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "只支持 GET 请求", http.StatusMethodNotAllowed)
		return
	}
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		writeJSONError(w, "缺少 'filename' 参数", http.StatusBadRequest)
		return
	}
	filePath, err := validateFilename(filename)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusForbidden)
		return
	}
	contentBytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		writeJSONError(w, fmt.Sprintf("无法读取文件 '%s': %v", filename, err), http.StatusInternalServerError)
		return
	}
	result := gjson.ParseBytes(contentBytes)
	if !result.IsObject() {
		writeJSONError(w, "文件内容不是一个有效的JSON对象。", http.StatusBadRequest)
		return
	}
	var topLevelKeys []string
	result.ForEach(func(key, value gjson.Result) bool {
		topLevelKeys = append(topLevelKeys, key.Str)
		return true
	})
	response := GetTopKeysResponse{
		RootContextKey: "",
		Keys:           topLevelKeys,
	}
	if len(topLevelKeys) == 1 {
		singleTopKey := topLevelKeys[0]
		singleTopKeyValue := gjson.GetBytes(contentBytes, singleTopKey)
		if singleTopKeyValue.IsObject() || singleTopKeyValue.IsArray() {
			var innerKeys []string
			singleTopKeyValue.ForEach(func(innerKey, innerValue gjson.Result) bool {
				if (singleTopKey == "outbounds" || singleTopKey == "inbounds") && singleTopKeyValue.IsArray() {
					tag := innerValue.Get("tag").String()
					if tag != "" {
						innerKeys = append(innerKeys, tag)
					} else {
						innerKeys = append(innerKeys, innerKey.Str)
					}
				} else {
					innerKeys = append(innerKeys, innerKey.Str)
				}
				return true
			})
			response.RootContextKey = singleTopKey
			response.Keys = innerKeys
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(response)
}

// getFileContentHandler ...
func getFileContentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "只支持 GET 请求", http.StatusMethodNotAllowed)
		return
	}
	filename := r.URL.Query().Get("filename")
	userPath := r.URL.Query().Get("path")
	filePath, err := validateFilename(filename)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusForbidden)
		return
	}
	contentBytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, "File not found.", http.StatusNotFound)
		} else {
			writeJSONError(w, "服务器内部错误：无法读取文件内容。", http.StatusInternalServerError)
		}
		return
	}
	var resultString string
	if userPath != "" {
		realPath := resolvePath(contentBytes, userPath)
		value := gjson.GetBytes(contentBytes, realPath)
		if !value.Exists() {
			writeJSONError(w, fmt.Sprintf("路径 '%s' (解析为 '%s') 不存在。", userPath, realPath), http.StatusNotFound)
			return
		}
		resultString = value.String()
	} else {
		resultString = string(contentBytes)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(resultString))
}

// SaveRequestData ...
type SaveRequestData struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
	Path     string `json:"path,omitempty"`
}

// saveFileContentHandler 处理 /api/save_content 请求。
// 修改点：使用 SetRawBytes 彻底绕过标准库 JSON 校验。
func saveFileContentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "只支持 POST 请求", http.StatusMethodNotAllowed)
		return
	}

	var reqData SaveRequestData
	err := json.NewDecoder(r.Body).Decode(&reqData)
	if err != nil {
		writeJSONError(w, "无效的请求体：无法解析JSON", http.StatusBadRequest)
		return
	}

	filename := reqData.Filename
	contentToSave := reqData.Content
	userPath := reqData.Path

	log.Printf("收到来自 %s 的保存文件请求: %s (path: %s)", r.RemoteAddr, filename, userPath)

	filePath, err := validateFilename(filename)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusForbidden)
		return
	}

	finalContentBytes := []byte(contentToSave)

	if userPath != "" {
		originalContentBytes, err := ioutil.ReadFile(filePath)
		if err != nil {
			writeJSONError(w, fmt.Sprintf("无法读取原文件: %v", err), http.StatusInternalServerError)
			return
		}

		realPath := resolvePath(originalContentBytes, userPath)

		var updatedContent []byte

		// ****** 终极修正：使用 SetRawBytes ******
		// 1. 判断是否像是复杂结构（对象或数组）
		trimmedContent := strings.TrimSpace(contentToSave)
		isLikeJSON := (strings.HasPrefix(trimmedContent, "{") && strings.HasSuffix(trimmedContent, "}")) ||
			(strings.HasPrefix(trimmedContent, "[") && strings.HasSuffix(trimmedContent, "]"))

		if isLikeJSON {
			log.Printf("检测到 JSON 结构，使用 SetRawBytes 强制写入（允许注释）。")
			// SetRawBytes 不会检查 contentToSave 是否合法，直接插入字节
			// 这样就完美支持了 /* 注释 */
			updatedContent, err = sjson.SetRawBytes(originalContentBytes, realPath, []byte(contentToSave))
		} else {
			log.Printf("检测到普通字符串，使用 SetBytes 自动转义写入。")
			// 如果是普通字符串（比如 "debug"），还是用这个，它会自动加引号变成 "debug"
			updatedContent, err = sjson.SetBytes(originalContentBytes, realPath, contentToSave)
		}
		// ****** 修正结束 ******

		if err != nil {
			log.Printf("路径修改失败: 文件 '%s', 路径 '%s' -> '%s', 错误: %v", filename, userPath, realPath, err)
			writeJSONError(w, fmt.Sprintf("修改失败：%v", err), http.StatusBadRequest)
			return
		}

		finalContentBytes = updatedContent
	}

	err = ioutil.WriteFile(filePath, finalContentBytes, 0644)
	if err != nil {
		log.Printf("无法写入文件 %s: %v", filePath, err)
		writeJSONError(w, "保存文件失败，请检查权限。", http.StatusInternalServerError)
		return
	}

	writeJSONResponse(w, "success", "文件保存成功！", http.StatusOK)
}

// restartSingboxHandler ...
func restartSingboxHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "只支持 POST 请求", http.StatusMethodNotAllowed)
		return
	}
	cmd := exec.Command("sudo", "systemctl", "restart", "sing-box")
	output, err := cmd.CombinedOutput()
	if err != nil {
		writeJSONError(w, fmt.Sprintf("重启服务失败：%v, 详情：%s", err, string(output)), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, "success", "Sing-box 服务已成功重启！", http.StatusOK)
}

// checkConfigHandler ...
func checkConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "只支持 POST 请求", http.StatusMethodNotAllowed)
		return
	}
	currentConfigPathMutex.RLock()
	activePath := currentConfigPath
	currentConfigPathMutex.RUnlock()
	if activePath == "" {
		writeJSONError(w, "未设置活动配置目录。", http.StatusServiceUnavailable)
		return
	}
	cmd := exec.Command("sing-box", "check", "-C", activePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		writeJSONError(w, fmt.Sprintf("配置检查失败：\n%s", string(output)), http.StatusInternalServerError)
		return
	}
	if len(output) == 0 {
		writeJSONResponse(w, "success", "配置检查成功，无错误！", http.StatusOK)
	} else {
		writeJSONError(w, fmt.Sprintf("配置检查失败：\n%s", string(output)), http.StatusInternalServerError)
	}
}

// 补充剩余结构体和函数...
type GetConfigPathsResponse struct {
	FoundPaths        []string `json:"found_paths"`
	SystemdDefault    string   `json:"systemd_default"`
	CurrentActivePath string   `json:"current_active_path"`
}

func getConfigPathsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "只支持 GET 请求", http.StatusMethodNotAllowed)
		return
	}
	foundPaths, systemdDefaultPath, initialActivePath := initConfigPaths()
	resp := GetConfigPathsResponse{
		FoundPaths:        foundPaths,
		SystemdDefault:    systemdDefaultPath,
		CurrentActivePath: initialActivePath,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}

type SetActiveConfigPathRequest struct {
	Path string `json:"path"`
}

func setActiveConfigPathHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "只支持 POST 请求", http.StatusMethodNotAllowed)
		return
	}
	var req SetActiveConfigPathRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		writeJSONError(w, "无效的请求体", http.StatusBadRequest)
		return
	}
	newPath := filepath.Clean(req.Path)
	if !isValidConfigDir(newPath) {
		writeJSONError(w, fmt.Sprintf("路径 '%s' 不存在或不可读。", newPath), http.StatusBadRequest)
		return
	}
	currentConfigPathMutex.Lock()
	currentConfigPath = newPath
	currentConfigPathMutex.Unlock()
	writeJSONResponse(w, "success", fmt.Sprintf("已成功设置配置目录为 '%s'。", newPath), http.StatusOK)
}

type FunctionalConfigResponse struct {
	OrderedFunctionalConfig []ConfigTypeInfo `json:"ordered_functional_config"`
	ConfigFiles             []string         `json:"config_files"`
	ActiveConfigPath        string           `json:"active_config_path"`
}

func getFunctionalConfigsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "只支持 GET 请求", http.StatusMethodNotAllowed)
		return
	}
	currentConfigPathMutex.RLock()
	baseDir := currentConfigPath
	currentConfigPathMutex.RUnlock()
	if baseDir == "" {
		writeJSONError(w, "未设置配置目录。", http.StatusServiceUnavailable)
		return
	}

	var configFiles []string
	tempFunctionalMap := make(map[string][]string)

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		writeJSONError(w, fmt.Sprintf("无法读取配置目录 '%s'。", baseDir), http.StatusInternalServerError)
		return
	}

	var unmatchedFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			filename := entry.Name()
			configFiles = append(configFiles, filename)
			filePath := filepath.Join(baseDir, filename)
			content, err := ioutil.ReadFile(filePath)
			if err != nil {
				continue
			}
			result := gjson.ParseBytes(content)
			if !result.IsObject() {
				unmatchedFiles = append(unmatchedFiles, filename)
				continue
			}
			matched := false
			result.ForEach(func(key, value gjson.Result) bool {
				if info, ok := configTypeMap[key.Str]; ok {
					tempFunctionalMap[info.FunctionName] = append(tempFunctionalMap[info.FunctionName], filename)
					matched = true
					return false
				}
				return true
			})
			if !matched {
				unmatchedFiles = append(unmatchedFiles, filename)
			}
		}
	}
	sort.Strings(configFiles)
	var orderedFunctionalConfig []ConfigTypeInfo
	for _, info := range configTypeMap {
		if fileList, ok := tempFunctionalMap[info.FunctionName]; ok {
			sort.Strings(fileList)
			if len(fileList) == 1 {
				orderedFunctionalConfig = append(orderedFunctionalConfig, ConfigTypeInfo{
					FunctionName: info.FunctionName,
					FileName:     fileList[0],
					Order:        info.Order,
				})
			} else {
				for i, fileName := range fileList {
					orderedFunctionalConfig = append(orderedFunctionalConfig, ConfigTypeInfo{
						FunctionName: fmt.Sprintf("%s %d", info.FunctionName, i+1),
						FileName:     fileName,
						Order:        info.Order,
					})
				}
			}
		}
	}
	sort.Slice(orderedFunctionalConfig, func(i, j int) bool {
		if orderedFunctionalConfig[i].Order == orderedFunctionalConfig[j].Order {
			return orderedFunctionalConfig[i].FunctionName < orderedFunctionalConfig[j].FunctionName
		}
		return orderedFunctionalConfig[i].Order < orderedFunctionalConfig[j].Order
	})
	sort.Strings(unmatchedFiles)
	for _, filename := range unmatchedFiles {
		funcName := fmt.Sprintf("其他-%s", strings.TrimSuffix(filename, ".json"))
		orderedFunctionalConfig = append(orderedFunctionalConfig, ConfigTypeInfo{
			FunctionName: funcName,
			FileName:     filename,
			Order:        len(configTypeMap) + 1,
		})
	}
	resp := FunctionalConfigResponse{
		OrderedFunctionalConfig: orderedFunctionalConfig,
		ConfigFiles:             configFiles,
		ActiveConfigPath:        baseDir,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}