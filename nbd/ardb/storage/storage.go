package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/garyburd/redigo/redis"
	"github.com/zero-os/0-Disk/config"
	"github.com/zero-os/0-Disk/log"
	"github.com/zero-os/0-Disk/nbd/ardb"
	"github.com/zero-os/0-Disk/nbd/ardb/command"
	"github.com/zero-os/0-Disk/nbd/ardb/storage/lba"
)

// BlockStorage defines an interface for all a block storage.
// It can be used to set, get and delete blocks.
//
// It is used by the `nbdserver.Backend` to implement the NBD Backend,
// as well as other modules, who need to manipulate the block storage for whatever reason.
type BlockStorage interface {
	SetBlock(blockIndex int64, content []byte) (err error)
	GetBlock(blockIndex int64) (content []byte, err error)
	DeleteBlock(blockIndex int64) (err error)

	Flush() (err error)
	Close() (err error)
}

// BlockStorageConfig is used when creating a block storage using the
// NewBlockStorage helper constructor.
type BlockStorageConfig struct {
	// required: ID of the vdisk
	VdiskID string
	// optional: used for nondeduped storage
	TemplateVdiskID string

	// required: type of vdisk
	VdiskType config.VdiskType

	// required: block size in bytes
	BlockSize int64

	// optional: used by (semi)deduped storage
	LBACacheLimit int64
}

// Validate this BlockStorageConfig.
func (cfg *BlockStorageConfig) Validate() error {
	if cfg == nil {
		return nil
	}

	if err := cfg.VdiskType.Validate(); err != nil {
		return err
	}

	if !config.ValidateBlockSize(cfg.BlockSize) {
		return errors.New("invalid block size size")
	}

	return nil
}

// BlockStorageFromConfigSource creates a block storage
// from the config retrieved from the given config source.
// It is the simplest way to create a BlockStorage,
// but it also has the disadvantage that
// it does not support SelfHealing or HotReloading of the used configuration.
func BlockStorageFromConfigSource(vdiskID string, cs config.Source, dialer ardb.ConnectionDialer) (BlockStorage, error) {
	// get configs from source
	vdiskConfig, err := config.ReadVdiskStaticConfig(cs, vdiskID)
	if err != nil {
		return nil, err
	}
	nbdStorageConfig, err := config.ReadNBDStorageConfig(cs, vdiskID)
	if err != nil {
		return nil, err
	}

	return BlockStorageFromConfig(vdiskID, *vdiskConfig, *nbdStorageConfig, dialer)
}

// BlockStorageFromConfig creates a block storage from the given config.
// It is the simplest way to create a BlockStorage,
// but it also has the disadvantage that
// it does not support SelfHealing or HotReloading of the used configuration.
func BlockStorageFromConfig(vdiskID string, vdiskConfig config.VdiskStaticConfig, nbdConfig config.NBDStorageConfig, dialer ardb.ConnectionDialer) (BlockStorage, error) {
	err := vdiskConfig.Validate()
	if err != nil {
		return nil, err
	}
	err = nbdConfig.Validate()
	if err != nil {
		return nil, err
	}

	// create primary cluster
	cluster, err := ardb.NewCluster(nbdConfig.StorageCluster, dialer)
	if err != nil {
		return nil, err
	}

	// create template cluster if needed
	var templateCluster ardb.StorageCluster
	if vdiskConfig.Type.TemplateSupport() && nbdConfig.TemplateStorageCluster != nil {
		templateCluster, err = ardb.NewCluster(*nbdConfig.TemplateStorageCluster, dialer)
		if err != nil {
			return nil, err
		}
	}

	// create block storage config
	cfg := BlockStorageConfig{
		VdiskID:         vdiskID,
		TemplateVdiskID: vdiskConfig.TemplateVdiskID,
		VdiskType:       vdiskConfig.Type,
		BlockSize:       int64(vdiskConfig.BlockSize),
		LBACacheLimit:   ardb.DefaultLBACacheLimit,
	}

	// try to create actual block storage
	return NewBlockStorage(cfg, cluster, templateCluster)
}

