package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// 定义版本号 / Version number
var version = "v1.0.0"

func main() {
	showVersion := flag.Bool("v", false, "Show version information / 显示版本信息")
	pretty := flag.Bool("p", false, "Format output in shell mode (use backslash for line breaks) / 格式化 Shell 模式输出")
	noName := flag.Bool("no-name", false, "Do not include the --name parameter / 不包含 --name 参数")
	showLabels := flag.Bool("l", false, "Include Labels tags (hidden by default) / 包含 Labels 标签 (默认隐藏)")
	ymlMode := flag.Bool("y", false, "Output in Docker Compose YAML format / 以 Docker Compose YAML 格式输出")
	bakAll := flag.Bool("a", false, "Export all containers / 导出所有容器")
	outDir := flag.String("o", ".", "Output directory for -a mode / 导出目录")
	cleanLogs := flag.Bool("c", false, "Clean all containers' json.log files / 清理所有容器的 json.log 文件")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage / 用法: runlike [OPTIONS] <container name>\n\nOptions / 选项:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	// 新增：-v 输出版本号功能
	if *showVersion {
		fmt.Printf("runlike %s\n", version)
		return
	}

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to Docker (无法连接到 Docker): %v\n", err)
		os.Exit(1)
	}

	if *bakAll {
		exportAllContainers(ctx, cli, *noName, *showLabels, *ymlMode, *pretty, *outDir)
		return
	}

	if *cleanLogs {
		cleanDockerLogs(ctx, cli)
		return
	}

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		return
	}

	containerJSON, err := cli.ContainerInspect(ctx, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Container not found (找不到容器): %v\n", err)
		os.Exit(1)
	}

	imgEnvs, imgExposed, imgWorkDir, imgEntrypoint := inspectImageDefaults(ctx, cli, containerJSON.Image)
	containerName := strings.TrimPrefix(containerJSON.Name, "/")

	if *ymlMode {
		fmt.Println(buildCompose(&containerJSON, containerName, imgEnvs, imgExposed, imgWorkDir, imgEntrypoint, *showLabels))
	} else {
		fmt.Println(buildShell(&containerJSON, containerName, imgEnvs, imgExposed, imgWorkDir, imgEntrypoint, *noName, *showLabels, *pretty))
	}
}

func inspectImageDefaults(ctx context.Context, cli *client.Client, imageID string) (map[string]bool, map[string]bool, string, string) {
	imgEnvs := make(map[string]bool)
	imgExposed := make(map[string]bool)
	var imgWorkDir string
	var imgEntrypoint string

	image, _, err := cli.ImageInspectWithRaw(ctx, imageID)
	if err == nil && image.Config != nil {
		for _, e := range image.Config.Env {
			imgEnvs[e] = true
		}
		for p := range image.Config.ExposedPorts {
			imgExposed[string(p)] = true
		}
		imgWorkDir = image.Config.WorkingDir
		imgEntrypoint = strings.Join(image.Config.Entrypoint, " ")
	}
	return imgEnvs, imgExposed, imgWorkDir, imgEntrypoint
}

func collectLinks(json *types.ContainerJSON) []string {
	var rawLinks []string
	if json.HostConfig != nil {
		rawLinks = append(rawLinks, json.HostConfig.Links...)
	}

	if json.NetworkSettings != nil {
		for _, net := range json.NetworkSettings.Networks {
			rawLinks = append(rawLinks, net.Links...)
		}
	}

	seen := make(map[string]bool)
	var result []string

	for _, link := range rawLinks {
		parts := strings.Split(link, ":")
		if len(parts) >= 2 {
			src := strings.TrimPrefix(parts[0], "/")
			aliasParts := strings.Split(parts[1], "/")
			alias := aliasParts[len(aliasParts)-1]

			if strings.HasSuffix(alias, "-1") || strings.Contains(alias, "docker-compose-") {
				continue
			}

			pair := fmt.Sprintf("%s:%s", src, alias)
			if !seen[pair] {
				seen[pair] = true
				result = append(result, pair)
			}
		}
	}
	return result
}

// 修复点：彻底规范化并去除末尾冗余的 :rw，避免重复输出
func collectBinds(json *types.ContainerJSON) []string {
	var binds []string
	seen := make(map[string]bool)

	addBind := func(entry string) {
		clean := strings.TrimSuffix(entry, ":rw")
		if !seen[clean] {
			seen[clean] = true
			binds = append(binds, clean)
		}
	}

	if json.HostConfig != nil {
		for _, b := range json.HostConfig.Binds {
			addBind(b)
		}
	}

	for _, m := range json.Mounts {
		if m.Type == "bind" {
			volOpt := ""
			if !m.RW || m.Mode == "ro" {
				volOpt = ":ro"
			}
			addBind(fmt.Sprintf("%s:%s%s", m.Source, m.Destination, volOpt))
		}
	}

	return binds
}

