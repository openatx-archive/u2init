package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver"
	"github.com/alecthomas/kingpin"
	"github.com/cavaliercoder/grab"
	"github.com/franela/goreq"
	"github.com/mholt/archiver"
	"github.com/phayes/freeport"
	"github.com/pkg/errors"
	"github.com/qiniu/log"
	"github.com/shogo82148/androidbinary/apk"
	goadb "github.com/yosemite-open/go-adb"
)

var adb *goadb.Adb
var resourcesDir string
var stfBinariesDir string
var apkVersionConstraint *semver.Constraints
var versions Versions // contains apk version and atx-agent version

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile | log.Llevel)
	log.SetOutputLevel(log.Ldebug)

	var err error
	adb, err = goadb.New()
	if err != nil {
		log.Fatal(err)
	}

}

// Nubia can only work in /data/data/com.android.shell
// Others works in both directory
const (
	PATHENV       = "PATH=$PATH:/data/local/tmp:/data/data/com.android.shell"
	GITHUB_MIRROR = "https://github-mirror.open.netease.com"
)

var (
	AGENT_VERSION  = "0.4.8"
	RECORD_VERSION = "1.3"
	APK_VERSION    = "1.1.5"
	SKIP_DEV       = false
)

type ATXKeeper struct {
	ServerAddr    string
	SkipDev       bool
	APKVersion    string
	AgentVersion  string
	RecordVersion string

	device *goadb.Device
}

// processAgent install /data/local/tmp/atx-agent
func (k *ATXKeeper) processAgent() error {
	if !k.shouldUpdateAgent() {
		log.Infof("SKIP, atx-agent is ok, version %s", k.AgentVersion)
		return nil
	}

	agentReleaseURL := FormatString(GITHUB_MIRROR+"/openatx/atx-agent/releases/download/${AGENT_VERSION}/atx-agent_${AGENT_VERSION}_linux_armv6.tar.gz", map[string]string{
		"AGENT_VERSION": AGENT_VERSION,
	})
	log.Infof("latest agent version %s", k.AgentVersion)
	log.Println("download from", agentReleaseURL)
	dstPath := resourcesDir + fmt.Sprintf("/atx-agent-%s.tar.gz", AGENT_VERSION)
	cached, err := httpDownload(dstPath, agentReleaseURL)
	if err != nil {
		return err
	}
	if cached {
		log.Info("Use cached resource")
	}
	err = archiver.TarGz.Open(dstPath, resourcesDir+"/atx-agent-armv6")
	if err != nil {
		return errors.Wrap(err, "open targz")
	}

	atxAgentPath := filepath.Join(resourcesDir, "atx-agent-armv6/atx-agent")
	if err := writeFileToDevice(k.device, atxAgentPath, "/data/local/tmp/atx-agent", 0755); err != nil {
		return errors.Wrap(err, "atx-agent")
	}
	output, _ := k.device.RunCommand(PATHENV, "atx-agent", "version")
	log.Infof("new atx-agent version %s", output)
	k.device.RunCommand(PATHENV, "atx-agent", "server", "--stop")
	args := []string{"atx-agent", "server", "-d", "--nouia"}
	if k.ServerAddr != "" {
		args = append(args, "-t", k.ServerAddr)
	}
	output, err = k.device.RunCommand(PATHENV, args...)
	output = strings.TrimSpace(output)
	if err != nil {
		return errors.Wrap(err, "start atx-agent")
	}
	serial, _ := k.device.Serial()
	fmt.Println(serial, output)
	return nil
}

