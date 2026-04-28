package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// ProcessManager управляет ядром mihomo как фоновым процессом
type ProcessManager struct {
	ExePath string
	cmd     *exec.Cmd
	job     syscall.Handle
}

// NewProcessManager создает новый менеджер процессов
func NewProcessManager() *ProcessManager {
	pm := &ProcessManager{
		ExePath: filepath.Join(exeDir(), "mihomo.exe"),
	}
	pm.setupJobObject()
	return pm
}

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
)

// setupJobObject создает объект задания, чтобы при закрытии лаунчера ядро тоже закрывалось
func (pm *ProcessManager) setupJobObject() {
	// CreateJobObjectW(nil, nil)
	r1, _, _ := procCreateJobObjectW.Call(0, 0)
	if r1 == 0 {
		return
	}
	pm.job = syscall.Handle(r1)
	
	info := struct {
		BasicLimitInformation struct {
			LimitFlags uint32
		}
	}{}
	// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x2000
	info.BasicLimitInformation.LimitFlags = 0x2000
	
	_, _, _ = procSetInformationJobObject.Call(
		uintptr(pm.job),
		9, // JobObjectExtendedLimitInformation
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
	)
}

// Start запускает ядро
func (pm *ProcessManager) Start() error {
	if pm.IsRunning() {
		return nil
	}

	pm.cmd = exec.Command(pm.ExePath, "-d", exeDir())
	pm.cmd.Dir = exeDir()
	pm.cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x01000000, // CREATE_BREAKAWAY_FROM_JOB = 0x01000000
	}

	if err := pm.cmd.Start(); err != nil {
		return err
	}

	if pm.job != 0 {
		// Открываем хендл процесса с нужными правами
		h, err := syscall.OpenProcess(0x0001|0x0400|0x0010|0x0020, false, uint32(pm.cmd.Process.Pid))
		if err == nil {
			_, _, _ = procAssignProcessToJobObject.Call(uintptr(pm.job), uintptr(h))
			syscall.CloseHandle(h)
		}
	}

	return nil
}

// runHidden выполняет команду без всплывающего окна консоли
func runHidden(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// Stop останавливает ядро
func (pm *ProcessManager) Stop() error {
	// 1. Пытаемся убить по PID, если он у нас есть
	if pm.cmd != nil && pm.cmd.Process != nil {
		pid := pm.cmd.Process.Pid
		runHidden("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid)).Run()
		pm.cmd.Process.Kill()
		pm.cmd = nil
	}
	
	// 2. Гарантированно добиваем по имени
	runHidden("taskkill", "/F", "/IM", "mihomo.exe").Run()
	
	return nil
}

// IsRunning проверяет, запущено ли ядро (неблокирующе)
func (pm *ProcessManager) IsRunning() bool {
	if pm.cmd != nil && pm.cmd.Process != nil {
		err := pm.cmd.Process.Signal(syscall.Signal(0))
		if err == nil {
			return true
		}
		pm.cmd = nil
	}
	
	// Скрытый запуск tasklist, чтобы не моргало окно
	out, _ := runHidden("tasklist", "/FI", "IMAGENAME eq mihomo.exe", "/NH").Output()
	return strings.Contains(string(out), "mihomo.exe")
}

// Status — для совместимости с интерфейсом tray.go
func (pm *ProcessManager) Status() (string, error) {
	if pm.IsRunning() {
		return "started", nil
	}
	return "stopped", nil
}

// Install/Uninstall — теперь управляют автозагрузкой через планировщик задач
// Это необходимо для обхода UAC и работы приложений с правами администратора
func (pm *ProcessManager) Install() error {
	exe, _ := os.Executable()
	
	// 1. Удаляем старый ключ реестра, если он был (миграция)
	runHidden("reg", "delete", "HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run", "/v", "TrayClash", "/f").Run()
	
	// 2. Создаем задачу в планировщике:
	// /tn "TrayClash" - имя задачи
	// /tr - путь к исполняемому файлу (в кавычках)
	// /sc onlogon - запуск при входе в систему
	// /rl highest - запуск с наивысшими правами (админ)
	// /f - принудительная перезапись если уже есть
	trValue := fmt.Sprintf("\"%s\" --autostart", exe)
	return runHidden("schtasks", "/create", "/tn", "TrayClash", "/tr", trValue, "/sc", "onlogon", "/rl", "highest", "/f").Run()
}

func (pm *ProcessManager) Uninstall() error {
	// 1. Удаляем ключ реестра
	runHidden("reg", "delete", "HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run", "/v", "TrayClash", "/f").Run()
	
	// 2. Удаляем задачу из планировщика
	return runHidden("schtasks", "/delete", "/tn", "TrayClash", "/f").Run()
}