func collectDevices(json *types.ContainerJSON) []string {
	var devs []string
	seen := make(map[string]bool)

	addDev := func(host, container string) {
		if host != "" && container != "" {
			pair := fmt.Sprintf("%s:%s", host, container)
			if !seen[pair] {
				seen[pair] = true
				devs = append(devs, pair)
			}
		}
	}

	if json.HostConfig != nil {
		for _, d := range json.HostConfig.Devices {
			addDev(d.PathOnHost, d.PathInContainer)
		}
		if json.HostConfig.Resources.Devices != nil {
			for _, d := range json.HostConfig.Resources.Devices {
				addDev(d.PathOnHost, d.PathInContainer)
			}
		}
	}

	// 智能补偿：如果 API 因为特权模式清空了 Devices，但在宿主机环境有硬件需求，自动补全 /dev/dri
	if len(devs) == 0 {
		hasGPUEnv := false
		if json.Config != nil {
			for _, env := range json.Config.Env {
				if strings.Contains(env, "JELLYFIN_FFMPEG") ||
					strings.Contains(env, "VAAPI") ||
					strings.Contains(env, "NVIDIA_VISIBLE_DEVICES") {
					hasGPUEnv = true
					break
				}
			}
		}
		if hasGPUEnv {
			if _, err := os.Stat("/dev/dri"); err == nil {
				addDev("/dev/dri", "/dev/dri")
			}
		}
	}

	return devs
}

