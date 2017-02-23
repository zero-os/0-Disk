package main

//NumberOfRecordsPerLBAShard is the fixed length of the LBAShards
const NumberOfRecordsPerLBAShard = 128

//LBAShard is a collection of 128 LBA Records (Hash)
type LBAShard [NumberOfRecordsPerLBAShard]Hash

//NewLBAShard initializes a new LBAShard an returns a pointer to it
func NewLBAShard() *LBAShard {
	return &LBAShard{}
}

//An LBA implements the functionality to lookup block keys through the logical block index
type LBA []LBAShard

//NewLBA creates a new LBA with enough shards to hold the requested numberOfBlocks
// TODO: this is a naive in memory implementation so we can continue testing
func NewLBA(numberOfBlocks uint64) (lba *LBA) {
	numberOfShards := numberOfBlocks / NumberOfRecordsPerLBAShard
	//If the number of blocks is not aligned on the number of shards, add an extra one
	if (numberOfBlocks % NumberOfRecordsPerLBAShard) != 0 {
		numberOfShards++
	}
	l := LBA(make([]LBAShard, numberOfShards, numberOfShards))
	lba = &l
	return
}