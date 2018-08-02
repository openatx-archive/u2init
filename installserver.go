package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	goadb "github.com/yosemite-open/go-adb"
)

// The json tag is to sync with REST API https://github.com/openatx/atx-agent
type SyncState struct {
	ID          string `json:"id"`
	Copied      int    `json:"copiedSize"`
	Total       int    `json:"totalSize"`
	State       string `json:"message"`
	asnycCopier *goadb.AsyncWriter
}

func (s *SyncState) Update() error {
	if s.asnycCopier == nil {
		return errors.New("asnycCopier is nil")
	}
	if s.Total == 0 {
		s.Total = int(s.asnycCopier.TotalSize)
		s.Copied = int(s.asnycCopier.BytesCompleted())
	} else if s.Copied != s.Total {
		s.Copied = int(s.asnycCopier.BytesCompleted())
	}
	return nil
}

var _idLocker sync.Mutex
var _id int

func UniqID() string {
	_idLocker.Lock()
	defer _idLocker.Unlock()
	_id++
	return fmt.Sprintf("%d", _id)
}

type Dashboard struct {
	m      sync.Mutex
	states map[string]*SyncState
}

func NewDashboard() *Dashboard {
	return &Dashboard{
		states: make(map[string]*SyncState),
	}
}
func (d *Dashboard) AddSyncState() (id string, state *SyncState) {
	d.m.Lock()
	defer d.m.Unlock()
	id = UniqID()
	state = &SyncState{ID: id}
	d.states[id] = state
	return
}

// If not found, return nil
func (d *Dashboard) Get(id string) *SyncState {
	d.m.Lock()
	defer d.m.Unlock()
	return d.states[id]
}

func (d *Dashboard) DeleteAfter(id string, duration time.Duration) {
	go func() {
		time.Sleep(duration)
		d.m.Lock()
		defer d.m.Unlock()
		delete(d.states, id)
	}()
}

func registerHTTPHandler() {
	m := mux.NewRouter()
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "Hello world!")
	})

	dashboard := NewDashboard()

	adb, err := goadb.New()
	if err != nil {
		panic(err)
	}

	m.HandleFunc("/install/{serial}", func(w http.ResponseWriter, r *http.Request) {
		serial := mux.Vars(r)["serial"]
		device := adb.Device(goadb.DeviceWithSerial(serial))
		url := r.FormValue("url")
		if url == "" {
			http.Error(w, "form value \"url\" is required", http.StatusBadRequest)
			return
		}
		id, state := dashboard.AddSyncState()
		tmpPath := fmt.Sprintf("/sdcard/tmp-%s.apk", id)
		aw, err := device.DoSyncHTTPFile(tmpPath, url, 0644)
		if err != nil {
			http.Error(w, err.Error(), 500)
			dashboard.DeleteAfter(id, 1*time.Second)
			return
		}
		state.State = "pushing"
		state.asnycCopier = aw
		io.WriteString(w, id)

		go func() {
			defer device.RunCommand("rm", tmpPath)
			defer dashboard.DeleteAfter(id, 5*time.Minute)

			<-aw.Done
			err := aw.Err()
			if err != nil {
				state.State = "err: " + err.Error()
				return
			}

			state.State = "installing"
			// do install
			output, err := device.RunCommand("pm", "install", "-r", "-t", tmpPath)
			if err != nil {
				state.State = "err: " + err.Error() + ":" + output
				return
			}
			if strings.Contains(output, "Success") {
				state.State = "finished"
			} else {
				state.State = "err: " + strings.TrimSpace(output)
			}
		}()
	}).Methods("POST")

	m.HandleFunc("/install/{serial}/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/install/"+mux.Vars(r)["id"], 302)
	})

	m.HandleFunc("/install/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		state := dashboard.Get(id)
		if state == nil {
			state = &SyncState{
				State: "finished",
			}
		}
		state.Update()
		data, _ := json.Marshal(state)
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}).Methods("GET")

	m.HandleFunc("/install/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		state := dashboard.Get(id)
		if state == nil {
			io.WriteString(w, "already deleted")
			return
		}
		state.asnycCopier.Cancel()
		io.WriteString(w, "canceled")
	}).Methods("DELETE")

	http.Handle("/", m)
}