func buildShell(json *types.ContainerJSON, name string, imgEnvs map[string]bool, imgExposed map[string]bool, imgWorkDir string, imgEntrypoint string, noName, showLabels, pretty bool) string {
	var p []string

	mode := "-"
	if json.State.Running || (!json.Config.AttachStdin) {
		mode += "d"
	}
	if json.Config.OpenStdin {
		mode += "i"
	}
	if json.Config.Tty {
		mode += "t"
	}

	runCmd := "docker run"
	if len(mode) > 1 {
		runCmd += " " + mode
	}
	p = append(p, runCmd)

	if !noName {
		p = append(p, fmt.Sprintf("--name=%s", name))
	}

	if json.Config.Hostname != "" {
		p = append(p, fmt.Sprintf("--hostname=%s", json.Config.Hostname))
	}

	containerEntrypoint := strings.Join(json.Config.Entrypoint, " ")
	isCommonImageDefault := containerEntrypoint == "/jellyfin/jellyfin" ||
		containerEntrypoint == "/entrypoint.sh" ||
		containerEntrypoint == imgEntrypoint

	if containerEntrypoint != "" && !isCommonImageDefault {
		p = append(p, fmt.Sprintf("--entrypoint=\"%s\"", containerEntrypoint))
	}

	netMode := string(json.HostConfig.NetworkMode)
	if netMode != "default" && netMode != "bridge" && netMode != "" && !strings.HasSuffix(netMode, "_default") {
		p = append(p, fmt.Sprintf("--network=%s", netMode))
	}
	if json.Config.MacAddress != "" {
		p = append(p, fmt.Sprintf("--mac-address=%s", json.Config.MacAddress))
	}

	for _, dns := range json.HostConfig.DNS {
		p = append(p, fmt.Sprintf("--dns=%s", dns))
	}

	links := collectLinks(json)
	for _, link := range links {
		p = append(p, fmt.Sprintf("--link %s", link))
	}

	for _, host := range json.HostConfig.ExtraHosts {
		p = append(p, fmt.Sprintf("--add-host=%s", host))
	}

	if json.Config.User != "" {
		p = append(p, fmt.Sprintf("--user=%s", json.Config.User))
	}
	if json.HostConfig.Privileged {
		p = append(p, "--privileged")
	}
	if json.HostConfig.Init != nil && *json.HostConfig.Init {
		p = append(p, "--init")
	}

	for _, cap := range json.HostConfig.CapAdd {
		p = append(p, fmt.Sprintf("--cap-add=%s", cap))
	}
	for _, cap := range json.HostConfig.CapDrop {
		p = append(p, fmt.Sprintf("--cap-drop=%s", cap))
	}
	for _, s := range json.HostConfig.SecurityOpt {
		if s != "label=disable" {
			p = append(p, fmt.Sprintf("--security-opt %s", s))
		}
	}

	if netMode != "host" {
		published := make(map[string]bool)
		for port, bindings := range json.HostConfig.PortBindings {
			published[string(port)] = true
			for _, b := range bindings {
				if b.HostIP == "" || b.HostIP == "0.0.0.0" {
					p = append(p, fmt.Sprintf("-p %s:%s", b.HostPort, port))
				} else {
					p = append(p, fmt.Sprintf("-p %s:%s:%s", b.HostIP, b.HostPort, port))
				}
			}
		}
		for port := range json.Config.ExposedPorts {
			if !imgExposed[string(port)] && !published[string(port)] {
				p = append(p, fmt.Sprintf("--expose=%s", port))
			}
		}
	}

	for _, b := range collectBinds(json) {
		p = append(p, fmt.Sprintf("-v %s", b))
	}

	for _, dev := range collectDevices(json) {
		p = append(p, fmt.Sprintf("--device %s", dev))
	}

	for dest, options := range json.HostConfig.Tmpfs {
		if options != "" {
			p = append(p, fmt.Sprintf("--tmpfs %s:%s", dest, options))
		} else {
			p = append(p, fmt.Sprintf("--tmpfs %s", dest))
		}
	}
	if json.HostConfig.ReadonlyRootfs {
		p = append(p, "--read-only")
	}

	if json.HostConfig.Memory > 0 {
		p = append(p, fmt.Sprintf("-m %db", json.HostConfig.Memory))
	}
	if json.HostConfig.ShmSize > 0 && json.HostConfig.ShmSize != 67108864 {
		p = append(p, fmt.Sprintf("--shm-size=%d", json.HostConfig.ShmSize))
	}
	for key, val := range json.HostConfig.Sysctls {
		p = append(p, fmt.Sprintf("--sysctl %s=%s", key, val))
	}
	for _, u := range json.HostConfig.Ulimits {
		p = append(p, fmt.Sprintf("--ulimit %s=%d:%d", u.Name, u.Soft, u.Hard))
	}

	if json.Config.WorkingDir != "" && json.Config.WorkingDir != imgWorkDir {
		p = append(p, fmt.Sprintf("--workdir=%s", json.Config.WorkingDir))
	}
	for _, env := range json.Config.Env {
		if !imgEnvs[env] {
			p = append(p, fmt.Sprintf("--env=\"%s\"", env))
		}
	}
	if json.Config.StopSignal != "" {
		p = append(p, fmt.Sprintf("--stop-signal=%s", json.Config.StopSignal))
	}

	if json.HostConfig.RestartPolicy.Name != "" {
		p = append(p, fmt.Sprintf("--restart=%s", json.HostConfig.RestartPolicy.Name))
	}
	if len(json.HostConfig.LogConfig.Config) > 0 {
		for key, val := range json.HostConfig.LogConfig.Config {
			p = append(p, fmt.Sprintf("--log-opt %s=%s", key, val))
		}
	}

	if string(json.HostConfig.PidMode) != "" && string(json.HostConfig.PidMode) != "default" {
		p = append(p, fmt.Sprintf("--pid=%s", json.HostConfig.PidMode))
	}
	if string(json.HostConfig.IpcMode) != "" && string(json.HostConfig.IpcMode) != "default" && string(json.HostConfig.IpcMode) != "private" {
		p = append(p, fmt.Sprintf("--ipc=%s", json.HostConfig.IpcMode))
	}

	if showLabels {
		for k, v := range json.Config.Labels {
			safeVal := strings.ReplaceAll(v, "\"", "\\\"")
			p = append(p, fmt.Sprintf("--label=\"%s=%s\"", k, safeVal))
		}
	}

	p = append(p, json.Config.Image)
	if len(json.Config.Cmd) > 0 {
		p = append(p, strings.Join(json.Config.Cmd, " "))
	}

	sep := " "
	if pretty {
		sep = " \\\n\t"
	}
	return strings.Join(p, sep)
}

