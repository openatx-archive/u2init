# uiautomator2 init offline
This is for project [uiautomator2](https://github.com/openatx/uiautomator2).

## Usage
First you need to download resources from github through a bash script.

```
./init-resources.sh
```


Assume your atx-server addr is `10.0.0.1:8000`

```bash
go run main.go -server 10.0.0.1:8000
```

Then you are ready to go. Any plugin devices will be inited automaticlly.

## LICENSE
[MIT](LICENSE)
