package restore

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zero-os/0-Disk"
	"github.com/zero-os/0-Disk/log"
	"github.com/zero-os/0-Disk/tlog/tlogclient/decoder"
	"github.com/zero-os/0-Disk/tlog/tlogclient/player"
	"github.com/zero-os/0-Disk/zeroctl/cmd/config"
)

// vdiskCfg is the configuration used for the restore vdisk command
var vdiskCmdCfg struct {
	TlogObjStorAddresses string
	RawConfigResource    string
	K, M                 int
	PrivKey, HexNonce    string
	StartTs              uint64 // start timestamp
	EndTs                uint64 // end timestamp
}

// VdiskCmd represents the restore vdisk subcommand
var VdiskCmd = &cobra.Command{
	Use:   "vdisk id",
	Short: "Restore a vdisk using a given tlogserver",
	RunE:  restoreVdisk,
}

func restoreVdisk(cmd *cobra.Command, args []string) error {
	argn := len(args)

	if argn < 1 {
		return errors.New("not enough arguments")
	}
	if argn > 1 {
		return errors.New("too many arguments")
	}

	vdiskID := args[0]

	configInfo, err := zerodisk.ParseConfigInfo(vdiskCmdCfg.RawConfigResource)
	if err != nil {
		return err
	}

	logLevel := log.ErrorLevel
	if config.Verbose {
		logLevel = log.DebugLevel
	}
	log.SetLevel(logLevel)

	ctx := context.Background()

	// parse optional server configs
	serverConfigs, err := zerodisk.ParseCSStorageServerConfigStrings(vdiskCmdCfg.TlogObjStorAddresses)
	if err != nil {
		return fmt.Errorf(
			"failed to parse given connection strings %q: %s",
			vdiskCmdCfg.TlogObjStorAddresses, err.Error())
	}

	player, err := player.NewPlayer(ctx, *configInfo, serverConfigs, vdiskID,
		vdiskCmdCfg.PrivKey, vdiskCmdCfg.HexNonce, vdiskCmdCfg.K, vdiskCmdCfg.M)
	if err != nil {
		return err
	}

	_, err = player.Replay(decoder.NewLimitByTimestamp(vdiskCmdCfg.StartTs, vdiskCmdCfg.EndTs))
	return err
}

func init() {
	VdiskCmd.Flags().StringVar(
		&vdiskCmdCfg.TlogObjStorAddresses,
		"storage-addresses", "",
		"comma seperated list of redis compatible connectionstrings (format: '<ip>:<port>[@<db>]', eg: 'localhost:16379,localhost:6379@2'), if given, these are used for all vdisks, ignoring the given config")
	VdiskCmd.Flags().StringVar(
		&vdiskCmdCfg.RawConfigResource, "config", "config.yml",
		"config resource: etcd (dialstring(s)) or file (path)")
	VdiskCmd.Flags().IntVar(
		&vdiskCmdCfg.K,
		"k", 4,
		"K variable of erasure encoding")
	VdiskCmd.Flags().IntVar(
		&vdiskCmdCfg.M,
		"m", 2,
		"M variable of erasure encoding")
	VdiskCmd.Flags().StringVar(
		&vdiskCmdCfg.PrivKey,
		"priv-key", "12345678901234567890123456789012",
		"private key")
	VdiskCmd.Flags().StringVar(
		&vdiskCmdCfg.HexNonce,
		"nonce", "37b8e8a308c354048d245f6d",
		"hex nonce used for encryption")
	VdiskCmd.Flags().Uint64Var(
		&vdiskCmdCfg.StartTs,
		"start-timestamp", 0,
		"start timestamp in nanosecond(default 0: since beginning)")

	VdiskCmd.Flags().Uint64Var(
		&vdiskCmdCfg.EndTs,
		"end-timestamp", 0,
		"end timestamp in nanosecond(default 0: until the end)")
}