func buildCompose(json *types.ContainerJSON, name string, imgEnvs map[string]bool, imgExposed map[string]bool, imgWorkDir string, imgEntrypoint string, showLabels bool) string {
	var b strings.Builder

	b.WriteString("services:\n")
	fmt.Fprintf(&b, "  %s:\n", name)
	fmt.Fprintf(&b, "    image: %s\n", json.Config.Image)
	fmt.Fprintf(&b, "    container_name: %s\n", name)

	containerEntrypoint := strings.Join(json.Config.Entrypoint, " ")
	isCommonImageDefault := containerEntrypoint == "/jellyfin/jellyfin" ||
		containerEntrypoint == "/entrypoint.sh" ||
		containerEntrypoint == imgEntrypoint

	if containerEntrypoint != "" && !isCommonImageDefault {
		fmt.Fprintf(&b, "    entrypoint: %s\n", containerEntrypoint)
	}

	netMode := string(json.HostConfig.NetworkMode)
	isSpecialNet := netMode == "host" || netMode == "none" || strings.HasPrefix(netMode, "container:")

	if isSpecialNet {
		fmt.Fprintf(&b, "    network_mode: %s\n", netMode)
	} else {
		var customNets []string
		if json.NetworkSettings != nil {
			for netName := range json.NetworkSettings.Networks {
				if netName != "bridge" && netName != "default" && !strings.HasSuffix(netName, "_default") {
					customNets = append(customNets, netName)
				}
			}
		}
		if len(customNets) > 0 {
			b.WriteString("    networks:\n")
			for _, netName := range customNets {
				fmt.Fprintf(&b, "      - %s\n", netName)
			}
		}
	}

	if json.Config.Hostname != "" {
		fmt.Fprintf(&b, "    hostname: %s\n", json.Config.Hostname)
	}
	if json.Config.MacAddress != "" {
		fmt.Fprintf(&b, "    mac_address: %s\n", json.Config.MacAddress)
	}

	if len(json.HostConfig.DNS) > 0 {
		b.WriteString("    dns:\n")
		for _, dns := range json.HostConfig.DNS {
			fmt.Fprintf(&b, "      - %s\n", dns)
		}
	}

	links := collectLinks(json)
	if len(links) > 0 {
		b.WriteString("    links:\n")
		for _, link := range links {
			fmt.Fprintf(&b, "      - %s\n", link)
		}
	}

	if len(json.HostConfig.ExtraHosts) > 0 {
		b.WriteString("    extra_hosts:\n")
		for _, host := range json.HostConfig.ExtraHosts {
			fmt.Fprintf(&b, "      - \"%s\"\n", host)
		}
	}

	if json.HostConfig.Privileged {
		b.WriteString("    privileged: true\n")
	}
	if json.HostConfig.Init != nil && *json.HostConfig.Init {
		b.WriteString("    init: true\n")
	}
	if len(json.HostConfig.CapAdd) > 0 {
		b.WriteString("    cap_add:\n")
		for _, c := range json.HostConfig.CapAdd {
			fmt.Fprintf(&b, "      - %s\n", c)
		}
	}
	if len(json.HostConfig.CapDrop) > 0 {
		b.WriteString("    cap_drop:\n")
		for _, c := range json.HostConfig.CapDrop {
			fmt.Fprintf(&b, "      - %s\n", c)
		}
	}

	var validSecOpts []string
	for _, s := range json.HostConfig.SecurityOpt {
		if s != "label=disable" {
			validSecOpts = append(validSecOpts, s)
		}
	}
	if len(validSecOpts) > 0 {
		b.WriteString("    security_opt:\n")
		for _, s := range validSecOpts {
			fmt.Fprintf(&b, "      - %s\n", s)
		}
	}

	if !isSpecialNet && len(json.HostConfig.PortBindings) > 0 {
		b.WriteString("    ports:\n")
		for p, bindings := range json.HostConfig.PortBindings {
			for _, bind := range bindings {
				if bind.HostIP != "" && bind.HostIP != "0.0.0.0" {
					fmt.Fprintf(&b, "      - \"%s:%s:%s\"\n", bind.HostIP, bind.HostPort, p)
				} else {
					fmt.Fprintf(&b, "      - \"%s:%s\"\n", bind.HostPort, p)
				}
			}
		}
	}

	if json.Config.Tty {
		b.WriteString("    tty: true\n")
	}
	if json.Config.OpenStdin {
		b.WriteString("    stdin_open: true\n")
	}
	if json.HostConfig.RestartPolicy.Name != "" {
		fmt.Fprintf(&b, "    restart: %s\n", json.HostConfig.RestartPolicy.Name)
	}

	binds := collectBinds(json)
	if len(binds) > 0 || len(json.HostConfig.Tmpfs) > 0 {
		b.WriteString("    volumes:\n")
		for _, bStr := range binds {
			fmt.Fprintf(&b, "      - %s\n", bStr)
		}
		for dest, options := range json.HostConfig.Tmpfs {
			if options != "" {
				fmt.Fprintf(&b, "      - type: tmpfs\n        target: %s\n        tmpfs:\n          size: %s\n", dest, options)
			} else {
				fmt.Fprintf(&b, "      - type: tmpfs\n        target: %s\n", dest)
			}
		}
	}
	if json.HostConfig.ReadonlyRootfs {
		b.WriteString("    read_only: true\n")
	}

	devices := collectDevices(json)
	if len(devices) > 0 {
		b.WriteString("    devices:\n")
		for _, dev := range devices {
			fmt.Fprintf(&b, "      - \"%s\"\n", dev)
		}
	}

	if json.HostConfig.ShmSize > 0 && json.HostConfig.ShmSize != 67108864 {
		fmt.Fprintf(&b, "    shm_size: %d\n", json.HostConfig.ShmSize)
	}
	if json.HostConfig.Memory > 0 {
		fmt.Fprintf(&b, "    mem_limit: %d\n", json.HostConfig.Memory)
	}

	var customEnvs []string
	for _, env := range json.Config.Env {
		if !imgEnvs[env] {
			customEnvs = append(customEnvs, env)
		}
	}
	if len(customEnvs) > 0 {
		b.WriteString("    environment:\n")
		for _, e := range customEnvs {
			fmt.Fprintf(&b, "      - %s\n", e)
		}
	}

	if json.HostConfig.LogConfig.Type != "" && json.HostConfig.LogConfig.Type != "none" {
		b.WriteString("    logging:\n")
		fmt.Fprintf(&b, "      driver: \"%s\"\n", json.HostConfig.LogConfig.Type)
		if len(json.HostConfig.LogConfig.Config) > 0 {
			b.WriteString("      options:\n")
			for k, v := range json.HostConfig.LogConfig.Config {
				fmt.Fprintf(&b, "        %s: \"%s\"\n", k, v)
			}
		}
	}

	if len(json.HostConfig.Sysctls) > 0 {
		b.WriteString("    sysctls:\n")
		for k, v := range json.HostConfig.Sysctls {
			fmt.Fprintf(&b, "      %s: %s\n", k, v)
		}
	}
	if len(json.HostConfig.Ulimits) > 0 {
		b.WriteString("    ulimits:\n")
		for _, u := range json.HostConfig.Ulimits {
			fmt.Fprintf(&b, "      %s:\n        soft: %d\n        hard: %d\n", u.Name, u.Soft, u.Hard)
		}
	}

	if string(json.HostConfig.PidMode) != "" && string(json.HostConfig.PidMode) != "default" {
		fmt.Fprintf(&b, "    pid: %s\n", json.HostConfig.PidMode)
	}
	if string(json.HostConfig.IpcMode) != "" && string(json.HostConfig.IpcMode) != "default" && string(json.HostConfig.IpcMode) != "private" {
		fmt.Fprintf(&b, "    ipc: %s\n", json.HostConfig.IpcMode)
	}
	if json.Config.StopSignal != "" {
		fmt.Fprintf(&b, "    stop_signal: %s\n", json.Config.StopSignal)
	}

	if showLabels && len(json.Config.Labels) > 0 {
		b.WriteString("    labels:\n")
		for k, v := range json.Config.Labels {
			fmt.Fprintf(&b, "      %s: \"%s\"\n", k, v)
		}
	}

	if len(json.Config.Cmd) > 0 {
		fmt.Fprintf(&b, "    command: %s\n", strings.Join(json.Config.Cmd, " "))
	}

	if !isSpecialNet && json.NetworkSettings != nil {
		hasCustomNet := false
		for netName := range json.NetworkSettings.Networks {
			if netName != "bridge" && netName != "default" && !strings.HasSuffix(netName, "_default") {
				if !hasCustomNet {
					b.WriteString("\nnetworks:\n")
					hasCustomNet = true
				}
				fmt.Fprintf(&b, "  %s:\n    external: true\n", netName)
			}
		}
	}

	return b.String()
}

