package main

import "testing"

func TestDeviceManager(t *testing.T) {
	dm.Remove("aabbcc") // shold not panic
}
