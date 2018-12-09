package hash

type Action int

// Operations
const (
	HashGet Action = iota
	HashPut
	HashPutNew
	HashDelete
	HashFirst
	HashNext
)

const (
	BufMod = 1 << iota
	BufDisk
	BufBucket
	BufPin
)

type BufHead struct {
	Prev  *BufHead // LRU links
	Next  *BufHead // LRU links
	Ovrlf *BufHead // Overflow page buffer header
	Addr  uint32   // Address of this page
	Page  []byte   // Actual page data
	Flags byte
}

type Segment *BufHead

// HTab is memroy resident data structure
type HTab struct {
	Hdr      HashHdr                         // Header
	NSegs    int                             // Number of allocated segments
	ExSegs   int                             // Number of extra allocated
	Hash     func(interface{}, int64) uint32 // Hash function
	Flags    int                             // Flag values
	FP       int                             // File pointer
	TmpBuf   []byte                          // Temprory Buffer for BIG data
	TmpKey   []byte                          // Temprory Buffer for BIG keys
	CPage    BufHead                         // Currrent page
	CBucket  int                             // Current bucket
	Err      int                             // Error Number -- for DBM compatibility
	NewFile  int                             // Indicates if fd is backing store or not
	SaveFile int                             // Indicates whether we need to flush file at exit
	Mapp     [nCached]uint32                 // Pointers to page maps
	NMaps    int                             // Initial number of bitmaps
	NBufs    int                             // Number of buffers left to allocate
	BufHead  BufHead                         // Header of buffer lru list
	Dir      Segment                         // Hash Bucket directory
}

const nCached = 32 // number of bit maps and spare points

// HashHdr holds hash table invormation
type HashHdr struct {
	Magic     int32           // Magic NO for hash tables
	Version   int32           // Version ID
	LOrder    uint32          // Byte Order
	BSize     int32           // Bucket/Page Size
	BShift    int32           // Bucket shift
	DSize     int32           // Directory Size
	SSize     int32           // Segment Size
	SShift    int32           // Segment shift
	OvflPoint int32           // Where overflow pages are being allocated
	LastFreed int32           // Last overflow page freed
	MaxBucket int32           // ID of Maximum bucket in use
	HighMask  int32           // Mask to modulo into entire table
	LowMask   int32           // Mask to modulo into lower half of table
	FFactor   int32           // Fill factor
	NKeys     int32           // Number of keys in hash table
	HdrPages  int32           // Size of table header
	HCharkey  int32           // value of hash(CHARKEY)
	Spares    [nCached]int32  // spare pages for overflow
	Bitmaps   [nCached]uint16 // address of overflow page bitmaps
}

const (
	hashMagic   = 0x061561
	hashVersion = 2
)