func (k *ATXKeeper) shouldUpdateAgent() bool {
	forwardedPort, err := k.device.ForwardToFreePort(goadb.ForwardSpec{
		Protocol:   "tcp",
		PortOrName: "7912",
	})
	if err != nil {
		return true
	}
	var v struct {
		Udid      string `json:"udid"`
		ServerURL string `json:"serverURL"`
	}
	res, err := retryGet(fmt.Sprintf("http://127.0.0.1:%d/info", forwardedPort))
	if err != nil {
		log.Infof("atx-agent /info not responding")
		return true
	}
	defer res.Body.Close()
	if err = res.Body.FromJsonTo(&v); err != nil {
		return true
	}
	if v.ServerURL != "http://"+k.ServerAddr {
		log.Infof("atx-agent server addr changed, '%s' -> 'http://%s'", v.ServerURL, k.ServerAddr)
		return true
	}
	// check version
	curVersion, _ := k.device.RunCommand(PATHENV, "atx-agent", "version")
	curVersion = strings.TrimSpace(curVersion)
	if curVersion == "dev" && k.SkipDev {
		log.Infof("SKIP, atx-agent version %s, skip update", strconv.Quote(curVersion))
		return false
	}
	if curVersion != k.AgentVersion {
		log.Infof("atx-agent version outdated, %s -> %s", curVersion, k.AgentVersion)
		return true
	}
	return false
}

// 2 apks
func (k *ATXKeeper) processUiautomator() error {
	if !k.shouldUpdateUiautomator() {
		log.Infof("SKIP, uiautomator-[test].apk %s already installed", k.APKVersion)
		return nil
	}

	apkReleaseBaseURL := FormatString(GITHUB_MIRROR+"/openatx/android-uiautomator-server/releases/download/${APK_VERSION}/", map[string]string{
		"APK_VERSION": k.APKVersion,
	})
	suffix := fmt.Sprintf("-%s.apk", k.APKVersion)
	apk1Dest := resourcesDir + "/app-uiautomator" + suffix
	apk2Dest := resourcesDir + "/app-uiautomator-test" + suffix
	var err error
	_, err = httpDownload(apk1Dest, apkReleaseBaseURL+"app-uiautomator.apk")
	if err != nil {
		return err
	}
	_, err = httpDownload(apk2Dest, apkReleaseBaseURL+"app-uiautomator-test.apk")
	if err != nil {
		return err
	}
	log.Infof("install app-uiautomator.apk version %s", k.APKVersion)
	err = k.forceInstallAPK(apk1Dest)
	if err != nil {

		return err
	}
	log.Infof("install app-uiautomator-test.apk version %s", k.APKVersion)
	err = k.forceInstallAPK(apk2Dest)
	if err != nil {
		return err
	}
	return nil
}

func (k *ATXKeeper) processRecordAPK() error {
	if !k.shouldUpdateRecordAPK() {
		log.Infof("SKIP, record apk %s already installed", k.RecordVersion)
		return nil
	}
	log.Infof("install record apk, version %s", k.RecordVersion)
	recordReleaseURL := GITHUB_MIRROR + "/openatx/android-uiautomator-server/releases/download/1.1.5/com.easetest.recorder_" + k.RecordVersion + ".apk"
	recordDst := resourcesDir + FormatString("/com.easetest.recorder-${RECORD_VERSION}.apk", map[string]string{
		"RECORD_VERSION": k.RecordVersion,
	})
	_, err := httpDownload(recordDst, recordReleaseURL)
	if err != nil {
		return err
	}
	return k.forceInstallAPK(recordDst)
}

func (k *ATXKeeper) shouldUpdateRecordAPK() bool {
	info, err := k.device.StatPackage("com.easetest.recorder")
	if err != nil {
		log.Debugf("package com.easetest.recorder not installed")
		return true
	}
	if info.Version.Name != k.RecordVersion {
		log.Infof("expect record apk version %s, but got %s",
			strconv.Quote(k.RecordVersion), strconv.Quote(info.Version.Name))
		return true
	}
	return false
}

