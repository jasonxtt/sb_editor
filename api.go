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

// resolvePath 是一个关键的辅助函数（翻译官）。
// 它的作用是将前端传来的基于 Tag 的路径（如 "outbounds.绿云"）
// 转换为 gjson/sjson 能理解的基于索引的路径（如 "outbounds.0"）。
func resolvePath(content []byte, userPath string) string {
	// 如果路径为空，直接返回
	if userPath == "" {
		return ""
	}

	// 尝试分割路径，例如 "outbounds.绿云" -> ["outbounds", "绿云"]
	parts := strings.SplitN(userPath, ".", 2)
	if len(parts) != 2 {
		// 如果不是 "root.sub" 这种格式（例如只是 "log"），不需要翻译
		return userPath
	}

	rootKey := parts[0]
	tagOrKey := parts[1]

	// 只有当根键是 outbounds 或 inbounds 时，才启动 Tag 翻译逻辑
	if rootKey == "outbounds" || rootKey == "inbounds" {
		// 获取对应的数组
		list := gjson.GetBytes(content, rootKey)
		if list.IsArray() {
			realIndex := -1
			// 遍历数组，寻找 tag 匹配的元素
			list.ForEach(func(key, value gjson.Result) bool {
				// 尝试获取该项的 tag
				if value.Get("tag").String() == tagOrKey {
					// 找到了！记录索引（key.Int() 就是数组下标）
					realIndex = int(key.Int())
					return false // 停止遍历
				}
				return true // 继续遍历
			})

			// 如果找到了对应的 Tag，返回真实的索引路径，例如 "outbounds.0"
			if realIndex != -1 {
				return fmt.Sprintf("%s.%d", rootKey, realIndex)
			}
		}
	}

	// 如果没触发特殊逻辑，或者没找到 Tag，原样返回路径
	// 这保证了其他普通配置（如 dns.servers）依然正常工作
	return userPath
}

// GetTopKeysResponse 定义 /api/get_top_keys 接口的响应结构。
type GetTopKeysResponse struct {
	RootContextKey string   `json:"root_context_key,omitempty"`
	Keys           []string `json:"keys"`
}

// getTopKeysHandler 处理 /api/get_top_keys 请求。
// 修改点：现在会优先提取 tag 作为键名显示。
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

	// 智能钻取逻辑：如果只有一个根键（如 outbounds），则显示其内部列表
	if len(topLevelKeys) == 1 {
		singleTopKey := topLevelKeys[0]
		singleTopKeyValue := gjson.GetBytes(contentBytes, singleTopKey)

		// 如果值是数组（outbounds/inbounds）或对象
		if singleTopKeyValue.IsObject() || singleTopKeyValue.IsArray() {
			var innerKeys []string
			singleTopKeyValue.ForEach(func(innerKey, innerValue gjson.Result) bool {
				// ******* 修改开始：Tag 提取逻辑 *******
				// 只有在 outbounds 或 inbounds 数组中，才尝试提取 tag
				if (singleTopKey == "outbounds" || singleTopKey == "inbounds") && singleTopKeyValue.IsArray() {
					tag := innerValue.Get("tag").String()
					if tag != "" {
						// 如果有 tag，就用 tag 做名字（例如 "绿云"）
						innerKeys = append(innerKeys, tag)
					} else {
						// 如果没 tag，还用原来的索引（例如 "0"）
						innerKeys = append(innerKeys, innerKey.Str)
					}
				} else {
					// 其他情况（如 log, dns），保持原样显示键名
					innerKeys = append(innerKeys, innerKey.Str)
				}
				// ******* 修改结束 *******
				return true
			})
			response.RootContextKey = singleTopKey
			response.Keys = innerKeys
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(response)
}

// getFileContentHandler 处理 /api/get_content 请求。
// 修改点：增加了 resolvePath 调用。
func getFileContentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "只支持 GET 请求", http.StatusMethodNotAllowed)
		return
	}

	filename := r.URL.Query().Get("filename")
	userPath := r.URL.Query().Get("path") // 前端传来的可能是 "outbounds.绿云"

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
		// ******* 修改：翻译路径 *******
		realPath := resolvePath(contentBytes, userPath) // 翻译为 "outbounds.0"
		// ******* 结束 *******

		value := gjson.GetBytes(contentBytes, realPath)
		if !value.Exists() {
			// 如果没找到，可能用户刚修改了 Tag 导致路径变了，尝试直接读，或者报错
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

// SaveRequestData 用于解析保存文件API的JSON请求体。
type SaveRequestData struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
	Path     string `json:"path,omitempty"`
}

// saveFileContentHandler 处理 /api/save_content 请求。
// 修改点：增加了 resolvePath 调用。
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

		// ******* 修改：翻译路径 *******
		// 注意：这里用 originalContentBytes 来定位原来的 Tag 在哪个位置
		realPath := resolvePath(originalContentBytes, userPath)
		// ******* 结束 *******

		var updatedContent []byte
		if gjson.Valid(contentToSave) {
			updatedContent, err = sjson.SetBytes(originalContentBytes, realPath, json.RawMessage(contentToSave))
		} else {
			updatedContent, err = sjson.SetBytes(originalContentBytes, realPath, contentToSave)
		}

		if err != nil {
			log.Printf("路径修改失败: 文件 '%s', 路径 '%s' -> '%s', 错误: %v", filename, userPath, realPath, err)
			writeJSONError(w, fmt.Sprintf("修改失败：%v", err), http.StatusBadRequest)
			return
		}

		if !gjson.ValidBytes(updatedContent) {
			writeJSONError(w, "修改后的文件内容不是合法的JSON。", http.StatusBadRequest)
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

// restartSingboxHandler (保持不变)
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

// checkConfigHandler (保持不变)
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

// GetConfigPathsResponse ... (后续代码与之前逻辑一致，为节省篇幅只展示改动部分，但建议你复制上述完整代码以防遗漏)
// getConfigPathsHandler, setActiveConfigPathHandler 保持不变，
// 但为了保证 api.go 文件的完整性，下面补充剩余部分：

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

// getFunctionalConfigsHandler (注意这里包含之前“一对多”的逻辑)
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
	tempFunctionalMap := make(map[string][]string) // 支持一对多

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