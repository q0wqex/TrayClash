package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	_ "embed"
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")

	pm  *ProcessManager
	api *MihomoAPI

	//go:embed assets/icon.ico
	iconData []byte
)

func showMessage(title, text string) {
	tPtr, _ := syscall.UTF16PtrFromString(title)
	mPtr, _ := syscall.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(mPtr)), uintptr(unsafe.Pointer(tPtr)), 0)
}

func askYesNo(title, text string) bool {
	tPtr, _ := syscall.UTF16PtrFromString(title)
	mPtr, _ := syscall.UTF16PtrFromString(text)
	// MB_YESNO | MB_ICONQUESTION = 0x4 | 0x20 = 0x24
	ret, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(mPtr)), uintptr(unsafe.Pointer(tPtr)), 0x24)
	return ret == 6 // IDYES = 6
}

func parseDeepLink(arg string) (string, error) {
	if !strings.HasPrefix(arg, "tray-clash:") {
		return "", fmt.Errorf("invalid scheme")
	}

	normalized := arg
	if !strings.HasPrefix(normalized, "tray-clash://") {
		normalized = "tray-clash://" + strings.TrimPrefix(normalized, "tray-clash:")
	}

	u, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}

	if u.Host != "install-config" {
		return "", fmt.Errorf("unknown command: %s", u.Host)
	}

	linkURL := u.Query().Get("url")
	if linkURL == "" {
		return "", fmt.Errorf("missing url parameter")
	}

	return linkURL, nil
}

