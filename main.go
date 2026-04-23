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

func main() {
	pretty := flag.Bool("p", false, "Format output in shell mode (use backslash for line breaks)")
	noName := flag.Bool("no-name", false, "Do not include the --name parameter")
	noLabels := flag.Bool("l", false, "Do not include Labels tags")
	ymlMode := flag.Bool("y", false, "Output in Docker Compose YAML format")
	flag.BoolVar(ymlMode, "yml", false, "Output in Docker Compose YAML format")
	bakAll := flag.Bool("a", false, "Export all containers. Use -a -p for shell only, -a -y for yml only, -a for both")
	outDir := flag.String("o", ".", "Output directory for -a mode (auto-created if not exists)")
	cleanLogs := flag.Bool("c", false, "Clean all containers' json.log files (truncate to 0)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: runlike [OPTIONS] <container name>\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to Docker: %v\n", err)
		os.Exit(1)
	}

	if *bakAll {
		exportAllContainers(ctx, cli, *noName, *noLabels, *ymlMode, *pretty, *outDir)
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
		fmt.Fprintf(os.Stderr, "❌ Container not found: %v\n", err)
		os.Exit(1)
	}

	imgEnvs, imgExposed, imgWorkDir := inspectImageDefaults(ctx, cli, containerJSON.Image)
	containerName := strings.TrimPrefix(containerJSON.Name, "/")

	if *ymlMode {
		fmt.Println(buildCompose(&containerJSON, containerName, imgEnvs, imgExposed, imgWorkDir, *noLabels))
	} else {
		fmt.Println(buildShell(&containerJSON, containerName, imgEnvs, imgExposed, imgWorkDir, *noName, *noLabels, *pretty))
	}
}

func inspectImageDefaults(ctx context.Context, cli *client.Client, imageID string) (map[string]bool, map[string]bool, string) {
	imgEnvs := make(map[string]bool)
	imgExposed := make(map[string]bool)
	var imgWorkDir string

	image, _, _ := cli.ImageInspectWithRaw(ctx, imageID)
	if image.Config != nil {
		for _, e := range image.Config.Env {
			imgEnvs[e] = true
		}
		for p := range image.Config.ExposedPorts {
			imgExposed[string(p)] = true
		}
		imgWorkDir = image.Config.WorkingDir
	}
	return imgEnvs, imgExposed, imgWorkDir
}

