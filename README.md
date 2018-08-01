# uiautomator2 init offline
This is for project [uiautomator2](https://github.com/openatx/uiautomator2).

## Installation
First install [go](https://golang.org) environment

```bash
$ go get -v github.com/openatx/u2init
$ cd $GOPATH/src/github.com/openatx/u2init
$ go build

# download stf stuffs(minitouch, minicap), atx-agent, uiautomator.apk(two apk actually)
$ ./init-resources.sh
```

## Usage
Assume your atx-server addr is `10.0.0.1:8000`

```bash
./u2init -server 10.0.0.1:8000
```

u2init is also provider service to install apk through REST API

Launch u2init with options `-p $PORT`, default is a random port

```bash
./u2init -p 8000
```

Open another terminal, the `$SERIAL` is the device serial number.

## How to start u2init automatically on boot (RaspberryPi)
First you need to run as root

```bash
$ ./u2init --initd > /etc/init.d/u2init
$ update-rc.d u2init defaults
```

That's all, when raspberry next reboot, u2init will started automatically

## REST API
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

Then you are ready to go. Any plugin devices will be inited automaticlly.

## LICENSE
[MIT](LICENSE)
