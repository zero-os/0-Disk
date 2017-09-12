package generator

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"gopkg.in/validator.v2"

	"github.com/zero-os/0-Disk/config"
	"github.com/zero-os/0-Disk/log"
	"github.com/zero-os/0-Disk/nbd/ardb"
	"github.com/zero-os/0-Disk/nbd/ardb/storage"
	"github.com/zero-os/0-Disk/tlog"
	"github.com/zero-os/0-Disk/tlog/flusher"
	"github.com/zero-os/0-Disk/tlog/schema"
)

// Config represent generator config
type Config struct {
	SourceVdiskID string `validate:"nonzero"`
	TargetVdiskID string `validate:"nonzero"`
	PrivKey       string `validate:"nonzero"`
	DataShards    int    `validate:"nonzero,min=1"`
	ParityShards  int    `validate:"nonzero,min=1"`
}

// Generator represents a tlog data generator/copier
type Generator struct {
	sourceVdiskID string
	flusher       *flusher.Flusher
	configSource  config.Source
}

// New creates new Generator
func New(configSource config.Source, conf Config) (*Generator, error) {
	if err := validator.Validate(conf); err != nil {
		return nil, err
	}

	flusher, err := flusher.New(configSource, conf.DataShards, conf.ParityShards, conf.TargetVdiskID, conf.PrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create flusher: %v", err)
	}
	return &Generator{
		sourceVdiskID: conf.SourceVdiskID,
		flusher:       flusher,
		configSource:  configSource,
	}, nil
}

// GenerateFromStorage generates tlog data from block storage
func (g *Generator) GenerateFromStorage() error {
	staticConf, err := config.ReadVdiskStaticConfig(g.configSource, g.sourceVdiskID)
	if err != nil {
		return err
	}

	storageConf, err := config.ReadNBDStorageConfig(g.configSource, g.sourceVdiskID, staticConf)
	if err != nil {
		return fmt.Errorf("failed to ReadNBDStorageConfig: %v", err)
	}

	indices, err := storage.ListBlockIndices(g.sourceVdiskID, staticConf.Type, &storageConf.StorageCluster)
	if err != nil {
		return fmt.Errorf("ListBlockIndices failed for vdisk `%v`: %v", g.sourceVdiskID, err)
	}

	ardbProv, err := ardb.StaticProvider(*storageConf, nil)
	if err != nil {
		return err
	}

	sourceStorage, err := storage.NewBlockStorage(storage.BlockStorageConfig{
		VdiskID:         g.sourceVdiskID,
		TemplateVdiskID: staticConf.TemplateVdiskID,
		VdiskType:       staticConf.Type,
		BlockSize:       int64(staticConf.BlockSize),
	}, ardbProv)
	if err != nil {
		return err
	}
	defer sourceStorage.Close()

	type idxContent struct {
		idx     int64
		content []byte
	}
	var (
		wg              sync.WaitGroup
		numProcess      = runtime.NumCPU()
		indicesCh       = make(chan int64, numProcess)
		idxContentCh    = make(chan idxContent, numProcess)
		errCh           = make(chan error)
		doneCh          = make(chan struct{})
		ctx, cancelFunc = context.WithCancel(context.Background())
	)
	defer cancelFunc()

	// produces the indices we want to fetch
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, idx := range indices {
			select {
			case <-ctx.Done():
				return
			case indicesCh <- idx:
			}
		}
		close(indicesCh)
	}()

	// fetch the indices
	for i := 0; i < numProcess; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range indicesCh {
				select {
				case <-ctx.Done():
					return
				default:
					content, err := sourceStorage.GetBlock(idx)
					if err != nil {
						errCh <- err
						return
					}
					idxContentCh <- idxContent{
						idx:     idx,
						content: content,
					}

				}
			}
		}()
	}

	// add to flusher
	var seq uint64
	wg.Add(1)
	go func() {
		defer wg.Done()

		for ic := range idxContentCh {
			select {
			case <-ctx.Done():
				return
			default:
				err = g.flusher.AddTransaction(schema.OpSet, seq, ic.content, ic.idx, tlog.TimeNowTimestamp())
				if err != nil {
					errCh <- err
					return
				}
				seq++
				if int(seq) == len(indices) {
					return
				}
			}
		}
	}()

	go func() {
		wg.Wait()
		doneCh <- struct{}{}
	}()

	select {
	case err := <-errCh:
		return err
	case <-doneCh:
		// all is good
	}

	_, err = g.flusher.Flush()
	log.Infof("GenerateFromStorage generates `%v` tlog data with err = %v", len(indices), err)
	return err
}

// CopyTlogData copy/fork tlog data
func (g *Generator) CopyTlogData() error {
	return nil
}
