package main

import (
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
