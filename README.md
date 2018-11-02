# uiautomator2 init for atx-server
**Beta** 仍在开发中

This is project relies on project [atx-server](https://github.com/openatx/atx-server)

So you need atx-server installed before use this project.

u2init is very similar to stf-provider.
If there is android phone connected to a PC which running u2init, Some resources(minicap, minitouch, apks, atx-agent) will be pushed into device automatically. And you can see this device show up in atx-server in a minute.

## Installation
First install [go](https://golang.org) environment

```bash
$ go get -v github.com/openatx/u2init
$ cd $GOPATH/src/github.com/openatx/u2init
$ go build

# download stf stuffs(minitouch, minicap), uiautomator.apk(two apk actually)
$ ./init-resources.sh
```

## Usage
Assume your atx-server addr is `10.0.0.1:8000`

```bash
./u2init --server 10.0.0.1:8000
```

u2init is also provider service to install apk through REST API

Use `./u2init -h` to known more usages.

## How it works
Download **atx-agent**

1. u2init get atx-agent version from URL `$ATX_SERVER_URL/version`.
2. If not found the specified version of atx-agent in dir `./resources`, atx-agent will downloaded from github.

## Enable u2init start automatically on boot (RaspberryPi)
First you need to run as root

```bash
$ ./u2init --server 10.0.0.1:8000 --initd > /etc/init.d/u2init # server addr should be modified
$ update-rc.d u2init defaults 90 # 启动级别90
```

That's all, when raspberry reboot, u2init will started automatically

## REST API （设计中）
**获取设备列表**

```
GET $SERVER_URL/devices
```

Response

```json
{
    "success": true,
    "data": [
        {"serial": "3ffecdf", "product": "MHA-AL00", "model": "MHA_AL00", "device": "HWMHA"},
        {"serial": "6EB0217607006479", "product": "DUK-AL20", "model": "DUK_AL20", "device": "HWDUK"}
    ]
}
```

**安装应用**

```bash
POST $SERVER_URL/devices/${serial}/pkgs
```

Params

Name | Type | Example value
-----|------|--------------
url  | string | http://www.example.org/some.apk
file | file (url or file must have one) | 文件类型

Response (SUCCESS)

```json
{
    "success": true,
    "data": {
        "id": "1"
    }
}
```

Response (FAILURE)

```json
{
    "success": false,
    "description": "url is invalid"
}
```

**查询安装进度**

```bash
GET $SERVER_URL/devices/${serial}/pkgs/${id}
```

Response example 1 (下载文件中)

```json
{
    "success": true,
    "data": {
        "status": "downloading",
        "description": "total 100M speed": "3 MB/s"
    }
}
```

Response example 2 （安装成功）

```json
{
    "success": true,
    "data": {
        "status": "success",
        "description": ""
    }
}
```

status字段的其他值有 `downloading`, `pushing`, `installing`, `success`, `failure`

安装异常时 success字段为false

Response example failure

```json
{
    "success": false,
    "description": "downloading EOF error"
}
```

**取消安装** TODO

```bash
DELETE $SERVER_URL/devices/${serial}/pkgs/${id}
```

Response

```json
{
    "success": true,
    "description": "install canceled"
}
```

**旧的接口（即将废弃)**

```bash
# Only support URL now.
$ curl -X POST -F url="https://gohttp.nie.netease.com/tools/apks/qrcodescan-2.6.0-green.apk" localhost:8000/install/$SERIAL
7
# You will get id like 7
# Then query progress through this id
$ curl -X GET localhost:8000/install/7
{
    "id": "7",
    "copiedSize": 371543214,
    "totalSize": 371543214,
    "message": "installing"
}
# message can be "pushing", "installing", "finished" or "err: xxxx-some failure resone here-xxxx"
```

Then you are ready to go. Any plugged-in devices will be inited automaticlly.

## LICENSE
[MIT](LICENSE)