func (k *ATXKeeper) shouldUpdateUiautomator() bool {
	info, err := k.device.StatPackage("com.github.uiautomator")
	if err != nil {
		log.Debugf("package com.github.uiautomator not installed")
		return true
	}
	if info.Version.Name != k.APKVersion {
		log.Infof("expect uiautomator apk version %s, but got %s",
			strconv.Quote(k.APKVersion), strconv.Quote(info.Version.Name))
		return true
	}

	// test package
	_, err = k.device.StatPackage("com.github.uiautomator.test")
	if err != nil {
		log.Infof("package com.github.uiautomator.test not installed")
		return true
	}
	return false
}

func (k *ATXKeeper) forceInstallAPK(localPath string) error {
	pkg, err := apk.OpenFile(localPath)
	if err != nil {
		return err
	}
	packageName := pkg.PackageName()
	k.device.RunCommand("pm", "uninstall", packageName)
	return k.installAPK(localPath)
}

func (k *ATXKeeper) installAPK(localPath string) error {
	dstPath := "/sdcard/tmp/" + filepath.Base(localPath)
	if err := writeFileToDevice(k.device, localPath, dstPath, 0644); err != nil {
		return err
	}
	defer k.device.RunCommand("rm", dstPath)
	output, err := k.device.RunCommand("pm", "install", "-r", "-t", dstPath)
	if err != nil {
		return err
	}
	if !strings.Contains(output, "Success") {
		return errors.Wrap(errors.New(output), "apk-install")
	}
	return nil
}

func initEverything(device *goadb.Device, serverAddr string) error {
	props, err := device.Properties()
	if err != nil {
		return err
	}
	sdk := props["ro.build.version.sdk"]
	abi := props["ro.product.cpu.abi"]
	pre := props["ro.build.version.preview_sdk"]
	log.Printf("product model: %s\n", props["ro.product.model"])

	if pre != "" && pre != "0" {
		sdk += pre
	}
	log.Println("Process minicap and minitouch")
	if err := initSTFMiniTools(device, abi, sdk); err != nil {
		return errors.Wrap(err, "mini(cap|touch)")
	}

	log.Println("Process atx-agent")
	ak := &ATXKeeper{
		AgentVersion:  AGENT_VERSION,
		RecordVersion: RECORD_VERSION,
		APKVersion:    APK_VERSION,
		ServerAddr:    serverAddr,
		SkipDev:       SKIP_DEV,
		device:        device,
	}
	if err := ak.processAgent(); err != nil {
		log.Warnf("install atx-agent failed: %v", err)
		return err
	}
	if err := ak.processUiautomator(); err != nil {
		log.Warnf("install uiautomator failed")
		return err
	}
	if err := ak.processRecordAPK(); err != nil {
		log.Warnf("install record apk failed")
		return err
	}
	return nil
}

func initMiniTouch(device *goadb.Device, abi string) error {
	srcPath := fmt.Sprintf(stfBinariesDir+"/minitouch-prebuilt/prebuilt/%s/bin/minitouch", abi)
	return writeFileToDevice(device, srcPath, "/data/local/tmp/minitouch", 0755)
}

func initSTFMiniTools(device *goadb.Device, abi, sdk string) error {
	soSrcPath := fmt.Sprintf(stfBinariesDir+"/minicap-prebuilt/prebuilt/%s/lib/android-%s/minicap.so", abi, sdk)
	err := writeFileToDevice(device, soSrcPath, "/data/local/tmp/minicap.so", 0644)
	if err != nil {
		return err
	}
	binSrcPath := fmt.Sprintf(stfBinariesDir+"/minicap-prebuilt/prebuilt/%s/bin/minicap", abi)
	err = writeFileToDevice(device, binSrcPath, "/data/local/tmp/minicap", 0755)
	if err != nil {
		return err
	}
	touchSrcPath := fmt.Sprintf(stfBinariesDir+"/minitouch-prebuilt/prebuilt/%s/bin/minitouch", abi)
	return writeFileToDevice(device, touchSrcPath, "/data/local/tmp/minitouch", 0755)
}

