package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/moby/moby/api/types"
	"github.com/moby/moby/client"
)

func main() {
	pretty := flag.Bool("p", false, "Format output in shell mode (use backslash for line breaks)")
	noName := flag.Bool("no-name", false, "Do not include the --name parameter")
	noLabels := flag.Bool("l", false, "Do not include Labels tags")
	ymlMode := flag.Bool("y", false, "Output in Docker Compose YAML format")
	flag.BoolVar(ymlMode, "yml", false, "Output in Docker Compose YAML format")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: runlike [OPTIONS] <container name>\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		return
	}

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to Docker: %v\n", err)
		os.Exit(1)
	}

	json, err := cli.ContainerInspect(ctx, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Container not found: %v\n", err)
		os.Exit(1)
	}

	image, _, _ := cli.ImageInspectWithRaw(ctx, json.Image)
	imageEnvs := make(map[string]bool)
	imageExposedPorts := make(map[string]bool)
	var imageWorkDir string
	if image.Config != nil {
		for _, e := range image.Config.Env {
			imageEnvs[e] = true
		}
		for p := range image.Config.ExposedPorts {
			imageExposedPorts[string(p)] = true
		}
		imageWorkDir = image.Config.WorkingDir
	}

	containerName := strings.TrimPrefix(json.Name, "/")

	if *ymlMode {
		renderCompose(&json, containerName, imageEnvs, imageExposedPorts, imageWorkDir, *noLabels)
	} else {
		renderShell(&json, containerName, imageEnvs, imageExposedPorts, imageWorkDir, *noName, *noLabels, *pretty)
	}
}

// renderShell: Output the standard docker run command
func renderShell(json *types.ContainerJSON, name string, imgEnvs map[string]bool, imgExposed map[string]bool, imgWorkDir string, noName, noLabels, pretty bool) {
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
	fmt.Println(strings.Join(p, sep))
}

// renderCompose: Output modern style Compose YAML
func renderCompose(json *types.ContainerJSON, name string, imgEnvs map[string]bool, imgExposed map[string]bool, imgWorkDir string, noLabels bool) {
	fmt.Println("services:")
	fmt.Printf("  %s:\n", name)
	fmt.Printf("    image: %s\n", json.Config.Image)
	fmt.Printf("    container_name: %s\n", name)

	if len(json.NetworkSettings.Networks) > 0 {
		fmt.Println("    networks:")
		for netName := range json.NetworkSettings.Networks {
			fmt.Printf("      - %s\n", netName)
		}
	}

	if json.Config.Hostname != "" {
		fmt.Printf("    hostname: %s\n", json.Config.Hostname)
	}
	if json.HostConfig.NetworkMode != "default" {
		fmt.Printf("    network_mode: %s\n", json.HostConfig.NetworkMode)
	}

	// DNS And ExtraHosts
	if len(json.HostConfig.DNS) > 0 {
		fmt.Println("    dns:")
		for _, d := range json.HostConfig.DNS {
			fmt.Printf("      - %s\n", d)
		}
	}
	if len(json.HostConfig.ExtraHosts) > 0 {
		fmt.Println("    extra_hosts:")
		for _, h := range json.HostConfig.ExtraHosts {
			fmt.Printf("      - \"%s\"\n", h)
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
		fmt.Println("    expose:")
		for _, p := range exPorts {
			fmt.Printf("      - \"%s\"\n", p)
		}
	}
	if len(json.HostConfig.PortBindings) > 0 {
		fmt.Println("    ports:")
		for p, b := range json.HostConfig.PortBindings {
			fmt.Printf("      - \"%s:%s\"\n", b[0].HostPort, p)
		}
	}

	if json.Config.Tty {
		fmt.Println("    tty: true")
	}
	if json.Config.OpenStdin {
		fmt.Println("    stdin_open: true")
	}
	if json.HostConfig.Privileged {
		fmt.Println("    privileged: true")
	}
	if json.HostConfig.RestartPolicy.Name != "" {
		fmt.Printf("    restart: %s\n", json.HostConfig.RestartPolicy.Name)
	}

	if len(json.Mounts) > 0 {
		fmt.Println("    volumes:")
		for _, m := range json.Mounts {
			fmt.Printf("      - %s:%s\n", m.Source, m.Destination)
		}
	}

	var customEnvs []string
	for _, env := range json.Config.Env {
		if !imgEnvs[env] {
			customEnvs = append(customEnvs, env)
		}
	}
	if len(customEnvs) > 0 {
		fmt.Println("    environment:")
		for _, e := range customEnvs {
			fmt.Printf("      - %s\n", e)
		}
	}

	if len(json.HostConfig.LogConfig.Config) > 0 {
		fmt.Println("    logging:")
		fmt.Printf("      driver: \"%s\"\n", json.HostConfig.LogConfig.Type)
		fmt.Println("      options:")
		for k, v := range json.HostConfig.LogConfig.Config {
			fmt.Printf("        %s: \"%s\"\n", k, v)
		}
	}
	if len(json.HostConfig.Sysctls) > 0 {
		fmt.Println("    sysctls:")
		for k, v := range json.HostConfig.Sysctls {
			fmt.Printf("      %s: %s\n", k, v)
		}
	}

	if !noLabels && len(json.Config.Labels) > 0 {
		fmt.Println("    labels:")
		for k, v := range json.Config.Labels {
			fmt.Printf("      %s: \"%s\"\n", k, v)
		}
	}

	if len(json.Config.Cmd) > 0 {
		fmt.Printf("    command: %s\n", strings.Join(json.Config.Cmd, " "))
	}

	if len(json.NetworkSettings.Networks) > 0 {
		fmt.Println("\nnetworks:")
		for netName := range json.NetworkSettings.Networks {
			fmt.Printf("  %s:\n", netName)
			fmt.Printf("    external: true\n")
		}
	}
}
