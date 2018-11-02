package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/bytefmt"

	"github.com/gorilla/mux"
	"github.com/openatx/u2init/flashget"
	"github.com/pkg/errors"
	"github.com/qiniu/log"
	goadb "github.com/yosemite-open/go-adb"
)

func renderHTML(w http.ResponseWriter, filename string) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

func renderJSON(w http.ResponseWriter, v interface{}, statusCode ...int) {
	data, _ := json.Marshal(v)
	if len(statusCode) == 1 {
		w.WriteHeader(statusCode[0])
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

func renderJSONSuccess(w http.ResponseWriter, v interface{}) {
	v2 := map[string]interface{}{
		"success": true,
		"data":    v,
	}
	renderJSON(w, v2)
}

type ADevice struct {
	Serial  string `json:"serial"`
	Model   string `json:"model"`
	Product string `json:"product"`
}

const (
	PACKAGE_DOWNLOAD = "downloading"
	PACKAGE_PUSHING  = "pushing"
	PACKAGE_INSTALL  = "installing"
	PACKAGE_FAILURE  = "failure"
	PACKAGE_SUCCESS  = "success"
)

type InstallInfo struct {
	Id             string               `json:"id"`
	Status         string               `json:"status"`
	Serial         string               `json:"serial"`
	DeviceFilePath string               `json:"deviceFilePath"`
	Description    string               `json:"description"`
	Downloader     *flashget.Downloader `json:"-"`
	PushBeganAt    time.Time            `json:"-"`
}

type PackageManager struct {
	downloads map[string]*InstallInfo
	dmer      *flashget.DownloadManager
	mu        sync.RWMutex
}

func newPackageManager() *PackageManager {
	return &PackageManager{
		downloads: make(map[string]*InstallInfo),
		dmer:      flashget.NewDownloadManager(),
	}
}

func (pm *PackageManager) InstallAPKFromURL(serial, url string) (info InstallInfo, err error) {
	id := UniqID()
	dl, err := pm.dmer.Retrive(url)
	if err != nil {
		return
	}
	log.Infof("Serial %s http download process %p", serial, dl)
	pm.mu.Lock()
	defer pm.mu.Unlock()
	insInfo := &InstallInfo{
		Id:         id,
		Serial:     serial,
		Status:     PACKAGE_DOWNLOAD,
		Downloader: dl,
	}
	pm.downloads[id] = insInfo
	go func() {
		dl.Wait()
		if !dl.Finished() {
			insInfo.Status = PACKAGE_FAILURE
			insInfo.Description = "http download failed: " +
				insInfo.Downloader.Status + " " + insInfo.Downloader.Description
			return
		}
		d := adb.Device(goadb.DeviceWithSerial(serial))
		f, er := os.Open(dl.Filename)
		if er != nil {
			insInfo.Status = PACKAGE_FAILURE
			insInfo.Description = "open file " + dl.Filename + " error: " + er.Error()
			return
		}
		dstFilepath := fmt.Sprintf("/sdcard/tmp/u2init-%s.apk", id)
		insInfo.Status = PACKAGE_PUSHING
		insInfo.PushBeganAt = time.Now()
		insInfo.DeviceFilePath = dstFilepath

		_, er = d.WriteToFile(dstFilepath, f, 0644)
		if er != nil {
			insInfo.Status = PACKAGE_FAILURE
			insInfo.Description = "push file to device err: " + er.Error()
			return
		}
		defer d.RunCommand("rm", dstFilepath) // clean apk

		insInfo.Status = PACKAGE_INSTALL
		output, er := d.RunTimeoutCommand(time.Minute*5, "pm", "install", "-r", "-t", dstFilepath)
		if er != nil {
			insInfo.Status = PACKAGE_FAILURE
			insInfo.Description = "pm install error: " + er.Error()
			return
		}
		output = strings.TrimSpace(output)
		insInfo.Description = output
		if strings.Contains(output, "Failure") {
			insInfo.Status = PACKAGE_FAILURE
			return
		}
		insInfo.Status = PACKAGE_SUCCESS
	}()
	return *pm.downloads[id], nil
}

func (pm *PackageManager) Get(id string) (info InstallInfo, err error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	pinfo, exists := pm.downloads[id]
	if !exists {
		err = errors.New("PackageManager can not found id: " + id)
		return
	}
	return *pinfo, nil
}

func init() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		renderHTML(w, "index.html")
	})

	http.HandleFunc("/devices", func(w http.ResponseWriter, r *http.Request) {
		infos, err := adb.ListDevices()
		if err != nil {
			renderJSON(w, map[string]interface{}{
				"success":     false,
				"description": "list device: " + err.Error(),
			}, 500)
			return
		}
		devs := make([]ADevice, 0, len(infos))
		for _, info := range infos {
			devs = append(devs, ADevice{
				Serial:  info.Serial,
				Model:   info.Model,
				Product: info.Product,
			})
		}
		renderJSONSuccess(w, devs)
	})

	router := mux.NewRouter()
	pm := newPackageManager()

	router.HandleFunc("/devices/{serial}/pkgs", func(w http.ResponseWriter, r *http.Request) {
		// check params
		serial := mux.Vars(r)["serial"]
		url := r.FormValue("url")
		if url == "" {
			renderJSON(w, map[string]interface{}{
				"success":     false,
				"description": "url is required",
			}, http.StatusBadRequest) // 400
			return
		}
		// check adb device
		d := adb.Device(goadb.DeviceWithSerial(serial))
		info, err := d.DeviceInfo()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest) // 400
			renderJSON(w, map[string]interface{}{
				"success":     false,
				"description": "device error: " + err.Error(),
			}, 500)
			return
		}

		// call download manager to download file
		insInfo, err := pm.InstallAPKFromURL(serial, url)
		if err != nil {
			renderJSON(w, map[string]interface{}{
				"success":     false,
				"description": "http download: " + err.Error(),
			}, 400)
			return
		}

		renderJSON(w, map[string]interface{}{
			"success": true,
			"data": map[string]string{
				"id":      insInfo.Id,
				"serial":  info.Serial,
				"product": info.Product,
				"model":   info.Model,
			},
		})
	}).Methods("POST")

	router.HandleFunc("/devices/{serial}/pkgs/{id}",
		func(w http.ResponseWriter, r *http.Request) {
			id := mux.Vars(r)["id"]
			insInfo, err := pm.Get(id)
			if err != nil {
				renderJSON(w, map[string]interface{}{
					"success":     false,
					"description": "pm error: " + err.Error(),
				}, 404)
				return
			}
			if insInfo.Status == PACKAGE_DOWNLOAD {
				total := bytefmt.ByteSize(uint64(insInfo.Downloader.ContentLength))
				copied := bytefmt.ByteSize(uint64(insInfo.Downloader.Written()))
				insInfo.Description = fmt.Sprintf("%s / %s", copied, total)
			}
			if insInfo.Status == PACKAGE_PUSHING {
				d := adb.Device(goadb.DeviceWithSerial(insInfo.Serial))
				fileInfo, err := d.Stat(insInfo.DeviceFilePath)
				if err == nil {
					total := bytefmt.ByteSize(uint64(insInfo.Downloader.ContentLength))
					copied := bytefmt.ByteSize(uint64(fileInfo.Size))
					speedByte := int(float64(fileInfo.Size) / time.Since(insInfo.PushBeganAt).Seconds())
					speed := bytefmt.ByteSize(uint64(speedByte))
					insInfo.Description = fmt.Sprintf("%s / %s  speed: %s/s", copied, total, speed)
				}
			}
			renderJSON(w, map[string]interface{}{
				"success": true,
				"data":    insInfo,
			})
		}).Methods("GET")

	http.Handle("/devices/", router)
}
