//go:build windows && 386

package main

import _ "embed"

//go:embed assets/bin/x86/mihomo.exe
var coreBytes []byte

//go:embed assets/bin/x86/wintun.dll
var tunBytes []byte
