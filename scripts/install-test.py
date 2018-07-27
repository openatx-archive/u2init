# coding: utf-8
#

import time
import requests

# https://gohttp.nie.netease.com/tools/apks/qrcodescan-2.6.0-green.apk
r = requests.post("http://localhost:8000/install/3578298f",
                  data={"url": "http://test.nie.netease.com/apps_file/3b4c311069349f53f"})
r.raise_for_status()

id = r.text
print("id:", id)

start = time.time()

while 1:
    time.sleep(1)
    r = requests.get("http://localhost:8000/install/"+id)
    jdata = r.json()
    message = jdata.get('message', '')
    percent = 0.0
    total = jdata.get('totalSize', 0)
    copied = jdata.get('copiedSize', 0)
    if total == 0:
        percent = 0
    else:
        percent = float(copied) / total
    speed_mb = float(copied) / (time.time() - start) / 1024.0/1024.0

    progress = "%.1f %% %.1f MB/s" % (percent*100, speed_mb)
    print(r.text, progress)
    if message.startswith("err:"):
        raise RuntimeError(message)
    if message == "finished":
        break