// NewBlockStorage returns the correct block storage based on the given VdiskConfig.
func NewBlockStorage(cfg BlockStorageConfig, cluster, templateCluster ardb.StorageCluster) (storage BlockStorage, err error) {
	err = cfg.Validate()
	if err != nil {
		return
	}

	vdiskType := cfg.VdiskType

	// templateCluster gets disabled,
	// if vdisk type has no template support.
	if !vdiskType.TemplateSupport() {
		templateCluster = nil
	}

	switch storageType := vdiskType.StorageType(); storageType {
	case config.StorageDeduped:
		return Deduped(
			cfg.VdiskID,
			cfg.BlockSize,
			cfg.LBACacheLimit,
			cluster,
			templateCluster)

	case config.StorageNonDeduped:
		return NonDeduped(
			cfg.VdiskID,
			cfg.TemplateVdiskID,
			cfg.BlockSize,
			cluster,
			templateCluster)

	case config.StorageSemiDeduped:
		return SemiDeduped(
			cfg.VdiskID,
			cfg.BlockSize,
			cfg.LBACacheLimit,
			cluster,
			templateCluster)

	default:
		return nil, fmt.Errorf(
			"no block storage available for %s's storage type %s",
			cfg.VdiskID, storageType)
	}
}

// VdiskExists returns true if the vdisk in question exists in the given ARDB storage cluster.
// An error is returned in case this couldn't be verified for whatever reason.
func VdiskExists(id string, t config.VdiskType, cluster ardb.StorageCluster) (bool, error) {
	switch st := t.StorageType(); st {
	case config.StorageDeduped:
		return dedupedVdiskExists(id, cluster)

	case config.StorageNonDeduped:
		return nonDedupedVdiskExists(id, cluster)

	case config.StorageSemiDeduped:
		return semiDedupedVdiskExists(id, cluster)

	default:
		return false, fmt.Errorf("%v is not a supported storage type", st)
	}
}

// DeleteVdisk returns true if the vdisk in question was deleted from the given ARDB storage cluster.
// An error is returned in case this couldn't be deleted (completely) for whatever reason.
func DeleteVdisk(id string, t config.VdiskType, cluster ardb.StorageCluster) (bool, error) {
	var err error
	var deletedTlogMetadata bool
	if t.TlogSupport() {
		command := ardb.Command(command.Delete, tlogMetadataKey(id))
		deletedTlogMetadata, err = ardb.Bool(cluster.Do(command))
		if err != nil {
			return false, err
		}
		if deletedTlogMetadata {
			log.Infof("deleted tlog metadata stored for vdisk %s on first available server", id)
		}
	}

	var deletedStorage bool
	switch st := t.StorageType(); st {
	case config.StorageDeduped:
		deletedStorage, err = deleteDedupedData(id, cluster)
	case config.StorageNonDeduped:
		deletedStorage, err = deleteNonDedupedData(id, cluster)
	case config.StorageSemiDeduped:
		deletedStorage, err = deleteSemiDedupedData(id, cluster)
	default:
		err = fmt.Errorf("%v is not a supported storage type", st)
	}

	return deletedTlogMetadata || deletedStorage, err
}

// ListVdisks scans a given storage cluster
// for available vdisks, and returns their ids.
// NOTE: this function is very slow,
//       and puts a lot of pressure on the ARDB cluster.
func ListVdisks(cluster ardb.StorageCluster) ([]string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverCh, err := cluster.ServerIterator(ctx)
	if err != nil {
		return nil, err
	}

	type serverResult struct {
		ids []string
		err error
	}
	resultCh := make(chan serverResult)

	var serverCount int
	// TODO: dereference deduped blocks as well
	// https://github.com/zero-os/0-Disk/issues/88
	var action listVdisksAction
	var reply interface{}
	for server := range serverCh {
		server := server
		go func() {
			var result serverResult
			log.Infof("listing all vdisks stored on %v", server.Config())
			reply, result.err = server.Do(action)
			if result.err == nil && reply != nil {
				result.ids = reply.([]string)
			}
			select {
			case resultCh <- result:
			case <-ctx.Done():
			}
		}()
		serverCount++
	}

	// collect the ids from all servers within the given cluster
	var ids []string
	var result serverResult
	for i := 0; i < serverCount; i++ {
		result = <-resultCh
		if result.err != nil {
			// return early, an error has occured!
			return nil, result.err
		}
		ids = append(ids, result.ids...)
	}

	if len(ids) <= 1 {
		return ids, nil // nothing to do
	}

	// sort and dedupe
	sort.Strings(ids)
	ids = dedupStrings(ids)

	return ids, nil
}

