package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	goadb "github.com/yosemite-open/go-adb"
)

// FormatString replace ${KEY} to value
func FormatString(s string, values map[string]string) string {
	for k, v := range values {
		s = strings.Replace(s, "${"+k+"}", v, -1)
	}
	return s
}

var _idLocker sync.Mutex
var _id int

func UniqID() string {
	_idLocker.Lock()
	defer _idLocker.Unlock()
	_id++
	return fmt.Sprintf("%d", _id)
}

func HashStr(str string) string {
	h := md5.New()
	h.Write([]byte(str))
	return hex.EncodeToString(h.Sum(nil))
}

// write with retry
func writeFileToDevice(device *goadb.Device, src, dst string, mode os.FileMode) error {
	for i := 0; i < 3; i++ {
		if err := unsafeWriteFileToDevice(device, src, dst, mode); err == nil {
			return nil
		}
		if i != 2 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return fmt.Errorf("copy file to device failed: %s -> %s", src, dst)
}

func unsafeWriteFileToDevice(device *goadb.Device, src, dst string, mode os.FileMode) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	dstTemp := dst + ".tmp-magic1231x"
	_, err = device.WriteToFile(dstTemp, f, mode)
	if err != nil {
		device.RunCommand("rm", dstTemp)
		return err
	}
	// use mv to prevent "text busy" error
	_, err = device.RunCommand("mv", dstTemp, dst)
	return err
}