func startService(device *goadb.Device) (err error) {
	_, err = device.RunCommand("am", "startservice", "-n", "com.github.uiautomator/.Service")
	return err
}

func retryGet(url string) (res *goreq.Response, err error) {
	for i := 0; i < 3; i++ {
		res, err = goreq.Request{
			Method: "GET",
			Uri:    url,
		}.Do()
		if err == nil {
			return
		}
		if i != 2 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return nil, errors.New("unable get url: " + url)
}

func deviceUdid(device *goadb.Device) (udid string, port int, err error) {
	forwardedPort, err := device.ForwardToFreePort(goadb.ForwardSpec{
		Protocol:   "tcp",
		PortOrName: "7912",
	})
	if err != nil {
		return
	}
	var v struct {
		Udid string `json:"udid"`
	}
	res, err := retryGet(fmt.Sprintf("http://127.0.0.1:%d/info", forwardedPort))
	if err != nil {
		return
	}
	defer res.Body.Close()
	if err = res.Body.FromJsonTo(&v); err != nil {
		return
	}
	return v.Udid, forwardedPort, nil
}

type DeviceManager struct {
	mu      sync.Mutex
	devices map[string]ADevice
}

func (dm *DeviceManager) Get(serial string) (d ADevice, exists bool) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	d, exists = dm.devices[serial]
	return
}
func (dm *DeviceManager) Add(info ADevice) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	if dm.devices == nil {
		dm.devices = make(map[string]ADevice)
	}
	dm.devices[info.Serial] = info
}

func (dm *DeviceManager) Remove(serial string) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	delete(dm.devices, serial)
}

func (dm *DeviceManager) All() []ADevice {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	devs := make([]ADevice, 0, len(dm.devices))
	for _, v := range dm.devices {
		devs = append(devs, v)
	}
	return devs
}

var dm = &DeviceManager{}

func watchAndInit(serverAddr string, heart *HeartbeatClient) {
	watcher := adb.NewDeviceWatcher()
	for event := range watcher.C() {
		if event.CameOnline() {
			log.Printf("Device %s came online", event.Serial)
			device := adb.Device(goadb.DeviceWithSerial(event.Serial))
			log.Println(event.Serial, "Init device")
			if err := initEverything(device, serverAddr); err != nil {
				log.Printf("Init error: %v", errors.Wrap(err, event.Serial))
				continue
			}
			startService(device)
			// start identify
			// device.RunCommand("am", "start", "-n", "com.github.uiautomator/.IdentifyActivity",
			// 	"-e", "theme", "black")

			udid, forwardedPort, err := deviceUdid(device)
			if err != nil {
				log.Println(event.Serial, err)
				continue
			}
			devInfo, err := device.DeviceInfo()
			if err != nil {
				log.Println(event.Serial, err)
				continue
			}

			log.Println(event.Serial, "UDID", udid)
			log.Println(event.Serial, "7912 forward to", forwardedPort)
			if heart != nil {
				// device manager
				dm.Add(ADevice{
					Serial:    event.Serial,
					Model:     devInfo.Model,
					Product:   devInfo.Product,
					Udid:      udid,
					AgentPort: forwardedPort,
				})
				heart.AddData(event.Serial, map[string]interface{}{
					"udid":                  udid,
					"status":                "online",
					"providerForwardedPort": forwardedPort,
				})
			}
			log.Println("Success init", strconv.Quote(event.Serial))
		}
		if event.WentOffline() {
			log.Printf("Device %s went offline", event.Serial)
			dm.Remove(event.Serial)
			if heart != nil {
				heart.Delete(event.Serial)
			}
		}
	}
	if watcher.Err() != nil {
		log.Fatal(watcher.Err())
	}
}