type listVdisksAction struct{}

// Do implements StorageAction.Do
func (action listVdisksAction) Do(conn ardb.Conn) (reply interface{}, err error) {
	const (
		startListCursor       = "0"
		vdiskListScriptSource = `
	local cursor = ARGV[1]

local result = redis.call("SCAN", cursor)
local batch = result[2]

local key
local type

local output = {}

for i = 1, #batch do
	key = batch[i]

	-- only add hashmaps
	type = redis.call("TYPE", key)
	type = type.ok or type
	if type == "hash" then
		table.insert(output, key)
	end
end

cursor = result[1]
table.insert(output, cursor)

return output
`
	)

	script := redis.NewScript(0, vdiskListScriptSource)
	cursor := startListCursor
	var output, vdisks []string

	// go through all available keys
	for {
		output, err = redis.Strings(script.Do(conn, cursor))
		if err != nil {
			log.Error("aborting key scan due to an error: ", err)
			break
		}

		// filter output
		filterPos := 0
		length := len(output) - 1
		var vdiskID string
		for i := 0; i < length; i++ {
			vdiskID = filterListedVdiskID(output[i])
			if vdiskID != "" {
				output[filterPos] = vdiskID
				filterPos++
			}
		}
		cursor = output[length]
		output = output[:filterPos]
		vdisks = append(vdisks, output...)
		if startListCursor == cursor {
			break
		}
	}

	return vdisks, nil
}

// Send implements StorageAction.Send
func (action listVdisksAction) Send(conn ardb.Conn) error {
	return ErrMethodNotSupported
}

// KeysModified implements StorageAction.KeysModified
func (action listVdisksAction) KeysModified() ([]string, bool) {
	return nil, false
}

// ListBlockIndices returns all indices stored for the given vdisk.
// This function returns either an error OR indices.
func ListBlockIndices(id string, t config.VdiskType, cluster ardb.StorageCluster) ([]int64, error) {
	switch st := t.StorageType(); st {
	case config.StorageDeduped:
		return listDedupedBlockIndices(id, cluster)

	case config.StorageNonDeduped:
		return listNonDedupedBlockIndices(id, cluster)

	case config.StorageSemiDeduped:
		return listSemiDedupedBlockIndices(id, cluster)

	default:
		return nil, fmt.Errorf("%v is not a supported storage type", st)
	}
}

// filterListedVdiskID only accepts keys with a known prefix,
// if no known prefix is found an empty string is returned,
// otherwise the prefix is removed and the vdiskID is returned.
func filterListedVdiskID(key string) string {
	parts := listStorageKeyPrefixRex.FindStringSubmatch(key)
	if len(parts) == 3 {
		return parts[2]
	}

	return ""
}

var listStorageKeyPrefixRex = regexp.MustCompile("^(" +
	strings.Join(listStorageKeyPrefixes, "|") +
	")(.+)$")

var listStorageKeyPrefixes = []string{
	lba.StorageKeyPrefix,
	nonDedupedStorageKeyPrefix,
}

// sortInt64s sorts a slice of int64s
func sortInt64s(s []int64) {
	if len(s) < 2 {
		return
	}
	sort.Sort(int64Slice(s))
}

// int64Slice implements the sort.Interface for a slice of int64s
type int64Slice []int64

func (s int64Slice) Len() int           { return len(s) }
func (s int64Slice) Less(i, j int) bool { return s[i] < s[j] }
func (s int64Slice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

// dedupInt64s deduplicates a given int64 slice which is already sorted.
func dedupInt64s(s []int64) []int64 {
	p := len(s) - 1
	if p <= 0 {
		return s
	}

	for i := p - 1; i >= 0; i-- {
		if s[p] != s[i] {
			p--
			s[p] = s[i]
		}
	}

	return s[p:]
}

// dedupStrings deduplicates a given string slice which is already sorted.
func dedupStrings(s []string) []string {
	p := len(s) - 1
	if p <= 0 {
		return s
	}

	for i := p - 1; i >= 0; i-- {
		if s[p] != s[i] {
			p--
			s[p] = s[i]
		}
	}

	return s[p:]
}

// a slightly expensive helper function which allows
// us to test if an interface value is nil or not
func isInterfaceValueNil(v interface{}) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Ptr && rv.IsNil()
}
