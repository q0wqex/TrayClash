package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/getlantern/systray"
)

var deepLinkCh = make(chan string, 10)

func registerProtocol() {
	exe, err := os.Executable()
	if err != nil {
		return
	}

	// Register protocol handler under HKEY_CURRENT_USER\Software\Classes
	runHidden("reg", "add", "HKCU\\Software\\Classes\\tray-clash", "/ve", "/t", "REG_SZ", "/d", "URL:TrayClash Protocol", "/f").Run()
	runHidden("reg", "add", "HKCU\\Software\\Classes\\tray-clash", "/v", "URL Protocol", "/t", "REG_SZ", "/d", "", "/f").Run()

	cmdVal := fmt.Sprintf("\"%s\" \"%%1\"", exe)
	runHidden("reg", "add", "HKCU\\Software\\Classes\\tray-clash\\shell\\open\\command", "/ve", "/t", "REG_SZ", "/d", cmdVal, "/f").Run()
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

	// 3. Single-instance lock via local TCP port
	listener, err := net.Listen("tcp", "127.0.0.1:39281")
	if err != nil {
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
	defer listener.Close()

	// 4. Start IPC server to listen for deep links from other instances
	go func() {
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

	// 5. If we have an initial deep link, send it to the channel
	if initialDeepLink != "" {
		deepLinkCh <- initialDeepLink
	}

	systray.Run(onReady, onExit)
}