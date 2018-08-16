package main

import "strings"

// FormatString replace ${KEY} to value
func FormatString(s string, values map[string]string) string {
	for k, v := range values {
		s = strings.Replace(s, "${"+k+"}", v, -1)
	}
	return s
}
