package main

import (
	"bingyan-netdisk/internal/backup"
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func run() error {
	var cfg backup.Config
	mode := flag.String("mode", "upload", "upload, download, status, archive, restore, or restore-all")
	initDisk := flag.Bool("init-disk", false, "bind a new empty download destination")
	adoptDisk := flag.Bool("adopt-disk", false, "recover an existing backup disk with a new local binding")
	diskLabel := flag.String("disk-label", "", "human-readable label for a new backup disk")
	fileID := flag.String("file-id", "", "one backed-up file ID for archive/restore")
	revision := flag.String("revision", "", "specific local manifest version key to restore")
	reserveBytes := flag.Int64("reserve-bytes", 16<<20, "free space to preserve on the backup disk")
	restoreElsewhere := flag.Bool("restore-to-other-server", false, "explicitly restore files to a new server/account; disables download and archive")
	server := flag.String("server", "http://127.0.0.1:38125", "NetDisk HTTP(S) origin")
	username := flag.String("username", "", "NetDisk username")
	once := flag.Bool("once", false, "scan once; exit nonzero if files fail or remain paused")
	stdinPassword := flag.Bool("password-stdin", false, "read password from standard input instead of NETDISK_SYNC_PASSWORD")
	flag.StringVar(&cfg.Root, "root", "", "explicit local source directory (required)")
	flag.StringVar(&cfg.StatePath, "state", ".tmp/netdisk-sync/state.json", "persistent state outside source directory")
	flag.StringVar(&cfg.RemoteParent, "remote-parent", "", "owned destination folder ID; empty means cloud root")
	flag.StringVar(&cfg.RemoteName, "remote-name", "", "backup folder name; default derives from source name and absolute path")
	flag.IntVar(&cfg.Concurrency, "concurrency", 2, "file workers, 1..8")
	flag.IntVar(&cfg.MaxAttempts, "max-attempts", 5, "attempts per content version across restarts, 1..20")
	flag.DurationVar(&cfg.PollInterval, "interval", 10*time.Second, "poll interval")
	flag.DurationVar(&cfg.RetryBase, "retry-delay", 30*time.Second, "initial exponential retry delay; capped at 15 minutes")
	flag.Int64Var(&cfg.PartBytes, "part-bytes", 8<<20, "resumable upload part size, at most 8 MiB")
	flag.BoolVar(&cfg.RetryFailed, "retry-failed", false, "explicitly reset incomplete file attempt budgets on startup")
	flag.Parse()
	if *restoreElsewhere && *mode != "restore" && *mode != "restore-all" {
		return errors.New("-restore-to-other-server requires restore or restore-all mode")
	}
	if *mode != "upload" && *mode != "download" && *mode != "status" && *mode != "archive" && *mode != "restore" && *mode != "restore-all" {
		return errors.New("unknown mode; see -help")
	}
	if (*mode == "archive" || *mode == "restore") && *fileID == "" {
		return errors.New("archive/restore requires one explicit -file-id")
	}
	if *mode != "restore" && *revision != "" {
		return errors.New("-revision is only valid for restore")
	}
	if *mode == "upload" && (*initDisk || *adoptDisk || *fileID != "" || *diskLabel != "") {
		return errors.New("disk options require download/archive/restore mode")
	}
	if *mode != "download" && *initDisk {
		return errors.New("initialize disks using download mode")
	}
	if *mode != "upload" {
		stateSet, intervalSet := false, false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "state" {
				stateSet = true
			}
			if f.Name == "interval" {
				intervalSet = true
			}
		})
		if !stateSet {
			cfg.StatePath = ".tmp/netdisk-pull/binding.json"
		}
		if !intervalSet {
			cfg.PollInterval = time.Minute
		}
	}
	if *username == "" || cfg.Root == "" || flag.NArg() != 0 {
		return errors.New("-username and -root are required; see -help")
	}
	password := os.Getenv("NETDISK_SYNC_PASSWORD")
	_ = os.Unsetenv("NETDISK_SYNC_PASSWORD")
	if *stdinPassword {
		line, err := bufio.NewReader(io.LimitReader(os.Stdin, 4096)).ReadString('\n')
		if err != nil && len(line) == 0 {
			return errors.New("password input unavailable")
		}
		password = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	}
	if password == "" {
		return errors.New("set NETDISK_SYNC_PASSWORD or use -password-stdin; never put the password in command arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, err := backup.NewClient(ctx, *server, *username, password)
	password = ""
	if err != nil {
		return err
	}
	cfg.Log = func(event backup.Event) { fmt.Printf("%q: %s\n", event.Path, event.Status) }
	if *mode != "upload" {
		pull, err := backup.NewPull(backup.PullConfig{Root: cfg.Root, StatePath: cfg.StatePath, DiskLabel: *diskLabel, InitDisk: *initDisk, AdoptDisk: *adoptDisk, RetryFailed: cfg.RetryFailed, PartBytes: cfg.PartBytes, ReserveBytes: *reserveBytes, MaxAttempts: cfg.MaxAttempts, PollInterval: cfg.PollInterval, RetryBase: cfg.RetryBase, Log: cfg.Log, RestoreToOtherServer: *restoreElsewhere}, client)
		if err != nil {
			return err
		}
		defer pull.Close()
		switch *mode {
		case "status":
			items, err := pull.Status()
			if err != nil {
				return err
			}
			for _, v := range items {
				fmt.Printf("%s  verified=%t archived=%t restored=%t  %q\n", v.File.ID, v.Done, v.ArchivedAt > 0, v.RestoredFileID != "", v.File.Name)
			}
			return nil
		case "archive":
			v, err := pull.Archive(ctx, *fileID)
			if err != nil {
				return err
			}
			fmt.Printf("Archived file %s to disk %q; cleanup_pending=%t. Shared content may remain on the server.\n", v.File.ID, v.DiskLabel, v.CleanupPending)
			return nil
		case "restore":
			v, err := pull.Restore(ctx, *fileID, *revision)
			if err != nil {
				return err
			}
			fmt.Printf("Restored file %s.\n", v.ID)
			return nil
		case "restore-all":
			return pull.RestoreAll(ctx)
		default:
			if *once {
				r, err := pull.SyncOnce(ctx)
				fmt.Printf("found=%d downloaded=%d skipped=%d failed=%d paused=%d\n", r.Found, r.Downloaded, r.Skipped, r.Failed, r.Paused)
				if err != nil {
					return err
				}
				if r.Failed+r.Paused > 0 {
					return errors.New("some downloads are incomplete; cloud files retained")
				}
				return nil
			}
			fmt.Println("Download backup active. Remote deletions are retained locally; disk identity is checked before writes.")
			err = pull.Run(ctx)
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
	engine, err := backup.New(cfg, client)
	if err != nil {
		return err
	}
	defer engine.Close()
	fmt.Println("One-way backup active. Cloud versions are retained; local deletions are not propagated.")
	if *once {
		result, err := engine.SyncOnce(ctx)
		fmt.Printf("found=%d uploaded=%d skipped=%d failed=%d paused=%d\n", result.Found, result.Uploaded, result.Skipped, result.Failed, result.Paused)
		if err != nil {
			return err
		}
		if result.Failed > 0 || result.Paused > 0 {
			return errors.New("some files remain incomplete; inspect status and retry explicitly")
		}
		return nil
	}
	err = engine.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "netdisk-sync:", err)
		os.Exit(1)
	}
}
