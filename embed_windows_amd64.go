//go:build windows && amd64

package main

import _ "embed"

//go:embed assets/bin/x64/mihomo.exe
var coreBytes []byte

//go:embed assets/bin/x64/wintun.dll
var tunBytes []byte
