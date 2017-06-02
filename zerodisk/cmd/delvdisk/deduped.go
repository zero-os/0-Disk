package delvdisk

import (
	"fmt"

	"github.com/garyburd/redigo/redis"
	"github.com/spf13/cobra"
	"github.com/zero-os/0-Disk/config"
	"github.com/zero-os/0-Disk/log"
	cmdconfig "github.com/zero-os/0-Disk/zerodisk/cmd/config"
)

// DedupedCmd represents the deduped delete subcommand
var DedupedCmd = &cobra.Command{
	Use:   "deduped vdiskid ardb_url",
	Short: "Delete the metadata of a deduped vdisk",
	RunE:  deleteDeduped,
}

func deleteDeduped(cmd *cobra.Command, args []string) error {
	// create logger
	logLevel := log.ErrorLevel
	if cmdconfig.Verbose {
		logLevel = log.InfoLevel
	}
	log.SetLevel(logLevel)

	// parse user input
	log.Info("parsing positional arguments...")
	input, err := parseUserInput(args)
	if err != nil {
		return err
	}

	storageServer := config.StorageServerConfig{
		Address:  input.URL,
		Database: 0,
	}
	return deleleDedupedVdisksMetadata(false, storageServer, input.VdiskID)
}

// delete the metadata of deduped vdisks
func deleleDedupedVdisksMetadata(force bool, cfg config.StorageServerConfig, vdiskids ...string) error {
	if len(vdiskids) == 0 {
		return nil
	}

	// open redis connection
	log.Infof("dialing redis TCP connection at: %s (%d)", cfg.Address, cfg.Database)
	conn, err := redis.Dial("tcp", cfg.Address, redis.DialDatabase(cfg.Database))
	if err != nil {
		return err
	}
	defer conn.Close()

	// cache delete request of each vdisk
	var delVdisks []string
	for _, vdiskID := range vdiskids {
		log.Infof("deleting metadata of vdisk %s...", vdiskID)
		err := conn.Send("DEL", vdiskID)
		if err != nil {
			if !force {
				return err
			}
			log.Error("could not delete metadata of deduped vdisk: ", vdiskID)
			continue
		}
		delVdisks = append(delVdisks, vdiskID)
	}

	// flush all delete requests
	err = conn.Flush()
	if err != nil {
		return fmt.Errorf("could not delete metadata of deduped vdisks %v: %s", delVdisks, err.Error())
	}

	// check if all vdisks have actually been deleted
	for _, vdiskID := range delVdisks {
		deleted, err := redis.Bool(conn.Receive())
		if err != nil {
			if !force {
				return err
			}

			log.Errorf("could not delete metadata of deduped vdisk %s: %s", vdiskID, err.Error())
			continue
		}

		// it's not an error if it did not exist yet,
		// as this is possible due to the multiple ardbs in use
		if !deleted {
			log.Infof("could not delete metadata of deduped vdisk %s: did not exist at %s (%d)",
				vdiskID, cfg.Address, cfg.Database)
		}
	}

	return nil
}