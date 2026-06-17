package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDeepLink(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		hasError bool
	}{
		{
			input:    "tray-clash://install-config?url=https%3A%2F%2Fexample.com%2Fconfig.yaml",
			expected: "https://example.com/config.yaml",
			hasError: false,
		},
		{
			input:    "tray-clash:install-config?url=https%3A%2F%2Fexample.com%2Fconfig.yaml",
			expected: "https://example.com/config.yaml",
			hasError: false,
		},
		{
			input:    "tray-clash://install-config?url=https://example.com/config.yaml?foo=bar%26baz=1",
			expected: "https://example.com/config.yaml?foo=bar&baz=1",
			hasError: false,
		},
		{
			input:    "invalid-scheme://install-config?url=https%3A%2F%2Fexample.com",
			expected: "",
			hasError: true,
		},
		{
			input:    "tray-clash://invalid-command?url=https%3A%2F%2Fexample.com",
			expected: "",
			hasError: true,
		},
		{
			input:    "tray-clash://install-config",
			expected: "",
			hasError: true,
		},
	}

	for _, test := range tests {
		res, err := parseDeepLink(test.input)
		if test.hasError {
			if err == nil {
				t.Errorf("expected error for input %q, got nil", test.input)
			}
		} else {
			if err != nil {
				t.Errorf("unexpected error for input %q: %v", test.input, err)
			}
			if res != test.expected {
				t.Errorf("expected %q, got %q for input %q", test.expected, res, test.input)
			}
		}
	}
}

func TestGetOrderedSelectGroupsFromConfig(t *testing.T) {
	// Backup original config.yaml if it exists
	configPath := filepath.Join(exeDir(), "config.yaml")
	var backup []byte
	var backupErr error
	if _, err := os.Stat(configPath); err == nil {
		backup, backupErr = os.ReadFile(configPath)
	}

	// Write test config.yaml
	testConfig := `
proxy-groups:
  - name: PROXY
    type: select
    hidden: true
    proxies:
      - Group A
  - name: Group A
    type: select
    proxies:
      - node1
  - name: Group B
    type: url-test
    proxies:
      - node2
  - name: Group C
    type: select
    hidden: false
    proxies:
      - node3
  - name: Group D
    type: select
    hidden: true
`
	err := os.WriteFile(configPath, []byte(testConfig), 0644)
	if err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	defer func() {
		// Restore original config.yaml
		if backupErr == nil && backup != nil {
			os.WriteFile(configPath, backup, 0644)
		} else {
			os.Remove(configPath)
		}
	}()

	expected := []string{"Group A", "Group C"}
	res := GetOrderedSelectGroupsFromConfig()

	if len(res) != len(expected) {
		t.Fatalf("expected length %d, got %d. Result: %v", len(expected), len(res), res)
	}

	for i, name := range expected {
		if res[i] != name {
			t.Errorf("expected res[%d] = %q, got %q", i, name, res[i])
		}
	}
}