func safeFileName(name string) string {
	return strings.ReplaceAll(name, "/", "_")
}

func isRoot() bool {
	return os.Geteuid() == 0
}

func humanSize(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func cleanDockerLogs(ctx context.Context, cli *client.Client) {
	if !isRoot() {
		fmt.Println("🔒 Root permission required, requesting via sudo (需要 Root 权限，正在尝试通过 sudo 运行)...")
		cmd := exec.Command("sudo", append([]string{os.Args[0]}, os.Args[1:]...)...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}
		return
	}

	info, err := cli.Info(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to get Docker info (获取 Docker 信息失败): %v\n", err)
		os.Exit(1)
	}
	dockerRoot := info.DockerRootDir

	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to list containers (列出容器失败): %v\n", err)
		os.Exit(1)
	}

	if len(containers) == 0 {
		fmt.Println("No containers found (未找到任何容器).")
		return
	}

	fmt.Printf("🔍 Docker Root Dir (Docker 根目录): %s\n", dockerRoot)
	fmt.Printf("📊 Found %d containers (共发现 %d 个容器)\n\n", len(containers), len(containers))

	cleaned := 0
	for i, c := range containers {
		name := strings.TrimPrefix(c.Names[0], "/")
		id := c.ID
		logPath := filepath.Join(dockerRoot, "containers", id, id+"-json.log")

		sizeStr := "N/A"
		if fi, err := os.Stat(logPath); err == nil {
			sizeStr = humanSize(fi.Size())
		} else if os.IsNotExist(err) {
			continue
		}

		fmt.Printf("  %d. %-30s  id: %-12s  log: %s\n", i+1, name, id[:12], sizeStr)

		if err := os.Truncate(logPath, 0); err != nil {
			fmt.Fprintf(os.Stderr, "     ❌ Clean failed (清理失败): %v\n", err)
			continue
		}
		fmt.Printf("     ✅ Cleared (已清理): %s\n", logPath)
		cleaned++
	}
	fmt.Printf("\n✅ Done! %d log files cleaned (清理完成! 共处理 %d 个日志文件)\n", cleaned, cleaned)
}

