package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows/registry"
)

var deepLinkCh = make(chan string, 10)

var (
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
)

const ERROR_ALREADY_EXISTS = 183

func checkSingleInstance() (syscall.Handle, bool) {
	name, err := syscall.UTF16PtrFromString("Local\\TrayClashSingleInstanceMutex")
	if err != nil {
		return 0, false
	}

	// CreateMutexW(nil, true, name)
	ret, _, err := procCreateMutexW.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if ret == 0 {
		return 0, false
	}

	if err != nil && err.(syscall.Errno) == ERROR_ALREADY_EXISTS {
		syscall.CloseHandle(syscall.Handle(ret))
		return 0, false
	}

	return syscall.Handle(ret), true
}

func registerProtocol() {
	exe, err := os.Executable()
	if err != nil {
		return
	}

	// Open or create HKCU\Software\Classes\tray-clash using Go's registry package with registry.WRITE access
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\tray-clash`, registry.WRITE)
	if err != nil {
		return
	}
	defer k.Close()

	k.SetStringValue("", "URL:TrayClash Protocol")
	k.SetStringValue("URL Protocol", "")

	// Create shell\open\command
	cmdKey, _, err := registry.CreateKey(k, `shell\open\command`, registry.WRITE)
	if err != nil {
		return
	}
	defer cmdKey.Close()

	cmdVal := fmt.Sprintf("\"%s\" \"%%1\"", exe)
	cmdKey.SetStringValue("", cmdVal)
}

func main() {
	// 1. Register protocol handler
	registerProtocol()

	// 2. Parse command-line args for deep links
	var initialDeepLink string
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "tray-clash:") {
			initialDeepLink = arg
			break
		}
	}

	// 3. Single-instance check via Mutex
	mutexHandle, isPrimary := checkSingleInstance()
	if !isPrimary {
		// Another instance is already running!
		if initialDeepLink != "" {
			// Connect to the primary instance and send the deep link URL
			conn, err := net.Dial("tcp", "127.0.0.1:39281")
			if err == nil {
				fmt.Fprintln(conn, initialDeepLink)
				conn.Close()
			}
		}
		// Exit this secondary instance
		return
	}
	defer syscall.CloseHandle(mutexHandle)

	// 4. Start IPC server to listen for deep links from other instances
	// If net.Listen fails (e.g. firewall blocked on Windows 7), we don't crash or exit.
	listener, err := net.Listen("tcp", "127.0.0.1:39281")
	if err == nil {
		go func() {
			defer listener.Close()
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					scanner := bufio.NewScanner(c)
					if scanner.Scan() {
						link := strings.TrimSpace(scanner.Text())
						if link != "" {
							deepLinkCh <- link
						}
					}
				}(conn)
			}
		}()
	}

	// 5. If we have an initial deep link, send it to the channel
	if initialDeepLink != "" {
		deepLinkCh <- initialDeepLink
	}

	systray.Run(onReady, onExit)
}