func buildShell(json *types.ContainerJSON, name string, imgEnvs map[string]bool, imgExposed map[string]bool, imgWorkDir string, noName, noLabels, pretty bool) string {
	var p []string

	mode := "-"
	if !json.Config.AttachStdout && !json.Config.AttachStderr {
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

	p = append(p, fmt.Sprintf("--hostname=%s", json.Config.Hostname))
	if json.HostConfig.NetworkMode != "default" {
		p = append(p, fmt.Sprintf("--network=%s", json.HostConfig.NetworkMode))
	}

	for _, dns := range json.HostConfig.DNS {
		p = append(p, fmt.Sprintf("--dns=%s", dns))
	}
	for _, link := range json.HostConfig.Links {
		parts := strings.Split(link, ":")
		src := strings.TrimPrefix(parts[0], "/")
		alias := strings.Split(parts[1], "/")[2]
		p = append(p, fmt.Sprintf("--link %s:%s", src, alias))
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

	for _, m := range json.Mounts {
		p = append(p, fmt.Sprintf("-v %s:%s", m.Source, m.Destination))
	}
	if json.HostConfig.ShmSize > 0 && json.HostConfig.ShmSize != 67108864 {
		p = append(p, fmt.Sprintf("--shm-size=%d", json.HostConfig.ShmSize))
	}
	for key, val := range json.HostConfig.Sysctls {
		p = append(p, fmt.Sprintf("--sysctl %s=%s", key, val))
	}

	if json.Config.WorkingDir != "" && json.Config.WorkingDir != imgWorkDir {
		p = append(p, fmt.Sprintf("--workdir=%s", json.Config.WorkingDir))
	}
	for _, env := range json.Config.Env {
		if !imgEnvs[env] {
			p = append(p, fmt.Sprintf("--env=\"%s\"", env))
		}
	}

	if json.HostConfig.RestartPolicy.Name != "" {
		p = append(p, fmt.Sprintf("--restart=%s", json.HostConfig.RestartPolicy.Name))
	}
	for key, val := range json.HostConfig.LogConfig.Config {
		p = append(p, fmt.Sprintf("--log-opt %s=%s", key, val))
	}
	for _, dev := range json.HostConfig.Resources.Devices {
		p = append(p, fmt.Sprintf("--device %s:%s", dev.PathOnHost, dev.PathInContainer))
	}

	if !noLabels {
		for k, v := range json.Config.Labels {
			p = append(p, fmt.Sprintf("--label='%s=%s'", k, v))
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

func buildCompose(json *types.ContainerJSON, name string, imgEnvs map[string]bool, imgExposed map[string]bool, imgWorkDir string, noLabels bool) string {
	var b strings.Builder

	b.WriteString("services:\n")
	fmt.Fprintf(&b, "  %s:\n", name)
	fmt.Fprintf(&b, "    image: %s\n", json.Config.Image)
	fmt.Fprintf(&b, "    container_name: %s\n", name)

	if len(json.NetworkSettings.Networks) > 0 {
		b.WriteString("    networks:\n")
		for netName := range json.NetworkSettings.Networks {
			fmt.Fprintf(&b, "      - %s\n", netName)
		}
	}

	if json.Config.Hostname != "" {
		fmt.Fprintf(&b, "    hostname: %s\n", json.Config.Hostname)
	}
	if json.HostConfig.NetworkMode != "default" {
		fmt.Fprintf(&b, "    network_mode: %s\n", json.HostConfig.NetworkMode)
	}

	if len(json.HostConfig.DNS) > 0 {
		b.WriteString("    dns:\n")
		for _, d := range json.HostConfig.DNS {
			fmt.Fprintf(&b, "      - %s\n", d)
		}
	}
	if len(json.HostConfig.ExtraHosts) > 0 {
		b.WriteString("    extra_hosts:\n")
		for _, h := range json.HostConfig.ExtraHosts {
			fmt.Fprintf(&b, "      - \"%s\"\n", h)
		}
	}

	published := make(map[string]bool)
	for p := range json.HostConfig.PortBindings {
		published[string(p)] = true
	}
	var exPorts []string
	for p := range json.Config.ExposedPorts {
		if !imgExposed[string(p)] && !published[string(p)] {
			exPorts = append(exPorts, string(p))
		}
	}
	if len(exPorts) > 0 {
		b.WriteString("    expose:\n")
		for _, p := range exPorts {
			fmt.Fprintf(&b, "      - \"%s\"\n", p)
		}
	}
	if len(json.HostConfig.PortBindings) > 0 {
		b.WriteString("    ports:\n")
		for p, bindings := range json.HostConfig.PortBindings {
			fmt.Fprintf(&b, "      - \"%s:%s\"\n", bindings[0].HostPort, p)
		}
	}

	if json.Config.Tty {
		b.WriteString("    tty: true\n")
	}
	if json.Config.OpenStdin {
		b.WriteString("    stdin_open: true\n")
	}
	if json.HostConfig.Privileged {
		b.WriteString("    privileged: true\n")
	}
	if json.HostConfig.RestartPolicy.Name != "" {
		fmt.Fprintf(&b, "    restart: %s\n", json.HostConfig.RestartPolicy.Name)
	}

	if len(json.Mounts) > 0 {
		b.WriteString("    volumes:\n")
		for _, m := range json.Mounts {
			fmt.Fprintf(&b, "      - %s:%s\n", m.Source, m.Destination)
		}
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

	if len(json.HostConfig.LogConfig.Config) > 0 {
		b.WriteString("    logging:\n")
		fmt.Fprintf(&b, "      driver: \"%s\"\n", json.HostConfig.LogConfig.Type)
		b.WriteString("      options:\n")
		for k, v := range json.HostConfig.LogConfig.Config {
			fmt.Fprintf(&b, "        %s: \"%s\"\n", k, v)
		}
	}
	if len(json.HostConfig.Sysctls) > 0 {
		b.WriteString("    sysctls:\n")
		for k, v := range json.HostConfig.Sysctls {
			fmt.Fprintf(&b, "      %s: %s\n", k, v)
		}
	}

	if !noLabels && len(json.Config.Labels) > 0 {
		b.WriteString("    labels:\n")
		for k, v := range json.Config.Labels {
			fmt.Fprintf(&b, "      %s: \"%s\"\n", k, v)
		}
	}

	if len(json.Config.Cmd) > 0 {
		fmt.Fprintf(&b, "    command: %s\n", strings.Join(json.Config.Cmd, " "))
	}

	if len(json.NetworkSettings.Networks) > 0 {
		b.WriteString("\nnetworks:\n")
		for netName := range json.NetworkSettings.Networks {
			fmt.Fprintf(&b, "  %s:\n", netName)
			b.WriteString("    external: true\n")
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

func cleanDockerLogs(ctx context.Context, cli *client.Client) {
	if !isRoot() {
		fmt.Println("🔒 Root permission required, requesting sudo...")
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
		fmt.Fprintf(os.Stderr, "❌ Failed to get Docker info: %v\n", err)
		os.Exit(1)
	}
	dockerRoot := info.DockerRootDir

	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to list containers: %v\n", err)
		os.Exit(1)
	}

	if len(containers) == 0 {
		fmt.Println("No containers found.")
		return
	}

	fmt.Printf("🔍 Docker Root Dir: %s\n", dockerRoot)
	fmt.Printf("📊 Found %d containers\n\n", len(containers))

	cleaned := 0
	for i, c := range containers {
		name := strings.TrimPrefix(c.Names[0], "/")
		id := c.ID
		logPath := filepath.Join(dockerRoot, "containers", id, id+"-json.log")

		sizeStr := "?"
		if fi, err := os.Stat(logPath); err == nil {
			sizeStr = humanSize(fi.Size())
		} else if os.IsNotExist(err) {
			continue
		}

		fmt.Printf("  %d. %-30s  id: %-12s  log: %s\n", i+1, name, id[:12], sizeStr)

		if err := os.Truncate(logPath, 0); err != nil {
			fmt.Fprintf(os.Stderr, "     ❌ clean failed: %v\n", err)
			continue
		}
		fmt.Printf("     ✅ %s\n", logPath)
		cleaned++
	}

	fmt.Printf("\n✅ Done! %d log files cleaned\n", cleaned)
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

func exportAllContainers(ctx context.Context, cli *client.Client, noName, noLabels, ymlMode, pretty bool, outDir string) {
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to list containers: %v\n", err)
		os.Exit(1)
	}

	if len(containers) == 0 {
		fmt.Println("No containers found.")
		return
	}

	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to create output directory %s: %v\n", outDir, err)
		os.Exit(1)
	}

	shellOnly := pretty && !ymlMode
	ymlOnly := ymlMode && !pretty

	if !ymlOnly {
		shellAllFile, err := os.Create(filepath.Join(outDir, "docker_run_shell.txt"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to create docker_run_shell.txt: %v\n", err)
			os.Exit(1)
		}
		defer shellAllFile.Close()

		for _, c := range containers {
			name := strings.TrimPrefix(c.Names[0], "/")
			containerJSON, err := cli.ContainerInspect(ctx, c.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Skip %s: inspect failed: %v\n", name, err)
				continue
			}
			imgEnvs, imgExposed, imgWorkDir := inspectImageDefaults(ctx, cli, containerJSON.Image)
			shellCmd := buildShell(&containerJSON, name, imgEnvs, imgExposed, imgWorkDir, noName, noLabels, true)
			fmt.Fprintf(shellAllFile, "# %s\n%s\n\n", name, shellCmd)
		}
		fmt.Printf("✅ %s/docker_run_shell.txt\n", outDir)
	}

	if !shellOnly {
		for _, c := range containers {
			name := strings.TrimPrefix(c.Names[0], "/")
			containerJSON, err := cli.ContainerInspect(ctx, c.ID)
			if err != nil {
				continue
			}
			imgEnvs, imgExposed, imgWorkDir := inspectImageDefaults(ctx, cli, containerJSON.Image)
			ymlContent := buildCompose(&containerJSON, name, imgEnvs, imgExposed, imgWorkDir, noLabels)

			fileName := safeFileName(name)
			perYmlFile, err := os.Create(filepath.Join(outDir, fileName+".yml"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Skip %s: create .yml failed: %v\n", name, err)
				continue
			}
			perYmlFile.WriteString(ymlContent)
			perYmlFile.Close()

			fmt.Printf("✅ %s -> %s/%s.yml\n", name, outDir, fileName)
		}
	}

	fmt.Printf("\n✅ Done! %d containers exported\n", len(containers))
}