func exportAllContainers(ctx context.Context, cli *client.Client, noName, showLabels, ymlMode, pretty bool, outDir string) {
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to list containers (列出容器失败): %v\n", err)
		os.Exit(1)
	}

	if len(containers) == 0 {
		fmt.Println("No containers found (未找到任何容器).")
		return
	}

	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to create directory (创建目录失败) %s: %v\n", outDir, err)
		os.Exit(1)
	}

	doShell := pretty || (!ymlMode && !pretty)
	doYml := ymlMode || (!ymlMode && !pretty)

	var shellFile *os.File
	if doShell {
		var err error
		shellFile, err = os.Create(filepath.Join(outDir, "docker_run_shell.txt"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to create combined shell file (创建 Shell 汇总文件失败): %v\n", err)
			return
		}
		defer shellFile.Close()
	}

	fmt.Printf("📂 Exporting to (正在导出至): %s\n", outDir)

	for _, c := range containers {
		name := strings.TrimPrefix(c.Names[0], "/")
		containerJSON, err := cli.ContainerInspect(ctx, c.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Skip %s: Inspect failed (获取配置失败): %v\n", name, err)
			continue
		}
		imgEnvs, imgExposed, imgWorkDir, imgEntrypoint := inspectImageDefaults(ctx, cli, containerJSON.Image)

		if doYml {
			// 1. 为每个容器创建独立的子目录 (outDir/容器名)
			// 使用 safeFileName 防止容器名中包含非法路径字符
			containerDir := filepath.Join(outDir, safeFileName(name))
			if err := os.MkdirAll(containerDir, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Failed to create folder for %s (创建容器目录失败): %v\n", name, err)
				continue
			}

			// 2. 将 Compose 文件统一命名为 docker-compose.yml 存入该目录下
			ymlContent := buildCompose(&containerJSON, name, imgEnvs, imgExposed, imgWorkDir, imgEntrypoint, showLabels)
			composePath := filepath.Join(containerDir, "docker-compose.yml")
			
			err := os.WriteFile(composePath, []byte(ymlContent), 0644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Failed to save YML (保存 YML 失败) %s: %v\n", name, err)
			} else {
				fmt.Printf("✅ %-20s -> %s\n", name, filepath.Join(safeFileName(name), "docker-compose.yml"))
			}
		}

		if doShell {
			shellCmd := buildShell(&containerJSON, name, imgEnvs, imgExposed, imgWorkDir, imgEntrypoint, noName, showLabels, true)
			fmt.Fprintf(shellFile, "# Container (容器): %s\n%s\n\n", name, shellCmd)
		}
	}

	if doShell {
		fmt.Printf("✅ Combined shell script saved (Shell 汇总已保存至): %s\n", filepath.Join(outDir, "docker_run_shell.txt"))
	}
	fmt.Printf("\n✨ Export Finished (导出完成)!\n")
}
