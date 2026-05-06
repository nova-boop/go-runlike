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
	showLabels := flag.Bool("l", false, "Include Labels tags (hidden by default)")
	ymlMode := flag.Bool("y", false, "Output in Docker Compose YAML format")
	bakAll := flag.Bool("a", false, "Export all containers. Use -a -p for shell, -a -y for yml")
	outDir := flag.String("o", ".", "Output directory for -a mode")
	cleanLogs := flag.Bool("c", false, "Clean all containers' json.log files")

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
		fmt.Fprintf(os.Stderr, "❌ Container not found: %v\n", err)
		os.Exit(1)
	}

	imgEnvs, imgExposed, imgWorkDir := inspectImageDefaults(ctx, cli, containerJSON.Image)
	containerName := strings.TrimPrefix(containerJSON.Name, "/")

	if *ymlMode {
		fmt.Println(buildCompose(&containerJSON, containerName, imgEnvs, imgExposed, imgWorkDir, *showLabels))
	} else {
		fmt.Println(buildShell(&containerJSON, containerName, imgEnvs, imgExposed, imgWorkDir, *noName, *showLabels, *pretty))
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

func buildShell(json *types.ContainerJSON, name string, imgEnvs map[string]bool, imgExposed map[string]bool, imgWorkDir string, noName, showLabels, pretty bool) string {
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

	netMode := string(json.HostConfig.NetworkMode)
	if netMode != "default" && netMode != "" {
		p = append(p, fmt.Sprintf("--network=%s", netMode))
	}

	for _, dns := range json.HostConfig.DNS {
		p = append(p, fmt.Sprintf("--dns=%s", dns))
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

	for _, m := range json.Mounts {
		p = append(p, fmt.Sprintf("-v %s:%s", m.Source, m.Destination))
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

	if showLabels {
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

func buildCompose(json *types.ContainerJSON, name string, imgEnvs map[string]bool, imgExposed map[string]bool, imgWorkDir string, showLabels bool) string {
	var b strings.Builder

	b.WriteString("services:\n")
	fmt.Fprintf(&b, "  %s:\n", name)
	fmt.Fprintf(&b, "    image: %s\n", json.Config.Image)
	fmt.Fprintf(&b, "    container_name: %s\n", name)

	netMode := string(json.HostConfig.NetworkMode)
	isSpecialNet := netMode == "host" || netMode == "none" || strings.HasPrefix(netMode, "container:")

	if isSpecialNet {
		fmt.Fprintf(&b, "    network_mode: %s\n", netMode)
	} else {
		var customNets []string
		for netName := range json.NetworkSettings.Networks {
			if netName != "bridge" && netName != "default" {
				customNets = append(customNets, netName)
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

	if showLabels && len(json.Config.Labels) > 0 {
		b.WriteString("    labels:\n")
		for k, v := range json.Config.Labels {
			fmt.Fprintf(&b, "      %s: \"%s\"\n", k, v)
		}
	}

	if len(json.Config.Cmd) > 0 {
		fmt.Fprintf(&b, "    command: %s\n", strings.Join(json.Config.Cmd, " "))
	}

	// 外部网络声明
	if !isSpecialNet {
		hasCustomNet := false
		for netName := range json.NetworkSettings.Networks {
			if netName != "bridge" && netName != "default" {
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

	cleaned := 0
	for _, c := range containers {
		id := c.ID
		logPath := filepath.Join(dockerRoot, "containers", id, id+"-json.log")
		if err := os.Truncate(logPath, 0); err == nil {
			cleaned++
		}
	}
	fmt.Printf("\n✅ Done! %d log files cleaned\n", cleaned)
}

func exportAllContainers(ctx context.Context, cli *client.Client, noName, showLabels, ymlMode, pretty bool, outDir string) {
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to list containers: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(outDir, 0755); err != nil {
		os.Exit(1)
	}

	for _, c := range containers {
		name := strings.TrimPrefix(c.Names[0], "/")
		containerJSON, err := cli.ContainerInspect(ctx, c.ID)
		if err != nil {
			continue
		}
		imgEnvs, imgExposed, imgWorkDir := inspectImageDefaults(ctx, cli, containerJSON.Image)

		if ymlMode {
			content := buildCompose(&containerJSON, name, imgEnvs, imgExposed, imgWorkDir, showLabels)
			os.WriteFile(filepath.Join(outDir, safeFileName(name)+".yml"), []byte(content), 0644)
		} else {
			content := buildShell(&containerJSON, name, imgEnvs, imgExposed, imgWorkDir, noName, showLabels, pretty)
			fmt.Println(content)
		}
	}
}
