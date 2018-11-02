package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/semver"
	"github.com/alecthomas/kingpin"
	"github.com/cavaliercoder/grab"
	"github.com/franela/goreq"
	"github.com/mholt/archiver"
	"github.com/phayes/freeport"
	"github.com/pkg/errors"
	"github.com/qiniu/log"
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

func initEverything(device *goadb.Device, serverAddr string) error {
	props, err := device.Properties()
	if err != nil {
		return err
	}
	sdk := props["ro.build.version.sdk"]
	abi := props["ro.product.cpu.abi"]
	pre := props["ro.build.version.preview_sdk"]
	// arch := props["ro.arch"]
	log.Printf("product model: %s\n", props["ro.product.model"])

	if pre != "" && pre != "0" {
		sdk += pre
	}
	log.Println("Process minicap and minitouch")
	if err := initSTFMiniTools(device, abi, sdk); err != nil {
		return errors.Wrap(err, "mini(cap|touch)")
	}
	log.Println("Process app-uiautomator[-test].apk")
	if err := initAPK(device); err != nil {
		return errors.Wrap(err, "app-uiautomator[-test].apk")
	}

	log.Println("Process atx-agent")
	atxAgentPath := filepath.Join(resourcesDir, "atx-agent-armv6/atx-agent")
	// atxAgentPath = filepath.Join(resourcesDir, "../../atx-agent/atx-agent")
	// print(atxAgentPath)
	if err := writeFileToDevice(device, atxAgentPath, "/data/local/tmp/atx-agent", 0755); err != nil {
		return errors.Wrap(err, "atx-agent")
	}

	device.RunCommand("/data/local/tmp/atx-agent", "-stop") // TODO(ssx): stop atx-agent first to force update

	args := []string{"-d", "-nouia"}
	if serverAddr != "" {
		args = append(args, "-t", serverAddr)
	}
	output, err := device.RunCommand("/data/local/tmp/atx-agent", args...)
	output = strings.TrimSpace(output)
	if err != nil {
		return errors.Wrap(err, "start atx-agent")
	}
	serial, _ := device.Serial()
	fmt.Println(serial, output)
	return nil
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

func installAPK(device *goadb.Device, localPath string) error {
	dstPath := "/data/local/tmp/" + filepath.Base(localPath)
	if err := writeFileToDevice(device, localPath, dstPath, 0644); err != nil {
		return err
	}
	defer device.RunCommand("rm", dstPath)
	output, err := device.RunCommand("pm", "install", "-r", "-t", dstPath)
	if err != nil {
		return err
	}
	if !strings.Contains(output, "Success") {
		return errors.Wrap(errors.New(output), "apk-install")
	}
	return nil
}

// check if apk info right
func checkAPK(device *goadb.Device) bool {
	info, err := device.StatPackage("com.github.uiautomator")
	if err != nil {
		log.Debugf("package com.github.uiautomator not installed")
		return false
	}
	curver, err := semver.NewVersion(info.Version.Name)
	if err != nil {
		log.Debugf("package com.github.uiautomator version format error, %s", info.Version.Name)
		return false
	}
	if !apkVersionConstraint.Check(curver) {
		log.Debugf("package com.github.uiautomator version outdated, %s", info.Version.Name)
		return false
	}
	if info.Version.Name != versions.ApkVersion {
		log.Debugf("apk version %s > required version %s", info.Version.Name, versions.ApkVersion)
	}
	_, err = device.StatPackage("com.github.uiautomator.test")
	if err != nil {
		log.Debugf("package com.github.uiautomator.test not installed")
	}

	info, err = device.StatPackage("com.easetest.recorder")
	if err != nil {
		log.Debugf("package com.easetest.recorder not installed")
		return false
	}

	if info.Version.Name != versions.RecordVersion {
		log.Debugf("package com.easetest.recorder version outdated, %s", info.Version.Name)
		return false
	}

	return err == nil
}

func initAPK(device *goadb.Device) (err error) {
	if checkAPK(device) {
		log.Printf("uiautomator-apks already installed")
		return
	}
	device.RunCommand("pm", "uninstall", "com.github.uiautomator.test")
	device.RunCommand("pm", "uninstall", "com.github.uiautomator")
	device.RunCommand("pm", "uninstall", "com.easetest.recorder")
	err = installAPK(device, filepath.Join(resourcesDir, fmt.Sprintf("app-uiautomator-%s.apk", versions.ApkVersion)))
	if err != nil {
		return errors.Wrap(err, "com.github.uiautomator")
	}
	err = installAPK(device, filepath.Join(resourcesDir, fmt.Sprintf("app-uiautomator-test-%s.apk", versions.ApkVersion)))
	if err != nil {
		return errors.Wrap(err, "com.github.uiautomator.test")
	}
	err = installAPK(device, filepath.Join(resourcesDir, fmt.Sprintf("com.easetest.recorder_%s.apk", versions.RecordVersion)))
	if err != nil {
		return errors.Wrap(err, "com.easetest.recorder")
	}
	if checkAPK(device) {
		log.Printf("uiautomator-apks successfully installed")
		return nil
	}
	return errors.New("install apk failed")
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
	// res, err := goreq.Request{
	// 	Method: "GET",
	// 	Uri:    fmt.Sprintf("http://127.0.0.1:%d/info", forwardedPort),
	// }.Do()
	if err != nil {
		return
	}
	defer res.Body.Close()
	if err = res.Body.FromJsonTo(&v); err != nil {
		return
	}
	return v.Udid, forwardedPort, nil
}

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
			device.RunCommand("am", "start", "-n", "com.github.uiautomator/.IdentifyActivity",
				"-e", "theme", "black")

			udid, forwardedPort, err := deviceUdid(device)
			if err != nil {
				log.Println(event.Serial, err)
				continue
			}
			log.Println(event.Serial, "UDID", udid)
			log.Println(event.Serial, "7912 forward to", forwardedPort)
			if heart != nil {
				heart.AddData(event.Serial, map[string]interface{}{
					"udid":                  udid,
					"status":                "online",
					"providerForwardedPort": forwardedPort,
				})
			}
			log.Println(event.Serial, "Init Success")
		}
		if event.WentOffline() {
			log.Printf("Device %s went offline", event.Serial)
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

func getVersions(serverAddr string) (vers Versions, err error) {
	res, err := goreq.Request{
		Method: "GET",
		Uri:    serverAddr + "/version",
	}.Do()
	if err != nil {
		return
	}
	defer res.Body.Close()
	err = res.Body.FromJsonTo(&vers)
	return
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

func initResources(serverAddr string) error {
	vers, err := getVersions(serverAddr)
	if err != nil {
		return err
	}
	// TODO: atx-server should contains apk version
	if vers.ApkVersion == "" {
		vers.ApkVersion = "1.1.5"
	}
	if vers.RecordVersion == "" {
		vers.RecordVersion = "1.0"
	}
	versions = vers
	// atx-agent
	githubMirror := "https://github-mirror.open.netease.com"
	agentReleaseURL := FormatString(githubMirror+"/openatx/atx-agent/releases/download/${ATX_AGENT_VERSION}/atx-agent_${ATX_AGENT_VERSION}_linux_armv6.tar.gz", map[string]string{
		"ATX_AGENT_VERSION": vers.AgentVersion,
	})
	log.Println("Agent version", vers.AgentVersion)
	log.Println("Download from", agentReleaseURL)
	dstPath := resourcesDir + fmt.Sprintf("/atx-agent-%s.tar.gz", vers.AgentVersion)
	cached, err := httpDownload(dstPath, agentReleaseURL)
	if err != nil {
		return err
	}
	if cached {
		log.Info("Use cached resource")
	}

	recordReleaseURL := githubMirror + "/openatx/android-uiautomator-server/releases/download/1.1.5/com.easetest.recorder_1.1.apk"
	// recordReleaseURL := FormatString("http://arch.s3.netease.com/hzdev-appci/com.easetest.recorder_${RECORD_VERSION}.apk", map[string]string{
	// 	"RECORD_VERSION": vers.RecordVersion,
	// })
	recordDst := resourcesDir + FormatString("/com.easetest.recorder_${RECORD_VERSION}.apk", map[string]string{
		"RECORD_VERSION": vers.RecordVersion,
	})
	_, err = httpDownload(recordDst, recordReleaseURL)
	if err != nil {
		return err
	}
	err = archiver.TarGz.Open(dstPath, resourcesDir+"/atx-agent-armv6")
	if err != nil {
		return errors.Wrap(err, "open targz")
	}

	// app-uiautomator apks
	apkReleaseBaseURL := FormatString(githubMirror+"/openatx/android-uiautomator-server/releases/download/${APK_VERSION}/", map[string]string{
		"APK_VERSION": vers.ApkVersion,
	})
	suffix := fmt.Sprintf("-%s.apk", vers.ApkVersion)
	_, err = httpDownload(resourcesDir+"/app-uiautomator"+suffix, apkReleaseBaseURL+"app-uiautomator.apk")
	if err != nil {
		return err
	}
	_, err = httpDownload(resourcesDir+"/app-uiautomator-test"+suffix, apkReleaseBaseURL+"app-uiautomator-test.apk")
	return err
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

	stfBinariesDir = filepath.Join(resourcesDir, "stf-binaries-master/node_modules")

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
	} else {
		// FIXME(ssx): init resources on every time reconnected
		err := initResources("http://" + *fServerAddr)
		if err != nil {
			log.Warnf("init resources %v", err)
		}
		log.Println("resources downloaded. ^_^")
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
