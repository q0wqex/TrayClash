package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

type DeviceInfo struct {
	HWID  string
	OS    string
	OSVer string
	Model string
}

type DownloadResult struct {
	MaxDevicesReached bool
	HWIDLimit         bool
	HWIDNotSupported  bool
	ProfileTitle      string
	Err               error
}

func exeDir() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		// Fallback to actual exe dir if APPDATA is missing
		exe, _ := os.Executable()
		return filepath.Dir(exe)
	}
	dir := filepath.Join(appData, "TrayClash")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0755)
	}
	return dir
}

type Subscription struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type SubConfig struct {
	Subscriptions []Subscription `json:"subscriptions"`
	ActiveIndex   int            `json:"active_index"`
}

func (c *SubConfig) GetActive() *Subscription {
	if c.ActiveIndex >= 0 && c.ActiveIndex < len(c.Subscriptions) {
		return &c.Subscriptions[c.ActiveIndex]
	}
	return nil
}

func LoadSubConfig() (*SubConfig, error) {
	configPath := filepath.Join(exeDir(), "subscriptions.json")
	data, err := os.ReadFile(configPath)
	if err == nil {
		var cfg SubConfig
		if err := json.Unmarshal(data, &cfg); err == nil {
			return &cfg, nil
		}
	}

	// Migration from url.txt
	urlPath := filepath.Join(exeDir(), "url.txt")
	urlData, err := os.ReadFile(urlPath)
	if err == nil {
		url := strings.TrimSpace(string(urlData))
		if url != "" {
			cfg := &SubConfig{
				Subscriptions: []Subscription{{Name: "По умолчанию", URL: url}},
				ActiveIndex:   0,
			}
			SaveSubConfig(cfg)
			return cfg, nil
		}
	}

	return &SubConfig{Subscriptions: []Subscription{}, ActiveIndex: -1}, nil
}

func SaveSubConfig(cfg *SubConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(exeDir(), "subscriptions.json"), data, 0644)
}

func loadConfigURL() string {
	cfg, _ := LoadSubConfig()
	if active := cfg.GetActive(); active != nil {
		return active.URL
	}
	return ""
}

func GetDeviceInfo() DeviceInfo {
	info := DeviceInfo{HWID: "unknown", OS: "Windows", OSVer: "unknown", Model: "PC"}

	// 1. HWID (MachineGuid)
	cmd := exec.Command("reg", "query", "HKEY_LOCAL_MACHINE\\SOFTWARE\\Microsoft\\Cryptography", "/v", "MachineGuid")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.Output(); err == nil {
		parts := strings.Fields(string(out))
		if len(parts) >= 3 {
			info.HWID = parts[len(parts)-1]
		}
	}

	// 2. OS Version
	cmd = exec.Command("powershell", "-Command", "[System.Environment]::OSVersion.Version.ToString()")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.Output(); err == nil {
		info.OSVer = strings.TrimSpace(string(out))
	}

	// 3. Model
	cmd = exec.Command("wmic", "csproduct", "get", "name")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.Output(); err == nil {
		lines := strings.Split(string(out), "\n")
		if len(lines) >= 2 {
			info.Model = strings.TrimSpace(lines[1])
		}
	}

	return info
}

func DownloadConfig(url string, device DeviceInfo, destPath string) DownloadResult {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return DownloadResult{Err: err}
	}

	// Happ/Remnawave Standard Headers
	req.Header.Set("x-hwid", device.HWID)
	req.Header.Set("x-device-os", device.OS)
	req.Header.Set("x-ver-os", device.OSVer)
	req.Header.Set("x-device-model", device.Model)
	req.Header.Set("User-Agent", "clash-meta/tray")

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return DownloadResult{Err: err}
	}
	defer resp.Body.Close()

	res := DownloadResult{}
	
	// Parse Happ/Remnawave Response Headers
	if resp.Header.Get("x-hwid-max-devices-reached") == "true" || resp.Header.Get("x-hwid-limit") == "true" {
		res.MaxDevicesReached = true
		res.HWIDLimit = true
	}
	if resp.Header.Get("x-hwid-not-supported") == "true" {
		res.HWIDNotSupported = true
	}

	// Extract Profile-Title
	if titleRaw := resp.Header.Get("Profile-Title"); titleRaw != "" {
		if strings.HasPrefix(titleRaw, "base64:") {
			encoded := strings.TrimPrefix(titleRaw, "base64:")
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err == nil {
				res.ProfileTitle = string(decoded)
			}
		} else {
			res.ProfileTitle = titleRaw
		}
	}

	// Fallback to status codes if headers are missing but error occurs
	if resp.StatusCode == 403 && !res.HWIDLimit {
		res.HWIDLimit = true
	}
	if resp.StatusCode == 401 && !res.HWIDNotSupported {
		res.HWIDNotSupported = true
	}

	if resp.StatusCode != http.StatusOK {
		res.Err = fmt.Errorf("HTTP %s", resp.Status)
		return res
	}

	out, err := os.Create(destPath)
	if err != nil {
		res.Err = err
		return res
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	res.Err = err
	return res
}