func escapePS(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func inputBox(title, prompt, defaultVal string) string {
	tmpFile := exeDir() + "\\~inputtmp.txt"
	psCmd := `Add-Type -AssemblyName Microsoft.VisualBasic; ` +
		`$r=[Microsoft.VisualBasic.Interaction]::InputBox('` + escapePS(prompt) + `','` + escapePS(title) + `','` + escapePS(defaultVal) + `'); ` +
		`[System.IO.File]::WriteAllText('` + tmpFile + `',$r)`
	cmd := exec.Command("powershell.exe", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", psCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()
	data, err := os.ReadFile(tmpFile)
	os.Remove(tmpFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(string(data), "\xef\xbb\xbf"))
}

// isProcessRunning — ядро запущено
func isProcessRunning(pm *ProcessManager) bool {
	return pm.IsRunning()
}

const (
	maxProxyItems      = 64
	maxSubItems        = 16
	maxGroups          = 15
	maxProxiesPerGroup = 64
)

type GroupMenu struct {
	Item    *systray.MenuItem
	Proxies []*systray.MenuItem
}


func prepareResources() {
	// Extract embedded files to AppData
	dir := exeDir()
	
	files := []struct {
		name string
		data []byte
	}{
		{"mihomo.exe", coreBytes},
		{"wintun.dll", tunBytes},
	}

	for _, f := range files {
		path := filepath.Join(dir, f.name)
		
		// Проверяем, нужно ли перезаписать (если файла нет или размер отличается)
		if info, err := os.Stat(path); err != nil || info.Size() != int64(len(f.data)) {
			os.WriteFile(path, f.data, 0755)
		}
	}
}

func onReady() {
	prepareResources()
	systray.SetIcon(iconData)
	systray.SetTitle("TrayClash")
	systray.SetTooltip("TrayClash")

	pm = NewProcessManager()
	api = NewMihomoAPI(ReadAPIPortFromConfig())

	// ── 1. Переключатель ──────────────────────────────────────────
	mToggle := systray.AddMenuItem("Включить", "Включить / Выключить Mihomo")
	mPanel := systray.AddMenuItem("Панель", "Открыть панель управления Zashboard")

	// ── 2. Подписки ──────────────────────────────────────────────
	mSubs := systray.AddMenuItem("Подписки", "Управление подписками")
	subPool := make([]*systray.MenuItem, maxSubItems)
	for i := range subPool {
		subPool[i] = mSubs.AddSubMenuItem("", "")
		subPool[i].Hide()
	}
	mAddSub := mSubs.AddSubMenuItem("Добавить подписку...", "Добавить новый URL")
	mDelSub := mSubs.AddSubMenuItem("Удалить текущую", "Удалить выбранную подписку")

	systray.AddSeparator()

	// ── 3. Прокси ────────────────────────────────────────────────
	groupMenus := make([]GroupMenu, maxGroups)
	for g := 0; g < maxGroups; g++ {
		groupMenus[g].Item = systray.AddMenuItem("", "")
		groupMenus[g].Item.Hide()
		groupMenus[g].Proxies = make([]*systray.MenuItem, maxProxiesPerGroup)
		for p := 0; p < maxProxiesPerGroup; p++ {
			groupMenus[g].Proxies[p] = groupMenus[g].Item.AddSubMenuItem("", "")
			groupMenus[g].Proxies[p].Hide()
		}
	}

	type GroupData struct {
		Name string
		All  []string
	}

	var (
		proxyMu          sync.Mutex
		activeGroupsData []GroupData
	)

	// Канал для обработки кликов по прокси
	type proxyClick struct {
		groupIndex int
		proxyIndex int
	}
	proxyClickCh := make(chan proxyClick, 15)

	for g := 0; g < maxGroups; g++ {
		groupIndex := g
		for p := 0; p < maxProxiesPerGroup; p++ {
			proxyIndex := p
			go func(item *systray.MenuItem) {
				for range item.ClickedCh {
					proxyClickCh <- proxyClick{groupIndex, proxyIndex}
				}
			}(groupMenus[g].Proxies[p])
		}
	}


	// updateProxies: берёт все Selector-группы в порядке их расположения в конфиге
	updateProxies := func() {
		groups, err := api.GetProxyGroups()
		if err != nil {
			return
		}

		orderedNames := GetOrderedSelectGroupsFromConfig()

		var displayGroups []ProxyGroup
		var displayNames []string

		// Сначала добавляем группы в порядке из конфига, если они есть в ответе API
		for _, name := range orderedNames {
			if g, ok := groups[name]; ok {
				displayGroups = append(displayGroups, g)
				displayNames = append(displayNames, name)
				delete(groups, name)
			}
		}

		// Если остались другие группы (кроме GLOBAL), добавим их в алфавитном порядке
		var extraNames []string
		for name := range groups {
			if !strings.EqualFold(name, "GLOBAL") {
				extraNames = append(extraNames, name)
			}
		}
		
		for _, name := range extraNames {
			if g, ok := groups[name]; ok {
				displayGroups = append(displayGroups, g)
				displayNames = append(displayNames, name)
			}
		}

		// Если совсем ничего нет, но есть GLOBAL
		if len(displayNames) == 0 {
			if g, ok := groups["GLOBAL"]; ok {
				displayGroups = append(displayGroups, g)
				displayNames = append(displayNames, "GLOBAL")
			}
		}

		proxyMu.Lock()
		activeGroupsData = make([]GroupData, len(displayNames))
		for i, name := range displayNames {
			activeGroupsData[i] = GroupData{
				Name: name,
				All:  displayGroups[i].All,
			}
		}
		proxyMu.Unlock()

		for i := 0; i < maxGroups; i++ {
			if i < len(displayNames) {
				name := displayNames[i]
				g := displayGroups[i]

				title := name
				if i == 0 {
					title = "★ " + name
				} else {
					title = "  " + name
				}

				groupMenus[i].Item.SetTitle(title)
				groupMenus[i].Item.Show()

				for p := 0; p < maxProxiesPerGroup; p++ {
					if p < len(g.All) {
						label := g.All[p]
						if g.Now == label {
							label = "✓ " + label
						}
						groupMenus[i].Proxies[p].SetTitle(label)
						groupMenus[i].Proxies[p].Show()
					} else {
						groupMenus[i].Proxies[p].Hide()
					}
				}
			} else {
				groupMenus[i].Item.Hide()
			}
		}
	}


	updateSubs := func() {
		cfg, _ := LoadSubConfig()
		for i, item := range subPool {
			if i < len(cfg.Subscriptions) {
				sub := cfg.Subscriptions[i]
				title := sub.Name
				if i == cfg.ActiveIndex {
					title = "✓ " + title
				}
				item.SetTitle(title)
				item.Show()
			} else {
				item.Hide()
			}
		}
	}
	updateSubs()

	subClickCh := make(chan int, 5)
	for i := range subPool {
		idx := i
		go func(item *systray.MenuItem) {
			for range item.ClickedCh {
				subClickCh <- idx
			}
		}(subPool[i])
	}

	// ── 4. Настройки ─────────────────────────────────────────────
	systray.AddSeparator()
	mSettings := systray.AddMenuItem("Настройки", "Параметры")
	mOpenFolder := mSettings.AddSubMenuItem("Открыть папку с данными", "Открыть папку в AppData")
	mInstall := mSettings.AddSubMenuItem("Добавить в автозагрузку", "Запускать TrayClash при старте Windows")
	mUninstall := mSettings.AddSubMenuItem("Убрать из автозагрузки", "Удалить из реестра")

	// ── 4. Выйти ─────────────────────────────────────────────────
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Выйти", "Остановить сервис и закрыть")

	// ─────────────────────────────────────────────────────────────
	// Оптимистичный апдейт заголовка переключателя
	setRunning := func(running bool) {
		if running {
			mToggle.SetTitle("Выключить")
		} else {
			mToggle.SetTitle("Включить")
		}
	}

	// Проверяем реальное состояние и обновляем заголовок
	syncToggle := func() {
		setRunning(isProcessRunning(pm))
	}
	syncToggle()

	// Проверяем флаг автостарта для автоматического включения туннеля
	isAutostart := false
	for _, arg := range os.Args {
		if arg == "--autostart" {
			isAutostart = true
			break
		}
	}

	if isAutostart && !isProcessRunning(pm) {
		go func() {
			// Небольшая задержка, чтобы UI успел инициализироваться
			time.Sleep(500 * time.Millisecond)
			mToggle.ClickedCh <- struct{}{}
		}()
	}

	// Если уже запущен — загружаем прокси сразу
	if isProcessRunning(pm) {
		go func() {
			time.Sleep(300 * time.Millisecond)
			updateProxies()
		}()
	}

	ticker := time.NewTicker(5 * time.Second)

	go func() {
		for {
			select {

			case link := <-deepLinkCh:
				linkURL, err := parseDeepLink(link)
				if err != nil {
					showMessage("Ошибка диплинка", "Неверный формат ссылки:\n"+err.Error())
					continue
				}

				if !askYesNo("Импорт подписки", "Хотите добавить и активировать новую подписку?\nURL: "+linkURL) {
					continue
				}

				device := GetDeviceInfo()
				tmpPath := filepath.Join(exeDir(), "tmp_config.yaml")
				res := DownloadConfig(linkURL, device, tmpPath)
				os.Remove(tmpPath)

				if res.Err != nil {
					showMessage("Ошибка", "Не удалось получить данные подписки:\n"+res.Err.Error())
					continue
				}

				name := res.ProfileTitle
				if name == "" {
					name = inputBox("Название подписки", "Сервер не вернул название. Введите вручную:", "Новая подписка")
				}
				if name == "" {
					continue
				}

				cfg, _ := LoadSubConfig()

				existsIdx := -1
				for idx, sub := range cfg.Subscriptions {
					if sub.URL == linkURL {
						existsIdx = idx
						break
					}
				}

				if existsIdx != -1 {
					cfg.Subscriptions[existsIdx].Name = name
					cfg.ActiveIndex = existsIdx
				} else {
					cfg.Subscriptions = append(cfg.Subscriptions, Subscription{Name: name, URL: linkURL})
					cfg.ActiveIndex = len(cfg.Subscriptions) - 1
				}

				SaveSubConfig(cfg)
				updateSubs()

				if isProcessRunning(pm) {
					pm.Stop()
					os.Remove(filepath.Join(exeDir(), "config.yaml"))
					go func() {
						time.Sleep(100 * time.Millisecond)
						mToggle.ClickedCh <- struct{}{}
					}()
				} else {
					os.Remove(filepath.Join(exeDir(), "config.yaml"))
					go func() {
						time.Sleep(100 * time.Millisecond)
						mToggle.ClickedCh <- struct{}{}
					}()
				}

			case <-ticker.C:
				running := isProcessRunning(pm)
				setRunning(running)
				if running {
					updateProxies()
				}

			case idx := <-subClickCh:
				cfg, _ := LoadSubConfig()
				if idx < len(cfg.Subscriptions) && idx != cfg.ActiveIndex {
					cfg.ActiveIndex = idx
					SaveSubConfig(cfg)
					updateSubs()

					if isProcessRunning(pm) {
						pm.Stop()
						os.Remove(filepath.Join(exeDir(), "config.yaml"))
						// Trigger restart logic (simulating mToggle click) after a short delay
						go func() {
							time.Sleep(100 * time.Millisecond)
							mToggle.ClickedCh <- struct{}{}
						}()
					} else {
						// Just remove config so next start uses new URL
						os.Remove(filepath.Join(exeDir(), "config.yaml"))
					}
				}

			case <-mAddSub.ClickedCh:
				url := inputBox("Новая подписка", "Введите URL подписки:", "")
				if url != "" {
					device := GetDeviceInfo()
					tmpPath := filepath.Join(exeDir(), "tmp_config.yaml")
					res := DownloadConfig(url, device, tmpPath)
					os.Remove(tmpPath) // We only needed headers, but DownloadConfig saves the file

					if res.Err != nil {
						showMessage("Ошибка", "Не удалось получить данные подписки:\n"+res.Err.Error())
						continue
					}

					name := res.ProfileTitle
					if name == "" {
						name = inputBox("Название подписки", "Сервер не вернул название. Введите вручную:", "Новая подписка")
					}
					if name == "" {
						continue
					}

					cfg, _ := LoadSubConfig()
					cfg.Subscriptions = append(cfg.Subscriptions, Subscription{Name: name, URL: url})
					if cfg.ActiveIndex == -1 {
						cfg.ActiveIndex = 0
					}
					SaveSubConfig(cfg)
					updateSubs()
				}

			case <-mDelSub.ClickedCh:
				cfg, _ := LoadSubConfig()
				if cfg.ActiveIndex >= 0 && cfg.ActiveIndex < len(cfg.Subscriptions) {
					cfg.Subscriptions = append(cfg.Subscriptions[:cfg.ActiveIndex], cfg.Subscriptions[cfg.ActiveIndex+1:]...)
					
					// Stop core and remove config if we deleted a subscription
					if isProcessRunning(pm) {
						pm.Stop()
					}
					os.Remove(filepath.Join(exeDir(), "config.yaml"))

					if len(cfg.Subscriptions) == 0 {
						cfg.ActiveIndex = -1
					} else {
						cfg.ActiveIndex = 0
					}
					SaveSubConfig(cfg)
					updateSubs()
					syncToggle()
				}

			case click := <-proxyClickCh:
				proxyMu.Lock()
				var groupName, proxyName string
				if click.groupIndex < len(activeGroupsData) {
					groupData := activeGroupsData[click.groupIndex]
					groupName = groupData.Name
					if click.proxyIndex < len(groupData.All) {
						proxyName = groupData.All[click.proxyIndex]
					}
				}
				proxyMu.Unlock()

				if groupName != "" && proxyName != "" {
					if err := api.SelectProxy(groupName, proxyName); err != nil {
						showMessage("Ошибка выбора прокси", err.Error())
					} else {
						updateProxies()
					}
				}

			// ── Переключатель ────────────────────────────────────
			case <-mToggle.ClickedCh:
				if isProcessRunning(pm) {
					// --- Выключить ---
					setRunning(false) // оптимистично
					if err := pm.Stop(); err != nil {
						showMessage("Ошибка", "Не удалось остановить ядро:\n"+err.Error())
						syncToggle() // откат
					}
					// Скрываем прокси
					for i := 0; i < maxGroups; i++ {
						groupMenus[i].Item.Hide()
					}
				} else {
					// --- Включить ---
					configPath := exeDir() + "\\config.yaml"

					cfg, _ := LoadSubConfig()
					// Если конфига нет ИЛИ нет активной подписки — нужно получить URL
					if _, err := os.Stat(configPath); os.IsNotExist(err) || cfg.ActiveIndex == -1 {
						url := ""
						if cfg.ActiveIndex >= 0 {
							url = cfg.Subscriptions[cfg.ActiveIndex].URL
						}

						if url == "" {
							url = inputBox("Ссылка на конфиг", "Введите URL подписки:", "")
							if url == "" {
								showMessage("Отмена", "URL не указан. Запуск отменён.")
								continue
							}
							cfg, _ = LoadSubConfig() // reload just in case
							cfg.Subscriptions = append(cfg.Subscriptions, Subscription{Name: "По умолчанию", URL: url})
							cfg.ActiveIndex = len(cfg.Subscriptions) - 1
							SaveSubConfig(cfg)
							updateSubs()
						}
						device := GetDeviceInfo()
						res := DownloadConfig(url, device, configPath)
						if res.MaxDevicesReached || res.HWIDLimit {
							showMessage("Лимит устройств", "Достигнут максимум разрешённых устройств.")
							continue
						}
						if res.HWIDNotSupported {
							showMessage("HWID", "Сервер требует HWID-идентификацию.")
							continue
						}
						if res.Err != nil {
							showMessage("Ошибка загрузки конфига", res.Err.Error())
							continue
						}
						// Update name if server provided one
						if res.ProfileTitle != "" {
							cfg, _ := LoadSubConfig()
							if cfg.ActiveIndex >= 0 {
								cfg.Subscriptions[cfg.ActiveIndex].Name = res.ProfileTitle
								SaveSubConfig(cfg)
								updateSubs()
							}
						}
					} else {
						// Config exists, but maybe we want to update it?
						// In the current logic, if it exists, we just use it.
						// But if we want to ensure we have the latest name:
						// Let's at least make sure DownloadConfig elsewhere also updates the name.
					}

					// Дописываем external-controller если нет
					if err := EnsureExternalController(configPath, "127.0.0.1:9090"); err != nil {
						showMessage("Ошибка", "Не удалось обновить config.yaml:\n"+err.Error())
						continue
					}

					// Проверяем поддержку IPv6 и отключаем в конфиге при необходимости
					_ = AutoPatchIPv6IfNeeded(configPath)

					// Запускаем
					if err := pm.Start(); err != nil {
						showMessage("Ошибка запуска", err.Error())
						syncToggle()
					} else {
						setRunning(true) // оптимистично
						go func() {
							retryWithBackoff(5, 500*time.Millisecond, func() error {
								_, err := api.GetProxyGroups()
								return err
							})
							updateProxies()
							setRunning(isProcessRunning(pm)) // подтверждение реального статуса
						}()
					}
				}

			// ── Настройки ────────────────────────────────────────
			case <-mOpenFolder.ClickedCh:
				runHidden("cmd", "/c", "start", "", exeDir()).Run()

			case <-mInstall.ClickedCh:
				if err := pm.Install(); err != nil {
					showMessage("Ошибка", err.Error())
				} else {
					showMessage("Успех", "Автозагрузка включена")
				}

			case <-mUninstall.ClickedCh:
				if err := pm.Uninstall(); err != nil {
					showMessage("Ошибка", err.Error())
				} else {
					showMessage("Успех", "Автозагрузка выключена")
				}

			case <-mPanel.ClickedCh:
				runHidden("cmd", "/c", "start", "http://board.zash.run.place").Run()

			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	if pm != nil {
		pm.Stop()
	}
	os.Exit(0)
}
