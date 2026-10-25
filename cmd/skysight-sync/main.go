package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ump45nose/skysight-sync/internal/syncer"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	global := flag.NewFlagSet("skysight-sync", flag.ContinueOnError)
	configPath := global.String("config", filepath.Join(codex, "skysight-sync", "config.json"), "configuration file")
	if err = global.Parse(os.Args[1:]); err != nil {
		return err
	}
	args := global.Args()
	if len(args) == 0 {
		return fmt.Errorf("usage: skysight-sync [--config PATH] init|sync|status|install-service|uninstall-service|restore-native|version")
	}
	if args[0] == "version" {
		fmt.Println(syncer.Version)
		return nil
	}
	if args[0] == "init" {
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		device := fs.String("device", "", "stable device ID: macair or macpro")
		ssh := fs.String("ssh-target", "", "existing SSH alias or user@host")
		host := fs.String("ssh-hostname", "", "optional hostname override for the SSH alias")
		remote := fs.String("remote-dir", "", "absolute remote data directory (required for a new installation)")
		ch := fs.String("codex-home", codex, "existing CODEX_HOME")
		devices := fs.String("devices", "macair,macpro", "participating device IDs")
		resume := fs.Bool("resume", false, "resume after restore-native")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("unexpected init arguments")
		}
		c := syncer.Config{DeviceID: *device, InstanceID: syncer.NewID(), CodexHome: *ch, SSHTarget: *ssh, SSHHostname: *host, RemoteDir: *remote, Devices: strings.Split(*devices, ",")}
		if old, x := syncer.LoadConfig(*configPath); x == nil {
			provided := map[string]bool{}
			fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })
			if !provided["device"] {
				c.DeviceID = old.DeviceID
			}
			if !provided["ssh-target"] {
				c.SSHTarget = old.SSHTarget
			}
			if !provided["ssh-hostname"] {
				c.SSHHostname = old.SSHHostname
			}
			if !provided["remote-dir"] {
				c.RemoteDir = old.RemoteDir
			}
			if !provided["codex-home"] {
				c.CodexHome = old.CodexHome
			}
			if !provided["devices"] {
				c.Devices = old.Devices
			}
			c.InstanceID = old.InstanceID
		}
		if c.RemoteDir == "" {
			return fmt.Errorf("init requires --remote-dir with the absolute SFTP data directory")
		}
		return syncer.Initialize(*configPath, c, *resume)
	}
	c, err := syncer.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	engine := &syncer.Engine{Config: c, Root: filepath.Dir(*configPath)}
	switch args[0] {
	case "sync":
		fs := flag.NewFlagSet("sync", flag.ContinueOnError)
		dry := fs.Bool("dry-run", false, "show changes without writing locally or remotely")
		scheduled := fs.Bool("scheduled", false, "background retries and failure notification")
		archiveOnly := fs.Bool("archive-only", false, "transfer immutable archives without rewriting or retiring native summaries")
		force := fs.Bool("force", false, "check for updates even after a successful daily publication")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("unexpected sync arguments")
		}
		if *dry && *scheduled {
			return fmt.Errorf("dry-run and scheduled are mutually exclusive")
		}
		engine.ArchiveOnly, engine.Force = *archiveOnly, *force
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		attempts := 1
		if *scheduled {
			attempts = 4
		}
		for attempt := 0; attempt < attempts; attempt++ {
			if attempt > 0 {
				timer := time.NewTimer(15 * time.Minute)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
			attemptCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			report, x := engine.Run(attemptCtx, *dry)
			cancel()
			err = x
			if err == nil {
				for _, issue := range report.Issues {
					fmt.Fprintf(os.Stderr, "%s summary issue: %s/%s: %s\n", time.Now().UTC().Format(time.RFC3339), issue.Device, issue.Filename, issue.Reason)
				}
				if !*scheduled {
					return printJSON(report)
				}
				fmt.Printf("%s archive sync succeeded: device=%s cutoff=%s upload=%d download=%d issues=%d\n", time.Now().UTC().Format(time.RFC3339), report.Device, report.Cutoff.Format(time.RFC3339), len(report.Upload), len(report.Download), len(report.Issues))
				return nil
			}
			fmt.Fprintf(os.Stderr, "%s sync failed: %v\n", time.Now().UTC().Format(time.RFC3339), err)
			if !*scheduled || !syncer.Retryable(err) {
				break
			}
		}
		if *scheduled {
			if x := engine.NotifyFailure(); x != nil {
				fmt.Fprintln(os.Stderr, x)
			}
		}
		return err
	case "status":
		fs := flag.NewFlagSet("status", flag.ContinueOnError)
		jsonMode := fs.Bool("json", false, "JSON status")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		status, x := engine.Status()
		if x != nil {
			return x
		}
		if *jsonMode {
			return printJSON(status)
		}
		fmt.Printf("设备：%s\n版本：%s\n", status.Device, status.Version)
		if status.LastSuccess != nil {
			fmt.Printf("最近成功：%s\n", status.LastSuccess.Format(time.RFC3339))
		} else {
			fmt.Println("最近成功：尚未完成")
		}
		fmt.Printf("同步截止：%s\n展示窗口：%d；本机删除：%d\n", status.Cutoff.Format(time.RFC3339), status.Windows, status.Suppressed)
		for _, source := range status.Sources {
			fmt.Printf("来源 %s：%d 份，截止 %s\n", source.Device, source.Records, source.Cutoff.Format(time.RFC3339))
		}
		if status.PendingPublication {
			fmt.Println("待办：完成上次中断的发布")
		}
		if status.Restored {
			fmt.Println("状态：已恢复原生历史，同步已停用")
		}
		if status.Error != "" {
			fmt.Printf("错误：%s\n", status.Error)
		}
		return nil
	case "install-service":
		return syncer.InstallService(*configPath)
	case "uninstall-service":
		return syncer.UninstallService(*configPath)
	case "restore-native":
		if err = syncer.UninstallService(*configPath); err != nil {
			return err
		}
		return engine.Restore()
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}
func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
