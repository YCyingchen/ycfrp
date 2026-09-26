YCFRP s2609.031 —— FRP 双端管理面板（Linux 版）
==============================================

内置 frp 内核 v0.71.0，单文件部署，无需安装任何运行时依赖。

一、文件说明
------------
  ycfrp                     主程序（静态编译，可直接运行）
  start.sh                  后台启动脚本（自动判断是否已在运行）
  stop.sh                   停止脚本（优雅退出）
  status.sh                 状态查询脚本
  ycfrp.service             systemd 服务单元模板
  docker-compose.yml        Docker Compose 部署文件
  README-linux.txt          本说明文件

二、快速开始
------------
  1) 安装主程序

       sudo install -m 755 ycfrp /usr/local/bin/ycfrp

  2) 前台试运行（方便查看启动日志，可选）

       sudo ycfrp -c /var/lib/ycfrp -p 38080

  3) 后台运行（推荐用脚本）

       sudo ./start.sh

     脚本会调用 ycfrp -d 在后台拉起面板，并在结束后打印面板地址。
     也可直接使用命令：

       sudo ycfrp -d -c /var/lib/ycfrp -p 38080

  4) 浏览器访问面板

       http://<服务器地址>:38080
       默认账号：admin / admin  （首次登录后请立即修改）

  常用脚本参数：

       sudo ./start.sh -p 39000             指定面板端口
       sudo ./start.sh -c /opt/ycfrp/data   指定数据目录（须与停止时一致）
       sudo ./status.sh                     查看运行状态与进程号
       sudo ./stop.sh                       停止后台运行的面板

三、注册为 systemd 服务（长期运行推荐）
------------------------------
  1) 复制服务单元并启用

       sudo install -m 644 ycfrp.service /etc/systemd/system/ycfrp.service
       sudo systemctl daemon-reload
       sudo systemctl enable --now ycfrp

  2) 查看状态与日志

       sudo systemctl status ycfrp
       sudo journalctl -u ycfrp -f

四、常用命令
------------
  ycfrp -v                  显示版本信息
  ycfrp -h                  显示全部参数
  ycfrp -d                  后台守护运行
  ycfrp -status             查看后台运行状态
  ycfrp -stop               停止后台运行的面板
  ycfrp -open               用系统浏览器打开面板（桌面环境）
  ycfrp -c <目录>           指定数据目录（配置、日志、隧道数据）
  ycfrp -p <端口>           指定面板监听端口
  ycfrp --reset-password    重置登录账号为 admin / admin

五、默认端口
------------
  38080   管理面板（HTTP）
  7000    frps 与 frpc 通信端口
  7500    frps 内核仪表盘接口（仅本机）
  8080    HTTP 类型隧道虚拟主机
  8443    HTTPS 类型隧道虚拟主机

  如启用防火墙，请放行面板端口与 frps 通信端口：

    sudo ufw allow 38080/tcp
    sudo ufw allow 7000/tcp

六、数据目录
------------
  默认位置：/var/lib/ycfrp
  包含内容：config.json（面板配置）、tunnels.json（隧道列表）、logs/（日志）

  备份该目录即可完整迁移面板配置。

七、Docker 部署（推荐 compose）
-------------------------------
  1) 创建目录并放入 compose 文件

       mkdir -p /opt/ycfrp && cd /opt/ycfrp

     使用随包附带的 docker-compose.yml（也可从下载站取得），内容如下：

       services:
         ycfrp:
           image: ycyingchen/ycfrp:s2609.031
           container_name: ycfrp
           restart: unless-stopped
           environment:
             - TZ=Asia/Shanghai
           volumes:
             - ./data:/var/lib/ycfrp
           network_mode: host

  2) 启动与查看状态

       docker compose up -d
       docker compose logs -f

  3) 浏览器访问面板

       http://<服务器地址>:38080

  4) 升级镜像

       docker compose pull && docker compose up -d

  说明：
    镜像名 ycyingchen/ycfrp 对应 Docker Hub 官方仓库。
    上面使用 host 网络（frps 需要监听隧道远程端口，默认 20000-60000 区间），
    若改用桥接网络，需在 ports 中逐条映射隧道端口。

八、问题排查
------------
  面板无法访问       检查进程是否存活、端口是否被占用、防火墙是否放行。
  frpc 连接失败      核对服务端地址、端口与 auth.token 是否与 frps 一致。
  端口被占用         修改隧道远程端口，或释放占用该端口的进程后重启隧道。

  面板内置中文日志与故障定位功能，可在「日志」页面直接查看成因与处理建议。

九、以普通用户运行（非 root）
--------------------------------
  面板与 frpc 客户端无需 root 权限即可运行；只有 frps 服务端绑定 7000/7500
  低位端口时才需要额外处理。推荐做法：

  1) 创建专用账号并接管数据目录

       sudo useradd -r -s /usr/sbin/nologin ycfrp
       sudo mkdir -p /var/lib/ycfrp
       sudo chown -R ycfrp:ycfrp /var/lib/ycfrp

  2) 给主程序加绑定低位端口的能力（仅当需要跑 frps 服务端时）

       sudo setcap 'cap_net_bind_service=+ep' /usr/local/bin/ycfrp

  3) 用 systemd 以该账号运行

       ycfrp.service 里已写 User=ycfrp / Group=ycfrp，按第三节步骤启用即可。
       （若不跑 frps、只跑 frpc 客户端，可跳过第 2 步。）

  Docker 方式同理：镜像已以非 root 用户（uid 1000）运行，挂载的 ./data
  目录需 chown 1000:1000（见 compose 文件内注释）。
