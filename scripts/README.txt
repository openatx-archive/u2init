树莓派部署说明
============

u2init 目前是部署在 /home/pi/opt/u2init-releases 下面，使用root管理的pm2(Process Manager)工具管理。
使用ansible完成自动部署

1. 先使用`./update-rpis.py > hosts.ini` 生成树莓派机器的IP列表

2. 树莓派扩容
树莓派的SD卡默认并没有将全部空间占满，所以需要expand fs一下

```
ansible -i hosts.ini expand-pi-fs.yml
```

3. 更新u2init
先在代码目录使用命令`GOOS=linux GOARCH=arm go build`编译出一个树莓派的二进制

部署到所有树莓派上

```
ansible -i hosts.ini playbooks/update-u2init.yml
```