package cloudinitexecute

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"

	log "github.com/Sirupsen/logrus"
	"github.com/coreos/coreos-cloudinit/system"
	"github.com/docker/docker/pkg/mount"
	rancherConfig "github.com/rancher/os/config"
	"github.com/rancher/os/docker"
	"github.com/rancher/os/util"
	"golang.org/x/net/context"
)

const (
	resizeStamp = "/var/lib/rancher/resizefs.done"
	sshKeyName  = "rancheros-cloud-config"
)

var (
	console    bool
	preConsole bool
	flags      *flag.FlagSet
)

func init() {
	flags = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flags.BoolVar(&console, "console", false, "apply console configuration")
	flags.BoolVar(&preConsole, "pre-console", false, "apply pre-console configuration")
}

func Main() {
	flags.Parse(os.Args[1:])

	log.Infof("Running cloud-init-execute: pre-console=%v, console=%v", preConsole, console)

	cfg := rancherConfig.LoadConfig()

	if !console && !preConsole {
		console = true
		preConsole = true
	}

	if console {
		ApplyConsole(cfg)
	}
	if preConsole {
		applyPreConsole(cfg)
	}
}

func ApplyConsole(cfg *rancherConfig.CloudConfig) {
	if len(cfg.SSHAuthorizedKeys) > 0 {
		authorizeSSHKeys("rancher", cfg.SSHAuthorizedKeys, sshKeyName)
		authorizeSSHKeys("docker", cfg.SSHAuthorizedKeys, sshKeyName)
	}

	WriteFiles(cfg, "console")

	for _, configMount := range cfg.Mounts {
		if len(configMount) != 4 {
			log.Errorf("Unable to mount %s: must specify exactly four arguments", configMount[1])
		}
		device := util.ResolveDevice(configMount[0])
		if configMount[2] == "swap" {
			cmd := exec.Command("swapon", device)
			err := cmd.Run()
			if err != nil {
				log.Errorf("Unable to swapon %s: %v", device, err)
			}
			continue
		}
		if err := mount.Mount(device, configMount[1], configMount[2], configMount[3]); err != nil {
			log.Errorf("Unable to mount %s: %v", configMount[1], err)
		}
	}
}

func WriteFiles(cfg *rancherConfig.CloudConfig, container string) {
	for _, file := range cfg.WriteFiles {
		fileContainer := file.Container
		if fileContainer == "" {
			fileContainer = "console"
		}
		if fileContainer != container {
			continue
		}

		f := system.File{
			File: file.File,
		}
		fullPath, err := system.WriteFile(&f, "/")
		if err != nil {
			log.WithFields(log.Fields{"err": err, "path": fullPath}).Error("Error writing file")
			continue
		}
		log.Printf("Wrote file %s to filesystem", fullPath)
	}
}

func applyPreConsole(cfg *rancherConfig.CloudConfig) {
	if _, err := os.Stat(resizeStamp); os.IsNotExist(err) && cfg.Rancher.ResizeDevice != "" {
		if err := resizeDevice(cfg); err == nil {
			os.Create(resizeStamp)
		} else {
			log.Errorf("Failed to resize %s: %s", cfg.Rancher.ResizeDevice, err)
		}
	}

	for k, v := range cfg.Rancher.Sysctl {
		elems := []string{"/proc", "sys"}
		elems = append(elems, strings.Split(k, ".")...)
		path := path.Join(elems...)
		if err := ioutil.WriteFile(path, []byte(v), 0644); err != nil {
			log.Errorf("Failed to set sysctl key %s: %s", k, err)
		}
	}

	client, err := docker.NewSystemClient()
	if err != nil {
		log.Error(err)
	}

	for _, restart := range cfg.Rancher.RestartServices {
		if err = client.ContainerRestart(context.Background(), restart, 10); err != nil {
			log.Error(err)
		}
	}
}

func resizeDevice(cfg *rancherConfig.CloudConfig) error {
	cmd := exec.Command("growpart", cfg.Rancher.ResizeDevice, "1")
	err := cmd.Run()
	if err != nil {
		return err
	}

	cmd = exec.Command("partprobe")
	err = cmd.Run()
	if err != nil {
		return err
	}

	cmd = exec.Command("resize2fs", fmt.Sprintf("%s1", cfg.Rancher.ResizeDevice))
	err = cmd.Run()
	if err != nil {
		return err
	}

	return nil
}
