package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
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
