/*
Heartbeat client use HTTP POST to communicate server

POST $SERVER_URL

Params

id:   clientId
port: clientPort
data: json format

Response 200 means success

- First CONNECTION
Will send all data

- The following CONNECTION
*/
package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/DeanThompson/syncmap"
	"github.com/codeskyblue/goreq"
	"github.com/codeskyblue/muuid"
	"github.com/pkg/errors"
	"github.com/qiniu/log"
)

type HeartbeatClient struct {
	ID      string
	Port    int
	URI     string
	storage *syncmap.SyncMap
}

func NewHeartbeatClient(uri string, selfListenPort int) *HeartbeatClient {
	return &HeartbeatClient{
		ID:      machineID(),
		URI:     uri,
		Port:    selfListenPort,
		storage: syncmap.New(),
	}
}

func (h *HeartbeatClient) formData() url.Values {
	values := url.Values{}
	values.Add("id", h.ID)
	values.Add("port", strconv.Itoa(h.Port))
	return values
}

func (h *HeartbeatClient) sendData(data interface{}) error {
	v := h.formData()
	if data != nil {
		jdata, err := json.Marshal(data)
		if err != nil {
			return err
		}
		v.Add("data", string(jdata))
	}
	// log.Println("POST", h.Uri, v.Encode())
	res, err := goreq.Request{
		Method:  "POST",
		Uri:     h.URI,
		Body:    v,
		Timeout: 2 * time.Second,
	}.Do()
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 200 {
		return nil
	}
	desc, _ := res.Body.ToString()
	return errors.New("heartbeat err: " + desc)
}

func (h *HeartbeatClient) Ping() error {
	return h.sendData(nil)
}

func (h *HeartbeatClient) PingForever() {
	failed := false
	for {
		time.Sleep(5 * time.Second)
		if err := h.Ping(); err != nil {
			log.Println("Ping", "err:", err)
			failed = true
			continue
		}

		if failed {
			failed = false
			// backalive
			log.Println("Server backalive, resend data")
			h.storage.EachItem(func(item *syncmap.Item) {
				h.sendData(item.Value)
			})
		}
	}
}

func (h *HeartbeatClient) AddData(key string, data interface{}) {
	h.storage.Set(key, data)
	h.sendData(data)
}

func (h *HeartbeatClient) Delete(key string) {
	value, ok := h.storage.Get(key)
	if !ok {
		return
	}
	m := value.(map[string]interface{})
	m["status"] = "offline"
	h.sendData(m)
	h.storage.Delete(key)
}

func machineID() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return muuid.UUID()
	}
	for _, i := range interfaces {
		if i.Flags&net.FlagUp != 0 && bytes.Compare(i.HardwareAddr, nil) != 0 {
			addr := i.HardwareAddr.String()
			if i.Name == "eth0" {
				return addr
			}
		}
	}
	return muuid.UUID()
}
