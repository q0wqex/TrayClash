package main

import (
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
	maxProxyItems = 64
	maxSubItems   = 16
)

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
	mProxies := systray.AddMenuItem("Прокси", "Список прокси активной группы")
	mProxies.Disable() // включится когда загрузятся прокси

	proxyPool := make([]*systray.MenuItem, maxProxyItems)
	for i := range proxyPool {
		proxyPool[i] = mProxies.AddSubMenuItem("", "")
		proxyPool[i].Hide()
	}

	var (
		proxyMu     sync.Mutex
		activeGroup string
		proxyNames  []string
	)

	// Канал для обработки кликов по прокси
	type proxyClick struct {
		idx int
	}
	proxyClickCh := make(chan proxyClick, 5)

	for i := range proxyPool {
		idx := i
		go func(item *systray.MenuItem) {
			for range item.ClickedCh {
				proxyClickCh <- proxyClick{idx}
			}
		}(proxyPool[i])
	}

	// updateProxies: берёт первую пользовательскую Selector-группу (не GLOBAL)
	updateProxies := func() {
		groups, err := api.GetProxyGroups()
		if err != nil {
			return
		}

		// Ищем первую подходящую группу
		var selectedKey string
		var selectedGroup ProxyGroup

		// Сначала ищем не GLOBAL
		for k, g := range groups {
			if strings.EqualFold(k, "GLOBAL") {
				continue
			}
			if selectedKey == "" || k < selectedKey {
				selectedKey = k
				selectedGroup = g
			}
		}

		// Если ничего не нашли кроме GLOBAL (или GLOBAL был единственным)
		if selectedKey == "" {
			if g, ok := groups["GLOBAL"]; ok {
				selectedKey = "GLOBAL"
				selectedGroup = g
			}
		}

		if selectedKey == "" {
			return
		}

		proxyMu.Lock()
		activeGroup = selectedKey
		proxyNames = selectedGroup.All
		proxyMu.Unlock()

		mProxies.SetTitle("Прокси: " + selectedKey)
		mProxies.Enable()

		for i, item := range proxyPool {
			if i < len(selectedGroup.All) {
				label := selectedGroup.All[i]
				if selectedGroup.Now == label {
					label = "✓ " + label
				}
				item.SetTitle(label)
				item.Show()
			} else {
				item.Hide()
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
				group := activeGroup
				var name string
				if click.idx < len(proxyNames) {
					name = proxyNames[click.idx]
				}
				proxyMu.Unlock()

				if name != "" && group != "" {
					if err := api.SelectProxy(group, name); err != nil {
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
					for _, item := range proxyPool {
						item.Hide()
					}
					mProxies.SetTitle("Прокси")
					mProxies.Disable()
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