func ReadAPIPortFromConfig() string {
	configPath := filepath.Join(exeDir(), "config.yaml")
	file, err := os.Open(configPath)
	if err != nil {
		return "9090"
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "external-controller:") {
			parts := strings.Split(trimmed, ":")
			if len(parts) >= 2 {
				port := strings.TrimSpace(parts[len(parts)-1])
				port = strings.Trim(port, "\"' ") // remove quotes and spaces
				if port != "" {
					return port
				}
			}
		}
	}
	return "9090"
}


func EnsureExternalController(path, controller string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	
	sContent := string(content)
	if !strings.Contains(sContent, "external-controller:") {
		newContent := "external-controller: " + controller + "\n" + sContent
		return os.WriteFile(path, []byte(newContent), 0644)
	}
	return nil
}

func isIPv6Supported() bool {
	// Try to listen on tcp6 wildcard address
	ln, err := net.Listen("tcp6", "[::]:0")
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func DisableIPv6InConfig(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	
	lines := strings.Split(string(content), "\n")
	modified := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ipv6:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				if val == "true" || val == "yes" {
					indent := line[:strings.Index(line, trimmed)]
					lines[i] = indent + "ipv6: false"
					modified = true
				}
			}
		}
	}
	
	if modified {
		newContent := strings.Join(lines, "\n")
		return os.WriteFile(path, []byte(newContent), 0644)
	}
	return nil
}

func AutoPatchIPv6IfNeeded(path string) error {
	// Always disable IPv6 in config on Windows to prevent DNS hijack resolution errors (e.g. 'lookup :: no such host')
	return DisableIPv6InConfig(path)
}

func GetOrderedSelectGroupsFromConfig() []string {
	configPath := filepath.Join(exeDir(), "config.yaml")
	file, err := os.Open(configPath)
	if err != nil {
		return nil
	}
	defer file.Close()

	var order []string
	scanner := bufio.NewScanner(file)
	inProxyGroups := false
	var currentName string
	var currentType string
	var currentHidden bool

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && trimmed != "" {
			if strings.HasPrefix(trimmed, "proxy-groups:") {
				inProxyGroups = true
				continue
			} else {
				inProxyGroups = false
			}
		}

		if !inProxyGroups {
			continue
		}

		if strings.HasPrefix(trimmed, "-") {
			if currentName != "" && (currentType == "select" || currentType == "") && !currentHidden {
				order = append(order, currentName)
			}
			currentName = ""
			currentType = ""
			currentHidden = false
			
			trimmed = strings.TrimPrefix(trimmed, "-")
			trimmed = strings.TrimSpace(trimmed)
		}

		if strings.HasPrefix(trimmed, "name:") {
			nameVal := strings.TrimPrefix(trimmed, "name:")
			nameVal = strings.TrimSpace(nameVal)
			nameVal = strings.Trim(nameVal, `"'`)
			currentName = nameVal
		} else if strings.HasPrefix(trimmed, "type:") {
			typeVal := strings.TrimPrefix(trimmed, "type:")
			typeVal = strings.TrimSpace(typeVal)
			typeVal = strings.Trim(typeVal, `"'`)
			currentType = typeVal
		} else if strings.HasPrefix(trimmed, "hidden:") {
			hiddenVal := strings.TrimPrefix(trimmed, "hidden:")
			hiddenVal = strings.TrimSpace(hiddenVal)
			if hiddenVal == "true" {
				currentHidden = true
			}
		}
	}
	if currentName != "" && (currentType == "select" || currentType == "") && !currentHidden {
		order = append(order, currentName)
	}

	return order
}