// Documents: https://testerhome.com/topics/8121
func generateInitd(serverAddr string) {
	if serverAddr == "" {
		log.Fatal("-server is required")
	}
	pattern := `#!/bin/sh
### BEGIN INIT INFO
# Provides:        ${NAME}
# Required-Start:  $network
# Required-Stop:   $network
# Default-Start:   2 3 4 5
# Default-Stop:    0 1 6
# Short-Description: ATX U2init (Provider)
### END INIT INFO

PATH=/bin:/usr/bin:/usr/local/bin
PROGRAM=${PROGRAM}
ARGS="-server ${SERVER}"

case "$1" in
	start)
		echo "start ${NAME}"
		$PROGRAM $ARGS >> /var/log/${NAME}.log 2>&1 &
		;;
	stop)
		echo "stop ${NAME}"
		killall ${NAME}
		;;
	*)
		echo "Usage: service ${NAME} <start|stop>"
		exit 1
		;;
esac
# run the following command to enable ato start
# update-rc.d ${NAME} defaults
`
	program, _ := os.Executable()
	pattern = strings.Replace(pattern, "${NAME}", filepath.Base(program), -1)
	pattern = strings.Replace(pattern, "${PROGRAM}", program, -1)
	pattern = strings.Replace(pattern, "${SERVER}", serverAddr, -1)
	fmt.Print(pattern)
}

type Versions struct {
	AgentVersion  string `json:"atx-agent"`
	ApkVersion    string `json:"uiautomator-apk"`
	RecordVersion string `json:"recorder-apk"`
}

func httpDownload(dst string, url string) (cached bool, err error) {
	if _, err := os.Stat(dst); err == nil {
		return true, nil
	}
	resp, err := grab.Get(dst+".cached", url)
	if err != nil {
		return false, err
	}
	log.Info("Download saved to", resp.Filename)
	return false, os.Rename(dst+".cached", dst)
}
func main() {
	fport := kingpin.Flag("port", "listen port, random free port if not specified").Short('p').Int()
	fServerAddr := kingpin.Flag("server", "atx-server address, format must be ip:port or hostname").Short('s').Required().String()
	fInitd := kingpin.Flag("initd", "Generate /etc/init.d file (Debian only)").Bool()

	execDir, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	kingpin.Flag("resdir", "directory contains minicap, apk etc resources").
		Default(filepath.Join(filepath.Dir(execDir), "resources")).StringVar(&resourcesDir)

	kingpin.CommandLine.HelpFlag.Short('h')
	kingpin.Parse()

	stfBinariesDir = filepath.Join(resourcesDir, "stf-binaries-0.2/node_modules")

	if *fInitd {
		generateInitd(*fServerAddr)
		return
	}

	fmt.Println("u2init version 20180330")
	log.Println("Add adb.exe to PATH +=", resourcesDir)
	newPath := fmt.Sprintf("%s%s%s", os.Getenv("PATH"), string(os.PathListSeparator), resourcesDir)
	os.Setenv("PATH", newPath)

	var heart *HeartbeatClient

	registerHTTPHandler()
	port := *fport
	if port == 0 {
		var err error
		port, err = freeport.GetFreePort()
		if err != nil {
			log.Fatal(err)
		}
	}
	heart = NewHeartbeatClient("http://"+*fServerAddr+"/provider/heartbeat", port)
	log.Println("MachineID:", heart.ID)

	log.Printf("u2init listening on port %d", port)
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		panic(err)
	}

	// FIXME(ssx): get apk version from server
	apkVersionConstraint, err = semver.NewConstraint(">= 1.1.5")
	if err != nil {
		panic(err)
	}

	err = heart.Ping()
	if err != nil {
		log.Println("Warning", err)
	}

	go heart.PingForever()
	go func() {
		log.Fatal(http.Serve(ln, nil))
	}()

	adbVersion, err := adb.ServerVersion()
	if err != nil {
		log.Println(err)
	}
	log.Println("Watch and init, adb version", adbVersion)
	watchAndInit(*fServerAddr, heart)
}
